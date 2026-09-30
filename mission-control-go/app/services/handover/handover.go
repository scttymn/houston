// Package handover moves the hosts an old project and its copy share in
// kamal-proxy, and their DNS records' comments (the Rails app's Handover):
// forward at the copy's Handover step, back when a copy is undone. A move
// that fails puts the services back as they were.
package handover

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/scttymn/gantry/db"

	"github.com/scttymn/houston/mission-control-go/app/models"
	"github.com/scttymn/houston/mission-control-go/app/services/cloudflare"
	"github.com/scttymn/houston/mission-control-go/app/services/dockercmd"
)

const (
	state         = "/home/kamal-proxy/.config/kamal-proxy/kamal-proxy.state"
	catchAll      = "houston-handover"
	defaultBuffer = 1048576
)

// Failed is a handover that didn't happen, in words for the runner.
type Failed string

func (f Failed) Error() string { return string(f) }

// Handover is what it works with.
type Handover struct {
	DB           *db.DB
	Docker       dockercmd.Runner
	Installation models.Installation
	Cloudflare   string // its API's address ("": Cloudflare's own)
}

// service is one of kamal-proxy's, as its state has it.
type service struct {
	Name    string
	Hosts   []string
	Target  string
	Options targetOptions
}

type targetOptions struct {
	HealthCheck struct {
		Path     string `json:"path"`
		Port     int    `json:"port"`
		Host     string `json:"host"`
		Interval int64  `json:"interval"`
		Timeout  int64  `json:"timeout"`
	} `json:"health_check_config"`
	ResponseTimeout    int64    `json:"response_timeout"`
	BufferRequests     bool     `json:"buffer_requests"`
	BufferResponses    bool     `json:"buffer_responses"`
	MaxMemoryBuffer    int64    `json:"max_memory_buffer_size"`
	MaxRequestBody     int64    `json:"max_request_body_size"`
	MaxResponseBody    int64    `json:"max_response_body_size"`
	LogRequestHeaders  []string `json:"log_request_headers"`
	LogResponseHeaders []string `json:"log_response_headers"`
	ForwardHeaders     bool     `json:"forward_headers"`
}

func (h Handover) readState(ctx context.Context) (map[string]service, error) {
	ran := h.Docker.Run(ctx, []string{"exec", "kamal-proxy", "cat", state}, dockercmd.Opts{Timeout: 10 * time.Second})
	if !ran.OK {
		out := strings.TrimSpace(ran.Output)
		if len(out) > 300 {
			out = out[:297] + "..."
		}
		return nil, Failed("couldn't read kamal-proxy's state: " + out)
	}
	var raw []struct {
		Name    string `json:"name"`
		Options struct {
			Hosts []string `json:"hosts"`
		} `json:"options"`
		ActiveTargets []string      `json:"active_targets"`
		TargetOptions targetOptions `json:"target_options"`
	}
	if json.Unmarshal([]byte(ran.Output), &raw) != nil {
		return nil, Failed("kamal-proxy's state isn't JSON")
	}
	services := map[string]service{}
	for _, s := range raw {
		svc := service{Name: s.Name, Hosts: s.Options.Hosts, Options: s.TargetOptions}
		if len(s.ActiveTargets) > 0 {
			svc.Target = s.ActiveTargets[0]
		}
		services[s.Name] = svc
	}
	return services, nil
}

// Forward hands the hosts the old project and the copy share to the copy,
// and is them.
func (h Handover) Forward(ctx context.Context, c models.ProjectCopy, to models.Project) ([]string, error) {
	services, err := h.readState(ctx)
	if err != nil {
		return nil, err
	}
	taker, ok := services[c.ToName+"-web"]
	if !ok {
		return nil, Failed(c.ToName + "-web isn't in kamal-proxy yet")
	}
	wanted := to.Hostnames(h.Installation.BaseDomain)
	var giver *service
	shared := []string{}
	if g, ok := services[c.FromName+"-web"]; ok {
		giver = &g
		for _, host := range g.Hosts {
			if slices.Contains(wanted, host) {
				shared = append(shared, host)
			}
		}
	}
	var giverHosts []string
	if giver != nil {
		for _, host := range giver.Hosts {
			if !slices.Contains(shared, host) {
				giverHosts = append(giverHosts, host)
			}
		}
	}
	if err := h.move(ctx, giver, taker, shared, giverHosts, wanted, c.FromName, c.FromName); err != nil {
		return nil, err
	}
	if err := h.records(ctx, shared, c.FromName, c.ToName, func() { h.moveBack(ctx, services, c.FromName) }); err != nil {
		return nil, err
	}
	now := time.Now()
	return shared, models.New(h.DB.Write).SetHandedOver(ctx, models.SetHandedOverParams{HandedOver: models.Names{V: shared},
		HandedOverAt: sql.NullTime{Time: now, Valid: true}, UpdatedAt: now, ID: c.ID})
}

// Back hands the hosts a copy took back to the old project.
func (h Handover) Back(ctx context.Context, c models.ProjectCopy) error {
	services, err := h.readState(ctx)
	if err != nil {
		return err
	}
	shared := c.HandedOver.V
	giver, ok := services[c.ToName+"-web"]
	if !ok {
		return Failed(c.ToName + "-web isn't in kamal-proxy")
	}
	taker, ok := services[c.FromName+"-web"]
	if !ok {
		return Failed(c.FromName + "-web isn't in kamal-proxy any more: its container is gone")
	}
	var giverHosts []string
	for _, host := range giver.Hosts {
		if !slices.Contains(shared, host) {
			giverHosts = append(giverHosts, host)
		}
	}
	takerHosts := slices.Clone(taker.Hosts)
	for _, host := range shared {
		if !slices.Contains(takerHosts, host) {
			takerHosts = append(takerHosts, host)
		}
	}
	if err := h.move(ctx, &giver, taker, shared, giverHosts, takerHosts, c.ToName, c.FromName); err != nil {
		return err
	}
	if err := h.records(ctx, shared, c.ToName, c.FromName, func() { h.moveBack(ctx, services, c.FromName) }); err != nil {
		return err
	}
	now := time.Now()
	return models.New(h.DB.Write).SetHandedOver(ctx, models.SetHandedOverParams{HandedOver: models.Names{V: []string{}}, UpdatedAt: now, ID: c.ID})
}

// move redeploys the services with their new hosts in one shell: a
// catch-all first while hosts are between them, the giver left a
// placeholder if it has none, then the taker.
func (h Handover) move(ctx context.Context, giver *service, taker service, shared, giverHosts, takerHosts []string, giverName, from string) error {
	var steps []string
	if len(shared) > 0 {
		steps = append(steps, deploy(catchAll, taker, nil))
	}
	if giver != nil && len(shared) > 0 {
		hosts := giverHosts
		if len(hosts) == 0 {
			hosts = []string{models.Placeholder(giverName)}
		}
		steps = append(steps, deploy(giver.Name, *giver, hosts))
	}
	steps = append(steps, deploy(taker.Name, taker, takerHosts))
	if len(shared) > 0 {
		steps = append(steps, "kamal-proxy remove "+catchAll)
	}
	ran := h.Docker.Run(ctx, []string{"exec", "kamal-proxy", "sh", "-c", strings.Join(steps, " && ")}, dockercmd.Opts{Timeout: 2 * time.Minute})
	if ran.OK {
		return nil
	}
	if giver != nil {
		h.moveBack(ctx, map[string]service{giver.Name: *giver, taker.Name: taker}, from)
	}
	lines := strings.Split(strings.TrimSpace(ran.Output), "\n")
	msg := strings.TrimSpace(strings.Join(lines[max(0, len(lines)-3):], "\n"))
	if len(msg) > 500 {
		msg = msg[:497] + "..."
	}
	return Failed("kamal-proxy didn't move the hosts: " + msg)
}

// moveBack puts services as they were, whatever got through: the old
// project (from) last, so the one that took hosts lets go of them first.
func (h Handover) moveBack(ctx context.Context, services map[string]service, from string) {
	names := make([]string, 0, len(services))
	for name := range services {
		names = append(names, name)
	}
	slices.Sort(names)
	last := func(name string) int {
		if name == from+"-web" {
			return 1
		}
		return 0
	}
	slices.SortStableFunc(names, func(a, b string) int { return last(a) - last(b) })
	var steps []string
	for _, name := range names {
		steps = append(steps, deploy(name, services[name], services[name].Hosts))
	}
	steps = append(steps, "kamal-proxy remove "+catchAll+" 2>/dev/null", "true")
	h.Docker.Run(ctx, []string{"exec", "kamal-proxy", "sh", "-c", strings.Join(steps, "; ")}, dockercmd.Opts{Timeout: 2 * time.Minute})
}

var shellSafe = regexp.MustCompile(`^[\w.:/=@-]+$`)

// deploy is kamal-proxy's deploy command for service with hosts, its
// options as they were.
func deploy(name string, s service, hosts []string) string {
	o := s.Options
	args := []string{"kamal-proxy", "deploy", name, "--target", s.Target}
	for _, host := range hosts {
		args = append(args, "--host", host)
	}
	path := o.HealthCheck.Path
	if path == "" {
		path = "/up"
	}
	args = append(args, "--health-check-path", path)
	if o.HealthCheck.Port > 0 {
		args = append(args, "--health-check-port", fmt.Sprint(o.HealthCheck.Port))
	}
	if o.HealthCheck.Host != "" {
		args = append(args, "--health-check-host", o.HealthCheck.Host)
	}
	args = append(args, "--health-check-interval", ms(o.HealthCheck.Interval), "--health-check-timeout", ms(o.HealthCheck.Timeout), "--target-timeout", ms(o.ResponseTimeout))
	if o.BufferRequests {
		args = append(args, "--buffer-requests")
	}
	if o.BufferResponses {
		args = append(args, "--buffer-responses")
	}
	if o.MaxMemoryBuffer > 0 && o.MaxMemoryBuffer != defaultBuffer {
		args = append(args, "--buffer-memory", fmt.Sprint(o.MaxMemoryBuffer))
	}
	if o.MaxRequestBody > 0 {
		args = append(args, "--max-request-body", fmt.Sprint(o.MaxRequestBody))
	}
	if o.MaxResponseBody > 0 {
		args = append(args, "--max-response-body", fmt.Sprint(o.MaxResponseBody))
	}
	for _, header := range o.LogRequestHeaders {
		args = append(args, "--log-request-header", header)
	}
	for _, header := range o.LogResponseHeaders {
		args = append(args, "--log-response-header", header)
	}
	args = append(args, fmt.Sprintf("--forward-headers=%t", o.ForwardHeaders))
	for i, a := range args {
		if !shellSafe.MatchString(a) {
			args[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		}
	}
	return strings.Join(args, " ")
}

func ms(ns int64) string { return fmt.Sprintf("%dms", ns/1_000_000) }

// records re-comments the shared hosts' DNS records from one project to
// the other; on Cloudflare's no, the ones moved go back, then undo runs.
func (h Handover) records(ctx context.Context, hosts []string, from, to string, undo func()) error {
	inst := h.Installation
	if len(hosts) == 0 || !inst.CloudflareConnectedAt.Valid || inst.CloudflareApiToken.Reveal() == "" {
		return nil
	}
	client := cloudflare.Client{Token: inst.CloudflareApiToken.Reveal(), Base: h.Cloudflare}
	mine, theirs := cloudflare.Managed+" project:"+from, cloudflare.Managed+" project:"+to
	type moved struct{ zone, id string }
	var done []moved
	var err error
	for _, host := range hosts {
		var zone string
		if zone, err = h.zoneOf(ctx, client, host); err != nil {
			break
		}
		if zone == "" {
			continue
		}
		var found []cloudflare.Record
		if err = client.Get(ctx, "/zones/"+zone+"/dns_records", url.Values{"name": {host}}, &found); err != nil {
			break
		}
		if len(found) == 0 || found[0].Comment != mine {
			continue
		}
		if err = client.Patch(ctx, "/zones/"+zone+"/dns_records/"+found[0].ID, map[string]string{"comment": theirs}, nil); err != nil {
			break
		}
		done = append(done, moved{zone, found[0].ID})
	}
	if err == nil {
		return nil
	}
	for _, m := range done {
		client.Patch(ctx, "/zones/"+m.zone+"/dns_records/"+m.id, map[string]string{"comment": mine}, nil)
	}
	undo()
	var cf *cloudflare.Error
	if errors.As(err, &cf) {
		return Failed("Cloudflare said no while moving the hosts' records: " + cf.Msg)
	}
	return err
}

func (h Handover) zoneOf(ctx context.Context, client cloudflare.Client, host string) (string, error) {
	if strings.HasSuffix(host, "."+h.Installation.BaseDomain) {
		return h.Installation.CloudflareZoneID, nil
	}
	labels := strings.Split(host, ".")
	for i := 0; i < len(labels)-1; i++ {
		zone, err := client.FindZone(ctx, strings.Join(labels[i:], "."))
		if err != nil || zone != nil {
			if zone != nil {
				return zone.ID, nil
			}
			return "", err
		}
	}
	return "", nil
}
