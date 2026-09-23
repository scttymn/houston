package project

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Houston is the parsed x-houston block, with defaults applied.
type Houston struct {
	Health   string
	Domains  []string
	Deploy   Deploy
	Commands Commands
	Hooks    Hooks
	Backups  Backups
	// MaintenancePage is the project's own maintenance page (the file
	// x-houston.maintenance names), or "" for Houston's default.
	MaintenancePage string
}

type Deploy struct {
	On     string // "commit" or "tag"
	Branch string
	Tags   string
}

type Commands struct {
	Console *Console
	Test    string
}

// Console is the console command per place; a plain string sets both.
type Console struct {
	Dev    string
	Server string
}

type Hooks struct {
	Release    string
	PostDeploy string
}

type Backups struct {
	Schedule   string
	KeepAuto   int
	KeepDeploy int
}

var (
	scheduleRE = regexp.MustCompile(`^daily ([01][0-9]|2[0-3]):[0-5][0-9]$`)
	labelRE    = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
)

// parseHouston walks x-houston strictly. It also returns x-houston.port, which
// isn't kept on Houston: Project.AppPort is the resolved port.
// dir is compose.yml's directory: x-houston.maintenance is relative to it.
func parseHouston(raw map[string]any, dir string, ps *problems) (h Houston, port int, hasPort bool) {
	h = Houston{
		Deploy:  Deploy{On: "commit", Branch: "main", Tags: "v*"},
		Backups: Backups{Schedule: "daily 03:00", KeepAuto: 14, KeepDeploy: 10},
	}
	v, present := raw["x-houston"]
	if !present {
		ps.add("x-houston", "is missing; add an x-houston block with at least `health: /up` (or run `houston init`)")
		return h, 0, false
	}
	x, ok := v.(map[string]any)
	if !ok {
		ps.add("x-houston", "must be a mapping of keys (health:, domains:, …)")
		return h, 0, false
	}

	if _, ok := x["health"]; !ok {
		ps.add("x-houston.health", "is required: the path that returns 200 when the app is ready (e.g. /up)")
	}
	for _, k := range sortedKeys(x) {
		path := "x-houston." + k
		switch k {
		case "health":
			h.Health = parseHealth(path, x[k], ps)
		case "port":
			port, hasPort = parsePort(path, x[k], ps)
		case "domains":
			h.Domains = parseDomains(path, x[k], ps)
		case "deploy":
			parseDeploy(path, x[k], &h.Deploy, ps)
		case "commands":
			parseCommands(path, x[k], &h.Commands, ps)
		case "hooks":
			parseHooks(path, x[k], &h.Hooks, ps)
		case "backups":
			parseBackups(path, x[k], &h.Backups, ps)
		case "maintenance":
			h.MaintenancePage = parseMaintenancePage(path, x[k], dir, ps)
		default:
			ps.add(path, "unknown key; x-houston takes health, port, domains, deploy, commands, hooks, backups, maintenance")
		}
	}
	return h, port, hasPort
}

func parseHealth(path string, v any, ps *problems) string {
	s, ok := v.(string)
	switch {
	case !ok:
		ps.add(path, "must be a path like /up")
	case s == "":
		ps.add(path, "is required: the path that returns 200 when the app is ready (e.g. /up)")
	case !strings.HasPrefix(s, "/"):
		ps.add(path, "must start with / (e.g. /up)")
	case strings.IndexFunc(s, unicode.IsSpace) >= 0:
		ps.add(path, "must not contain whitespace")
	case len(s) > 256:
		ps.add(path, "must be at most 256 characters")
	default:
		return s
	}
	return ""
}

func parsePort(path string, v any, ps *problems) (int, bool) {
	n, ok := v.(int)
	if !ok || n < 1 || n > 65535 {
		ps.add(path, "must be a number from 1 to 65535")
		return 0, false
	}
	return n, true
}

func parseDomains(path string, v any, ps *problems) []string {
	list, ok := v.([]any)
	if !ok {
		ps.add(path, "must be a list of hostnames")
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for i, item := range list {
		p := path + "[" + itoa(i) + "]"
		s, _ := item.(string)
		switch {
		case !validHostname(s):
			ps.add(p, "`%v` isn't a hostname; use lowercase like `example.com`, with no scheme, port, path or wildcard", item)
		case seen[s]:
			ps.add(p, "duplicate domain `%s`", s)
		default:
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func validHostname(s string) bool {
	if len(s) > 253 {
		return false
	}
	labels := strings.Split(s, ".")
	if len(labels) < 2 {
		return false
	}
	for _, l := range labels {
		if !labelRE.MatchString(l) {
			return false
		}
	}
	return true
}

func parseDeploy(path string, v any, d *Deploy, ps *problems) {
	m, ok := v.(map[string]any)
	if !ok {
		ps.add(path, "must be a mapping (on:, branch:, tags:)")
		return
	}
	for _, k := range sortedKeys(m) {
		p := path + "." + k
		s, isString := m[k].(string)
		switch k {
		case "on":
			if s != "commit" && s != "tag" {
				ps.add(p, "must be `commit` or `tag`")
				continue
			}
			d.On = s
		case "branch":
			if !isString || s == "" || strings.IndexFunc(s, unicode.IsSpace) >= 0 || strings.Contains(s, "..") || strings.HasPrefix(s, "-") {
				ps.add(p, "must be a branch name (no spaces, no `..`, not starting with -)")
				continue
			}
			d.Branch = s
		case "tags":
			if !isString || s == "" {
				ps.add(p, "must be a tag pattern like `v*`")
				continue
			}
			d.Tags = s
		default:
			ps.add(p, "unknown key; deploy takes on, branch, tags")
		}
	}
}

func parseCommands(path string, v any, c *Commands, ps *problems) {
	m, ok := v.(map[string]any)
	if !ok {
		ps.add(path, "must be a mapping (console:, test:)")
		return
	}
	for _, k := range sortedKeys(m) {
		p := path + "." + k
		switch k {
		case "console":
			c.Console = parseConsole(p, m[k], ps)
		case "test":
			s, isString := m[k].(string)
			switch {
			case !isString:
				ps.add(p, "must be a string: tests run the same way everywhere")
			case s == "":
				ps.add(p, "must not be empty")
			default:
				c.Test = shellCommand(p, s, ps)
			}
		default:
			ps.add(p, "unknown command; Houston runs `console` and `test` (tasks for every deploy go in hooks.release)")
		}
	}
}

func parseConsole(path string, v any, ps *problems) *Console {
	switch v := v.(type) {
	case string:
		if v == "" {
			ps.add(path, "must not be empty")
			return nil
		}
		cmd := shellCommand(path, v, ps)
		return &Console{Dev: cmd, Server: cmd}
	case map[string]any:
		c := &Console{}
		for _, k := range sortedKeys(v) {
			s, _ := v[k].(string)
			switch k {
			case "dev":
				c.Dev = shellCommand(path+".dev", s, ps)
			case "server":
				c.Server = shellCommand(path+".server", s, ps)
			default:
				ps.add(path+"."+k, "unknown key; console takes dev and server")
			}
		}
		if c.Dev == "" || c.Server == "" {
			ps.add(path, "needs both `dev` and `server` commands (or one string for both)")
			return nil
		}
		return c
	default:
		ps.add(path, "must be a command string, or dev: and server: commands")
		return nil
	}
}

func parseHooks(path string, v any, h *Hooks, ps *problems) {
	m, ok := v.(map[string]any)
	if !ok {
		ps.add(path, "must be a mapping (release:, post_deploy:)")
		return
	}
	for _, k := range sortedKeys(m) {
		p := path + "." + k
		s, isString := m[k].(string)
		if k != "release" && k != "post_deploy" {
			ps.add(p, "unknown hook; Houston runs `release` and `post_deploy`")
			continue
		}
		if !isString {
			ps.add(p, "must be a command string")
			continue
		}
		if s == "" {
			ps.add(p, "must not be empty")
			continue
		}
		if k == "release" {
			h.Release = shellCommand(p, s, ps)
		} else {
			h.PostDeploy = shellCommand(p, s, ps)
		}
	}
}

func parseBackups(path string, v any, b *Backups, ps *problems) {
	m, ok := v.(map[string]any)
	if !ok {
		ps.add(path, "must be a mapping (schedule:, keep:)")
		return
	}
	for _, k := range sortedKeys(m) {
		p := path + "." + k
		switch k {
		case "schedule":
			s, _ := m[k].(string)
			if !scheduleRE.MatchString(s) {
				ps.add(p, "must look like `daily HH:MM` (24-hour)")
				continue
			}
			b.Schedule = s
		case "keep":
			parseKeep(p, m[k], b, ps)
		case "storage":
			ps.add(p, "pick the backup target on the project page in Mission Control, not in the repo")
		default:
			ps.add(p, "unknown key; backups takes schedule and keep")
		}
	}
}

func parseKeep(path string, v any, b *Backups, ps *problems) {
	m, ok := v.(map[string]any)
	if !ok {
		ps.add(path, "must be a mapping (auto:, deploy:)")
		return
	}
	for _, k := range sortedKeys(m) {
		p := path + "." + k
		n, isInt := m[k].(int)
		if k != "auto" && k != "deploy" {
			ps.add(p, "unknown key; keep takes auto and deploy")
			continue
		}
		if !isInt || n < 1 || n > 1000 {
			ps.add(p, "must be a whole number from 1 to 1000")
			continue
		}
		if k == "auto" {
			b.KeepAuto = n
		} else {
			b.KeepDeploy = n
		}
	}
}

// shellCommand returns an x-houston command as the shell will run it. Compose
// interpolates this file too, x-houston included, so a bare $NAME would be
// replaced by compose (blank, with a warning) before anyone saw it. Commands
// follow compose's own rule: $$ is a literal $, and a bare $NAME is an error.
func shellCommand(path, s string, ps *problems) string {
	var uses []use
	scan(s, path, &uses)
	for _, u := range uses {
		ps.add(path, "write `$$%s`: compose reads this file too and would replace `$%s` with a value from your shell or .env", u.name, u.name)
	}
	return strings.ReplaceAll(s, "$$", "$")
}

// MaxMaintenancePage is the most a project's maintenance page may weigh.
const MaxMaintenancePage = 512 * 1024

// parseMaintenancePage reads the maintenance page x-houston.maintenance names:
// a file inside compose.yml's directory, at most 512 KB of UTF-8.
func parseMaintenancePage(path string, v any, dir string, ps *problems) string {
	rel, ok := v.(string)
	if !ok || rel == "" {
		ps.add(path, "must be a path to an HTML file, like public/maintenance.html")
		return ""
	}
	if filepath.IsAbs(rel) {
		ps.add(path, "must be relative to compose.yml's directory, not %s", rel)
		return ""
	}
	clean := filepath.Clean(rel)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		ps.add(path, "must be inside the repo (next to compose.yml or below), not %s", rel)
		return ""
	}
	full := filepath.Join(dir, clean)
	// Symlinks are followed, but the file they reach must be inside the repo
	// too: on a runner, the checkout sits next to its deploy keys, and the
	// page is served to anyone.
	if real, err := filepath.EvalSymlinks(full); err == nil {
		root, rootErr := filepath.EvalSymlinks(dir)
		if within, relErr := filepath.Rel(root, real); rootErr != nil || relErr != nil ||
			within == ".." || strings.HasPrefix(within, ".."+string(filepath.Separator)) {
			ps.add(path, "must be inside the repo; %s leads outside it", rel)
			return ""
		}
		full = real
	}
	info, err := os.Stat(full)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		ps.add(path, "%s doesn't exist", rel)
		return ""
	case err != nil:
		ps.add(path, "can't read %s: %v", rel, err)
		return ""
	case !info.Mode().IsRegular():
		ps.add(path, "%s must be a file", rel)
		return ""
	case info.Size() > MaxMaintenancePage:
		ps.add(path, "%s is larger than 512 KB", rel)
		return ""
	}
	data, err := os.ReadFile(full)
	if err != nil {
		ps.add(path, "can't read %s: %v", rel, err)
		return ""
	}
	if !utf8.Valid(data) {
		ps.add(path, "%s must be UTF-8 text", rel)
		return ""
	}
	return string(data)
}
