package models

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The rules for what a sync names (the Rails app's ProjectSync).
var (
	nameFormat     = regexp.MustCompile(`^[a-z]([a-z0-9-]{0,61}[a-z0-9])?$`)
	serviceFormat  = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)
	variableFormat = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	labelFormat    = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	healthFormat   = regexp.MustCompile(`^/\S*$`)
	// ScheduleFormat is a backup schedule: "daily HH:MM".
	ScheduleFormat = regexp.MustCompile(`^daily ([01][0-9]|2[0-3]):([0-5][0-9])$`)
)

// Reserved are names no project may have: Mission Control's own hosts.
var Reserved = []string{"admin", "hooks"}

const (
	maxVolumes         = 50
	maxMaintenancePage = 512 << 10
)

// ValidName is whether name can be a project's: a DNS label, not reserved.
func ValidName(name string) bool {
	for _, r := range Reserved {
		if name == r {
			return false
		}
	}
	return nameFormat.MatchString(name)
}

// Sync is what houston deploy read from a project's compose.yml, sent to
// POST /api/projects/sync, checked field by field (the Rails app's
// ProjectSync): Mission Control doesn't trust its callers' parsing.
type Sync struct {
	Name       string
	AppService string
	Services   []string
	Domains    []string
	Variables  []Variable
	Health     string
	Port       int
	// DeployRule is compose.yml's, stored as it came: an object.
	DeployRule      json.RawMessage
	Volumes         []Volume
	Databases       []Database
	KeepAuto        int
	KeepDeploy      int
	BackupSchedule  string
	MaintenancePage string
	Details         Details
	// RestoreDeploy is set when this is a restore's check of its snapshot's
	// compose.yml; ClaimedDeploy when a runner syncs for the deploy it
	// claimed. Either is 0 when what was sent isn't an id (no deploy has it).
	RestoreDeploy, ClaimedDeploy *int64
	// ServingGeneration and ServingSHA are the app kamal-proxy routes to, as
	// the runner read it (0 and "": it can't say).
	ServingGeneration int
	ServingSHA        string
}

// Variable is one a compose file references; Required ones need a value
// before a deploy goes on.
type Variable struct {
	Name     string `json:"name"`
	Required bool   `json:"required"`
}

// Volume is a named volume of the app's, and where it's mounted.
type Volume struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// Database is an accessory a backup dumps.
type Database struct {
	Service string `json:"service"`
	Image   string `json:"image"`
}

// Details are what the project page shows; Houston acts on none of them.
type Details struct {
	Images  map[string]string `json:"images,omitempty"`
	CPUs    string            `json:"cpus,omitempty"`
	Memory  string            `json:"memory,omitempty"`
	Console string            `json:"console,omitempty"`
}

// ParseSync reads a sync's fields, and is what's wrong with them, field by
// field, in the Rails app's words (none: it's valid). What an older CLI
// leaves out has its default.
func ParseSync(p map[string]json.RawMessage) (Sync, map[string][]string) {
	errs := map[string][]string{}
	add := func(field, msg string) { errs[field] = append(errs[field], msg) }
	s := Sync{DeployRule: json.RawMessage(`{}`), Volumes: []Volume{}, Databases: []Database{},
		KeepAuto: 14, KeepDeploy: 10, BackupSchedule: "daily 03:00", Details: Details{Images: map[string]string{}}}

	name, ok := str(p["name"])
	switch {
	case !ok || !nameFormat.MatchString(name):
		add("name", "must be a DNS label (a-z, 0-9, -)")
	case !ValidName(name):
		add("name", name+" is reserved")
	}
	s.Name = name

	services, ok := strs(p["services"])
	servicesOK := ok
	if ok && len(services) > 0 {
		for _, sv := range services {
			ok = ok && serviceFormat.MatchString(sv)
		}
	}
	appService, _ := str(p["app_service"])
	switch {
	case !ok || len(services) == 0:
		add("services", "must be a list of compose service names")
	case !contains(services, appService):
		add("app_service", "must be one of the services")
	}
	s.Services, s.AppService = services, appService
	hasService := func(raw json.RawMessage) (string, bool) {
		sv, ok := str(raw)
		return sv, ok && servicesOK && contains(services, sv)
	}

	items, ok := objs(p["variables"])
	for _, v := range items {
		name, isName := str(v["name"])
		ok = ok && isName && variableFormat.MatchString(name)
		s.Variables = append(s.Variables, Variable{Name: name, Required: string(v["required"]) == "true"})
	}
	if !ok {
		add("variables", "must be a list of {name, required} with variable names")
	}

	domains, ok := strs(p["domains"])
	for _, d := range domains {
		ok = ok && domain(d)
	}
	if !ok {
		add("domains", "must be a list of lowercase hostnames")
	}
	s.Domains = domains

	port, ok := integer(p["port"])
	if !ok || port < 1 || port > 65535 {
		add("port", "must be a port number")
	}
	s.Port = int(port)

	health, ok := str(p["health"])
	if !ok || !healthFormat.MatchString(health) {
		add("health", "must be a path starting with /")
	}
	s.Health = health

	if raw := p["deploy_rule"]; !null(raw) {
		if _, ok := obj(raw); ok {
			s.DeployRule = raw
		} else {
			add("deploy_rule", "must be an object")
		}
	}

	if raw, sent := p["volumes"]; sent {
		items, ok := objs(raw)
		ok = ok && len(items) <= maxVolumes
		s.Volumes = s.Volumes[:0]
		for _, v := range items {
			name, isName := str(v["name"])
			path, isPath := str(v["path"])
			ok = ok && isName && serviceFormat.MatchString(name) && isPath && mountPath(path)
			s.Volumes = append(s.Volumes, Volume{Name: name, Path: path})
		}
		if !ok {
			add("volumes", fmt.Sprintf("must be a list of at most %d {name, path} with absolute paths", maxVolumes))
		}
	}

	if raw := p["maintenance_page"]; !null(raw) {
		page, ok := str(raw)
		if !ok || len(page) > maxMaintenancePage {
			add("maintenance_page", "must be the page's HTML, at most 512 KB of UTF-8")
		}
		s.MaintenancePage = page
	}

	if raw := p["backups"]; !null(raw) {
		b, ok := obj(raw)
		keepAuto, autoOK := integer(b["keep_auto"])
		keepDeploy, deployOK := integer(b["keep_deploy"])
		ok = ok && autoOK && deployOK && between(keepAuto, 1, 1000) && between(keepDeploy, 1, 1000)
		if raw := b["schedule"]; !null(raw) {
			schedule, isStr := str(raw)
			ok = ok && isStr && ScheduleFormat.MatchString(schedule)
			s.BackupSchedule = schedule
		}
		if !ok {
			add("backups", `must be {schedule: "daily HH:MM", keep_auto, keep_deploy}, keeps from 1 to 1000`)
		}
		s.KeepAuto, s.KeepDeploy = int(keepAuto), int(keepDeploy)
	}

	if raw, sent := p["details"]; sent {
		d, ok := obj(raw)
		for k := range d {
			ok = ok && contains([]string{"images", "cpus", "memory", "console"}, k)
		}
		images := map[string]json.RawMessage{}
		if raw, sent := d["images"]; sent {
			var isObj bool
			images, isObj = obj(raw)
			ok = ok && isObj
		}
		for service, raw := range images {
			image, isText := shortText(raw, 255)
			ok = ok && servicesOK && contains(services, service) && isText
			s.Details.Images[service] = image
		}
		for _, f := range []struct {
			key  string
			max  int
			into *string
		}{{"cpus", 32, &s.Details.CPUs}, {"memory", 32, &s.Details.Memory}, {"console", 500, &s.Details.Console}} {
			if raw := d[f.key]; !null(raw) {
				text, isText := shortText(raw, f.max)
				ok = ok && isText
				*f.into = text
			}
		}
		if !ok {
			add("details", "must be {images: {service: image}, cpus, memory, console}, as short text")
		}
	}

	if raw, sent := p["databases"]; sent {
		items, ok := objs(raw)
		for _, d := range items {
			service, named := hasService(d["service"])
			image, isStr := str(d["image"])
			ok = ok && named && isStr && strings.TrimSpace(image) != ""
			s.Databases = append(s.Databases, Database{Service: service, Image: image})
		}
		if !ok {
			add("databases", "must be a list of {service, image} naming the project's services")
		}
	}

	if raw, sent := p["restore_deploy"]; sent {
		id, _ := integer(raw)
		s.RestoreDeploy = &id
	}
	if raw, sent := p["claimed_deploy"]; sent {
		id, _ := integer(raw)
		s.ClaimedDeploy = &id
	}
	if g, ok := integer(p["serving_generation"]); ok {
		s.ServingGeneration = int(g)
	}
	s.ServingSHA, _ = str(p["serving_sha"])

	if len(errs) == 0 {
		errs = nil
	}
	return s, errs
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func between(n, lo, hi int64) bool { return n >= lo && n <= hi }

// null is a field absent or JSON null (Ruby's nil).
func null(raw json.RawMessage) bool { return raw == nil || string(raw) == "null" }

func str(raw json.RawMessage) (string, bool) {
	var s string
	if len(raw) == 0 || raw[0] != '"' || json.Unmarshal(raw, &s) != nil {
		return "", false
	}
	return s, true
}

func strs(raw json.RawMessage) ([]string, bool) {
	var items []json.RawMessage
	if len(raw) == 0 || raw[0] != '[' || json.Unmarshal(raw, &items) != nil {
		return nil, false
	}
	out := []string{}
	for _, item := range items {
		s, ok := str(item)
		if !ok {
			return nil, false
		}
		out = append(out, s)
	}
	return out, true
}

func obj(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	var m map[string]json.RawMessage
	if len(raw) == 0 || raw[0] != '{' || json.Unmarshal(raw, &m) != nil {
		return nil, false
	}
	return m, true
}

// objs is a list of objects; ok is false when anything in it isn't one.
func objs(raw json.RawMessage) ([]map[string]json.RawMessage, bool) {
	var items []json.RawMessage
	if len(raw) == 0 || raw[0] != '[' || json.Unmarshal(raw, &items) != nil {
		return nil, false
	}
	out := []map[string]json.RawMessage{}
	ok := true
	for _, item := range items {
		m, isObj := obj(item)
		ok = ok && isObj
		out = append(out, m)
	}
	return out, ok
}

// integer is a JSON integer: digits, not 3000.0 or 3e3 (Ruby reads those
// as floats).
func integer(raw json.RawMessage) (int64, bool) {
	n, err := strconv.ParseInt(string(raw), 10, 64)
	return n, err == nil
}

// domain is a lowercase hostname of at least two labels.
func domain(d string) bool {
	if len(d) > 253 {
		return false
	}
	labels := strings.Split(d, ".")
	if len(labels) < 2 {
		return false
	}
	for _, l := range labels {
		if !labelFormat.MatchString(l) {
			return false
		}
	}
	return true
}

// mountPath is a container path for docker -v: absolute, and no ":" (the
// separator), NUL or line break, at most 4096 characters.
func mountPath(p string) bool {
	return strings.HasPrefix(p, "/") && !strings.ContainsAny(p, ":\x00\n") && utf8.RuneCountInString(p) <= 4096
}

// shortText is text for a page: a string of at most max characters with no
// control characters.
func shortText(raw json.RawMessage, max int) (string, bool) {
	s, ok := str(raw)
	if !ok || utf8.RuneCountInString(s) > max || strings.ContainsFunc(s, unicode.IsControl) {
		return s, false
	}
	return s, true
}
