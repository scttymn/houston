// Package server is the houston CLI's client for Mission Control's remote
// API (/api/v1), with a named personal token: houston --server commands, and
// agents. It never sees a secret's value; that stays on the runner's local
// API (internal/mission).
package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ErrRefused: Mission Control refused the API token.
var ErrRefused = errors.New("the API token was refused (revoked?); run houston login")

// Config is where the server is and how to reach it.
type Config struct {
	URL                string `json:"url"`
	Token              string `json:"token"`
	AccessClientID     string `json:"access_client_id,omitempty"`
	AccessClientSecret string `json:"access_client_secret,omitempty"`
	SSH                string `json:"ssh,omitempty"` // houston@<LAN address>, for console --server
}

func configPath(home string) string { return filepath.Join(home, ".config", "houston", "server.json") }

// Load reads HOUSTON_SERVER and HOUSTON_API_TOKEN (agents: both, no file
// needed), else the file houston login saved. HOUSTON_ACCESS_CLIENT_ID and
// _SECRET add Cloudflare Access service-token headers either way.
func Load(getenv func(string) string, home string) (Config, error) {
	var c Config
	url, token := getenv("HOUSTON_SERVER"), getenv("HOUSTON_API_TOKEN")
	switch {
	case url != "" && token != "":
		c = Config{URL: url, Token: token}
	case url != "" || token != "":
		return c, errors.New("set both HOUSTON_SERVER and HOUSTON_API_TOKEN (or neither, and run houston login)")
	default:
		data, err := os.ReadFile(configPath(home))
		if err != nil {
			return c, errors.New("not logged in to a server: run houston login <url>, or set HOUSTON_SERVER and HOUSTON_API_TOKEN")
		}
		if err := json.Unmarshal(data, &c); err != nil {
			return c, fmt.Errorf("%s is unreadable: %v; run houston login again", configPath(home), err)
		}
	}
	if id := getenv("HOUSTON_ACCESS_CLIENT_ID"); id != "" {
		c.AccessClientID, c.AccessClientSecret = id, getenv("HOUSTON_ACCESS_CLIENT_SECRET")
	}
	c.URL = strings.TrimRight(c.URL, "/")
	return c, nil
}

// Save writes the config for the current user only (dir 0700, file 0600),
// replacing any earlier one in one step.
func Save(home string, c Config) error {
	dir := filepath.Dir(configPath(home))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".server-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), configPath(home))
}

// Remove forgets the saved server.
func Remove(home string) error {
	err := os.Remove(configPath(home))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

type Client struct {
	c    Config
	http *http.Client
}

func New(c Config) *Client { return &Client{c: c, http: &http.Client{Timeout: 30 * time.Second}} }

// Me is the token's name and the server's base domain.
type Me struct {
	Token    string `json:"token"`
	Server   string `json:"server"`
	Version  string `json:"version"`  // the Houston release it runs, "source <sha>" or "dev"
	Latest   string `json:"latest"`   // a newer release, when one is out
	Updating string `json:"updating"` // the release the server is updating to, while it does
}

func (cl *Client) Me(ctx context.Context) (Me, error) {
	var me Me
	return me, cl.getJSON(ctx, "/api/v1/me", &me)
}

func (cl *Client) getJSON(ctx context.Context, path string, into any) error {
	status, body, err := cl.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("%s", message(body))
	}
	return json.Unmarshal(body, into)
}

func (cl *Client) do(ctx context.Context, method, path string, payload any) (int, []byte, error) {
	var reqBody io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return 0, nil, err
		}
		reqBody = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, cl.c.URL+path, reqBody)
	if err != nil {
		return 0, nil, err
	}
	cl.headers(req)
	req.Header.Set("Content-Type", "application/json")
	res, err := cl.http.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("can't reach %s: %w", cl.c.URL, err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 16<<20))
	if err != nil {
		return 0, nil, err
	}
	if res.StatusCode == http.StatusUnauthorized {
		return 0, nil, ErrRefused
	}
	return res.StatusCode, body, nil
}

func message(body []byte) string {
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &e) == nil && e.Error != "" {
		return e.Error
	}
	return strings.TrimSpace(string(body))
}

// DomainState is a custom domain's DNS: DNS OK, DNS PENDING, WILDCARD, …
type DomainState struct {
	State  string `json:"state"`
	Reason string `json:"reason"`
}

type Deploy struct {
	Number     int     `json:"number"`
	Status     string  `json:"status"` // queued, in_flight, go, no_go
	SHA        string  `json:"sha"`
	Ref        string  `json:"ref"`
	Step       string  `json:"step"`
	Error      string  `json:"error"`
	Runner     string  `json:"runner"`
	StartedAt  string  `json:"started_at"`
	FinishedAt *string `json:"finished_at"`
	Duration   int     `json:"duration"`
	// With a single deploy: its steps and a piece of its log.
	Steps   []Step `json:"steps"`
	Log     string `json:"log"`
	LogSize int    `json:"log_size"`
	LogNext int    `json:"log_next"`
}

type Step struct {
	Name  string `json:"name"`
	State string `json:"state"` // done, current, failed, pending, skipped
}

type SecretSummary struct {
	Name     string `json:"name"`
	Required bool   `json:"required"`
	Set      bool   `json:"set"`
}

type Project struct {
	Name       string                 `json:"name"`
	Status     string                 `json:"status"` // queued, in_flight, go, no_go, standby
	RunningSHA string                 `json:"running_sha"`
	Host       string                 `json:"host"`
	Domains    map[string]DomainState `json:"domains"`
	LastDeploy *Deploy                `json:"last_deploy"`
	// Only for a single project:
	DeployRule struct {
		On     string `json:"on"`
		Branch string `json:"branch"`
		Tags   string `json:"tags"`
	} `json:"deploy_rule"`
	Services        []string        `json:"services"`
	RepoURL         string          `json:"repo_url"`
	Branch          string          `json:"branch"`
	WebhookVerified bool            `json:"webhook_verified"`
	Secrets         []SecretSummary `json:"secrets"`
	LastBackup      *Backup         `json:"last_backup"`
	Maintenance     Maintenance     `json:"maintenance"`
	BackupSchedule  string          `json:"backup_schedule"`
	TimeZone        string          `json:"time_zone"`
}

// Settings are Houston's own: its base domain, and the time zone backup
// schedules run in.
type Settings struct {
	BaseDomain string `json:"base_domain"`
	TimeZone   string `json:"time_zone"`
}

// Volume is one of a project's named volumes and where it lives (Location
// empty: local disk).
type Volume struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	Location string `json:"location"`
	Placed   bool   `json:"placed"`
}

func (cl *Client) Volumes(ctx context.Context, project string) ([]Volume, error) {
	var body struct {
		Volumes []Volume `json:"volumes"`
	}
	return body.Volumes, cl.getJSON(ctx, "/api/v1/projects/"+url.PathEscape(project)+"/volumes", &body)
}

// PlaceVolume chooses where a volume will live (location "": local disk),
// until Houston has made it.
func (cl *Client) PlaceVolume(ctx context.Context, project, volume, location string) (Volume, error) {
	var v Volume
	var loc any
	if location != "" {
		loc = location
	}
	status, body, err := cl.do(ctx, http.MethodPatch, "/api/v1/projects/"+url.PathEscape(project)+"/volumes/"+url.PathEscape(volume), map[string]any{"location": loc})
	if err != nil {
		return v, err
	}
	if status != http.StatusOK {
		return v, fmt.Errorf("%s", message(body))
	}
	return v, json.Unmarshal(body, &v)
}

// Maintenance is Houston's maintenance page for a project: the admin's switch.
type Maintenance struct {
	On      bool       `json:"on"`
	Since   *time.Time `json:"since"`
	By      string     `json:"by"`
	Message string     `json:"message"`
}

// SetMaintenance turns a project's maintenance page on (with an optional
// message) or off.
func (cl *Client) SetMaintenance(ctx context.Context, project string, on bool, text string) (Maintenance, error) {
	body := map[string]any{"on": on}
	if on && text != "" {
		body["message"] = text
	}
	var m Maintenance
	status, resp, err := cl.do(ctx, http.MethodPut, "/api/v1/projects/"+url.PathEscape(project)+"/maintenance", body)
	if err != nil {
		return m, err
	}
	if status != http.StatusOK {
		return m, fmt.Errorf("%s", message(resp))
	}
	return m, json.Unmarshal(resp, &m)
}

// Location is a storage location, as the API lists it: never a password or
// a credential.
type Location struct {
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	Where      string `json:"where"`
	Live       bool   `json:"live"`
	Default    bool   `json:"default"`
	Confirmed  bool   `json:"confirmed"`
	UsedBy     int    `json:"used_by"`
	PruneError string `json:"prune_error"`
}

func (cl *Client) Storage(ctx context.Context) ([]Location, error) {
	var body struct {
		Locations []Location `json:"locations"`
	}
	return body.Locations, cl.getJSON(ctx, "/api/v1/storage", &body)
}

// MakeDefault makes a location the default backup target.
func (cl *Client) MakeDefault(ctx context.Context, name string) error {
	return cl.patch(ctx, "/api/v1/storage/"+url.PathEscape(name), map[string]any{"default": true}, &struct{}{})
}

// UseStorage sets where a project backs up (location "": the default) and
// returns where that is now.
func (cl *Client) UseStorage(ctx context.Context, project, location string) (string, error) {
	var loc any
	if location != "" {
		loc = location
	}
	var body struct {
		BackupLocation string `json:"backup_location"`
	}
	err := cl.patch(ctx, "/api/v1/projects/"+url.PathEscape(project)+"/backup_target", map[string]any{"location": loc}, &body)
	return body.BackupLocation, err
}

func (cl *Client) patch(ctx context.Context, path string, payload, into any) error {
	status, body, err := cl.do(ctx, http.MethodPatch, path, payload)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("%s", message(body))
	}
	return json.Unmarshal(body, into)
}

func (cl *Client) Settings(ctx context.Context) (Settings, error) {
	var s Settings
	return s, cl.getJSON(ctx, "/api/v1/settings", &s)
}

// SetTimeZone changes Houston's time zone (an IANA name).
func (cl *Client) SetTimeZone(ctx context.Context, zone string) (Settings, error) {
	var s Settings
	status, body, err := cl.do(ctx, http.MethodPatch, "/api/v1/settings", map[string]string{"time_zone": zone})
	if err != nil {
		return s, err
	}
	if status != http.StatusOK {
		return s, fmt.Errorf("%s", message(body))
	}
	return s, json.Unmarshal(body, &s)
}

// Snapshot is one restic snapshot of a project (code and data together).
type Snapshot struct {
	ID      string    `json:"id"`
	ShortID string    `json:"short_id"`
	Time    time.Time `json:"time"`
	Kind    string    `json:"kind"`   // auto, deploy
	Reason  string    `json:"reason"` // schedule, manual, deploy, restore
	Deploy  int       `json:"deploy"`
	SHA     string    `json:"sha"`
	Bytes   int64     `json:"bytes"`
}

// Backup is one backup run.
type Backup struct {
	ID         int        `json:"id"`
	Status     string     `json:"status"` // queued, running, go, no_go, skipped
	Kind       string     `json:"kind"`
	Reason     string     `json:"reason"`
	SnapshotID string     `json:"snapshot_id"`
	Bytes      int64      `json:"bytes"`
	Error      string     `json:"error"`
	FinishedAt *time.Time `json:"finished_at"`
}

func (cl *Client) Snapshots(ctx context.Context, project string) ([]Snapshot, error) {
	var body struct {
		Snapshots []Snapshot `json:"snapshots"`
	}
	return body.Snapshots, cl.getJSON(ctx, "/api/v1/projects/"+url.PathEscape(project)+"/snapshots", &body)
}

// BackupNow queues a backup (or returns the one already queued).
func (cl *Client) BackupNow(ctx context.Context, project string) (Backup, error) {
	var b Backup
	return b, cl.postJSON(ctx, "/api/v1/projects/"+url.PathEscape(project)+"/backups", nil, &b)
}

// Backup is one backup run by id, or "latest".
func (cl *Client) Backup(ctx context.Context, project, id string) (Backup, error) {
	var b Backup
	return b, cl.getJSON(ctx, "/api/v1/projects/"+url.PathEscape(project)+"/backups/"+url.PathEscape(id), &b)
}

func (cl *Client) Projects(ctx context.Context) ([]Project, error) {
	var body struct {
		Projects []Project `json:"projects"`
	}
	return body.Projects, cl.getJSON(ctx, "/api/v1/projects", &body)
}

func (cl *Client) Project(ctx context.Context, name string) (Project, error) {
	var p Project
	return p, cl.getJSON(ctx, "/api/v1/projects/"+url.PathEscape(name), &p)
}

func (cl *Client) Deploys(ctx context.Context, project string, page int) ([]Deploy, error) {
	var body struct {
		Deploys []Deploy `json:"deploys"`
	}
	return body.Deploys, cl.getJSON(ctx, fmt.Sprintf("/api/v1/projects/%s/deploys?page=%d", url.PathEscape(project), page), &body)
}

// Deploy is one deploy (a number, or "latest") with its log from byte from.
func (cl *Client) Deploy(ctx context.Context, project, number string, from int) (Deploy, error) {
	var d Deploy
	return d, cl.getJSON(ctx, fmt.Sprintf("/api/v1/projects/%s/deploys/%s?log_from=%d", url.PathEscape(project), url.PathEscape(number), from), &d)
}

// Raw is the API's JSON for path, for --json.
func (cl *Client) Raw(ctx context.Context, path string) ([]byte, error) {
	status, body, err := cl.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("%s", message(body))
	}
	return body, nil
}

func (cl *Client) Secrets(ctx context.Context, project string) ([]SecretSummary, error) {
	var body struct {
		Secrets []SecretSummary `json:"secrets"`
	}
	return body.Secrets, cl.getJSON(ctx, "/api/v1/projects/"+url.PathEscape(project)+"/secrets", &body)
}

// SetSecret sends a value; Mission Control never sends one back.
func (cl *Client) SetSecret(ctx context.Context, project, key, value string) error {
	return cl.send(ctx, http.MethodPut, secretPath(project, key), map[string]string{"value": value})
}

func (cl *Client) UnsetSecret(ctx context.Context, project, key string) error {
	return cl.send(ctx, http.MethodDelete, secretPath(project, key), nil)
}

// GenerateSecret stores a random value nobody sees.
func (cl *Client) GenerateSecret(ctx context.Context, project, key string) error {
	return cl.send(ctx, http.MethodPost, secretPath(project, key)+"/generate", nil)
}

func secretPath(project, key string) string {
	return "/api/v1/projects/" + url.PathEscape(project) + "/secrets/" + url.PathEscape(key)
}

func (cl *Client) send(ctx context.Context, method, path string, payload any) error {
	status, body, err := cl.do(ctx, method, path, payload)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("%s", message(body))
	}
	return nil
}

// DeployNow queues the head of what the project's deploy rule matches.
// Restore queues a restore of project to a snapshot (location "": the
// project's backup location); confirm must be the project's name.
func (cl *Client) Restore(ctx context.Context, project, snapshot, location, confirm string) (Deploy, error) {
	body := map[string]string{"snapshot": snapshot, "confirm": confirm}
	if location != "" {
		body["location"] = location
	}
	var d Deploy
	return d, cl.postJSON(ctx, "/api/v1/projects/"+url.PathEscape(project)+"/restores", body, &d)
}

func (cl *Client) DeployNow(ctx context.Context, project string) (Deploy, error) {
	var d Deploy
	return d, cl.postJSON(ctx, "/api/v1/projects/"+url.PathEscape(project)+"/deploys", nil, &d)
}

type Access struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

type Link struct {
	ID        int    `json:"id"`
	DeployKey string `json:"deploy_key"`
	Access    Access `json:"access"`
}

type LinkRead struct {
	OK    bool `json:"ok"`
	Found struct {
		Name      string   `json:"name"`
		SHA       string   `json:"sha"`
		Branch    string   `json:"branch"`
		Domains   []string `json:"domains"`
		Variables []struct {
			Name     string `json:"name"`
			Required bool   `json:"required"`
		} `json:"variables"`
	} `json:"found"`
	Problems string `json:"problems"`
}

// Webhook is a project's webhook. Secret is nil once a push has arrived.
type Webhook struct {
	URL      string  `json:"url"`
	Verified bool    `json:"verified"`
	Secret   *string `json:"secret"`
}

// LinkSaved is a linked project and its webhook (the secret, until the
// first push arrives).
type LinkSaved struct {
	Project       string  `json:"project"`
	WebhookURL    string  `json:"webhook_url"`
	WebhookSecret *string `json:"webhook_secret"`
}

func (cl *Client) CreateLink(ctx context.Context, repoURL string) (Link, error) {
	var l Link
	return l, cl.postJSON(ctx, "/api/v1/links", map[string]string{"repo_url": repoURL}, &l)
}

func (cl *Client) LinkAccess(ctx context.Context, id int) (Access, error) {
	var a Access
	return a, cl.postJSON(ctx, fmt.Sprintf("/api/v1/links/%d/access", id), nil, &a)
}

func (cl *Client) LinkRead(ctx context.Context, id int, branch, composePath string) (LinkRead, error) {
	var r LinkRead
	return r, cl.postJSON(ctx, fmt.Sprintf("/api/v1/links/%d/read", id), map[string]string{"branch": branch, "compose_path": composePath}, &r)
}

func (cl *Client) LinkSave(ctx context.Context, id int) (LinkSaved, error) {
	var l LinkSaved
	return l, cl.postJSON(ctx, fmt.Sprintf("/api/v1/links/%d/save", id), nil, &l)
}

func (cl *Client) Webhook(ctx context.Context, project string, rotate bool) (Webhook, error) {
	var w Webhook
	path := "/api/v1/projects/" + url.PathEscape(project) + "/webhook"
	if rotate {
		return w, cl.postJSON(ctx, path+"/rotate", nil, &w)
	}
	return w, cl.getJSON(ctx, path, &w)
}

func (cl *Client) postJSON(ctx context.Context, path string, payload, into any) error {
	status, body, err := cl.do(ctx, http.MethodPost, path, payload)
	if err != nil {
		return err
	}
	if status < 200 || status > 299 {
		return fmt.Errorf("%s", message(body))
	}
	return json.Unmarshal(body, into)
}

// Logs copies the running app's output to w; with follow, until the server
// ends it (an hour at most) or ctx does.
func (cl *Client) Logs(ctx context.Context, project string, follow bool, tail int, w io.Writer) error {
	q := url.Values{"tail": {fmt.Sprint(tail)}}
	if follow {
		q.Set("follow", "1")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cl.c.URL+"/api/v1/projects/"+url.PathEscape(project)+"/logs?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	cl.headers(req)
	res, err := (&http.Client{}).Do(req) // no timeout: a followed log runs as long as it runs
	if err != nil {
		return fmt.Errorf("can't reach %s: %w", cl.c.URL, err)
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusUnauthorized {
		return ErrRefused
	}
	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
		return fmt.Errorf("%s", message(body))
	}
	_, err = io.Copy(w, res.Body)
	return err
}

func (cl *Client) headers(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+cl.c.Token)
	if cl.c.AccessClientID != "" {
		req.Header.Set("CF-Access-Client-Id", cl.c.AccessClientID)
		req.Header.Set("CF-Access-Client-Secret", cl.c.AccessClientSecret)
	}
}

// SSHTarget is where console --server connects: HOUSTON_SSH, else the saved one.
func (cl *Client) SSHTarget(getenv func(string) string) string {
	if t := getenv("HOUSTON_SSH"); t != "" {
		return t
	}
	return cl.c.SSH
}

// Cloudflare (docs/plans/cloudflare-settings.md): the view, a new token, repair.
type CloudflareView struct {
	Tunnel *struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Status string `json:"status"`
	} `json:"tunnel"`
	Connections []struct {
		Colo    string `json:"colo"`
		Version string `json:"version"`
		Origin  string `json:"origin"`
		Since   string `json:"since"`
	} `json:"connections"`
	Routes        []CloudflareRoute `json:"routes"`
	MissingRoutes []CloudflareRoute `json:"missing_routes"`
	Drift         bool              `json:"drift"`
	Records       []struct {
		Zone    string `json:"zone"`
		Name    string `json:"name"`
		Project string `json:"project"`
		Proxied bool   `json:"proxied"`
		Here    bool   `json:"here"`
	} `json:"records"`
	Problems []string `json:"problems"`
}

type CloudflareRoute struct {
	Hostname *string `json:"hostname"`
	Path     *string `json:"path"`
	Service  string  `json:"service"`
	Drift    bool    `json:"drift"`
}

type CloudflareTokenResult struct {
	Replaced bool `json:"replaced"`
	Checks   []struct {
		OK    bool   `json:"ok"`
		Label string `json:"label"`
	} `json:"checks"`
}

type CloudflareRepairResult struct {
	Item   string `json:"item"`
	State  string `json:"state"`
	Reason string `json:"reason"`
}

func (cl *Client) Cloudflare(ctx context.Context) (CloudflareView, error) {
	var v CloudflareView
	return v, cl.getJSON(ctx, "/api/v1/cloudflare", &v)
}

// ReplaceCloudflareToken sends a new token; a refused one (422) still
// returns its checks, so they can be shown.
func (cl *Client) ReplaceCloudflareToken(ctx context.Context, token string) (CloudflareTokenResult, error) {
	var res CloudflareTokenResult
	status, body, err := cl.do(ctx, http.MethodPut, "/api/v1/cloudflare/token", map[string]string{"token": token})
	if err != nil {
		return res, err
	}
	if status != http.StatusOK && status != http.StatusUnprocessableEntity {
		return res, fmt.Errorf("%s", message(body))
	}
	return res, json.Unmarshal(body, &res)
}

func (cl *Client) RepairCloudflare(ctx context.Context) ([]CloudflareRepairResult, error) {
	var res struct {
		Results []CloudflareRepairResult `json:"results"`
	}
	return res.Results, cl.postJSON(ctx, "/api/v1/cloudflare/repair", nil, &res)
}

// PortView is Settings › Port 3000: what it's bound to now (Address empty
// when Mission Control can't tell), and the saved choice.
type PortView struct {
	Open    bool   `json:"open"`
	Address string `json:"address"`
	Saved   string `json:"saved"`
	Message string `json:"message"`
}

func (cl *Client) Port(ctx context.Context) (PortView, error) {
	var v PortView
	return v, cl.getJSON(ctx, "/api/v1/port", &v)
}

// SetPort opens or closes port 3000; Mission Control restarts to apply it.
func (cl *Client) SetPort(ctx context.Context, open bool) (PortView, error) {
	var v PortView
	status, body, err := cl.do(ctx, http.MethodPut, "/api/v1/port", map[string]bool{"open": open})
	if err != nil {
		return v, err
	}
	if status != http.StatusAccepted {
		return v, fmt.Errorf("%s", message(body))
	}
	return v, json.Unmarshal(body, &v)
}

// UpdateView is the server's version, a newer release, and the last update
// started from Mission Control (docs/plans/update-from-mission-control.md).
type UpdateView struct {
	Version string        `json:"version"`
	Latest  string        `json:"latest"`
	Update  *ServerUpdate `json:"update"`
	Message string        `json:"message"`
}

// ServerUpdate is one update: running, go, rolled_back or no_go.
type ServerUpdate struct {
	ID     int    `json:"id"`
	To     string `json:"to"`
	From   string `json:"from"`
	Status string `json:"status"`
	Log    string `json:"log"`
}

func (cl *Client) Update(ctx context.Context) (UpdateView, error) {
	var v UpdateView
	return v, cl.getJSON(ctx, "/api/v1/update", &v)
}

// StartUpdate updates the server to version ("" for the latest release).
func (cl *Client) StartUpdate(ctx context.Context, version string) (UpdateView, error) {
	var v UpdateView
	payload := map[string]string{}
	if version != "" {
		payload["version"] = version
	}
	status, body, err := cl.do(ctx, http.MethodPost, "/api/v1/update", payload)
	if err != nil {
		return v, err
	}
	if status != http.StatusAccepted {
		return v, fmt.Errorf("%s", message(body))
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return v, err
	}
	if v.Update == nil {
		return v, fmt.Errorf("the server didn't say which update it started")
	}
	return v, nil
}
