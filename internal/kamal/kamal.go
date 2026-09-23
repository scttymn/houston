// Package kamal writes the Kamal configuration for a project on a Houston
// server: the app becomes Kamal service <name>, every other service an
// accessory named <name>-<service>, and named volumes get the project's name
// as a prefix. It's pure: a validated project in, bytes out.
package kamal

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/compose-spec/compose-go/v2/template"
	"github.com/compose-spec/compose-go/v2/types"
	"go.yaml.in/yaml/v3"

	"github.com/sevenmoons/houston/internal/project"
)

// Single-host v1: Kamal runs on the server itself, in a container on the host
// network, and deploys to it over SSH as the houston user.
const (
	host   = "127.0.0.1"
	sshKey = "/ssh/id_ed25519"
	// Not "localhost:5000": for localhost… Kamal starts its own registry on
	// that port and forwards it over SSH, which fights the installer's
	// registry on the same host. Any other server needs a login, which
	// Houston's registry ignores.
	registry         = "127.0.0.1:5000"
	registryUser     = "houston"
	registryPassword = "KAMAL_REGISTRY_PASSWORD"
	// houston deploy passes each secret's value as HOUSTON_S_<NAME> in the
	// Kamal container's environment.
	carrierPrefix = "HOUSTON_S_"
)

// Target is what the generated config needs beyond the compose file.
type Target struct {
	BaseDomain string // <name>.<BaseDomain> is the app's default host
	Arch       string // amd64 or arm64; Kamal requires builder.arch
}

var (
	labelRE      = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	secretNameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	nonNameRE    = regexp.MustCompile(`[^A-Za-z0-9_]`)
	// A value that is exactly one variable, with no default: $V, ${V},
	// ${V:?msg}, ${V?msg}. Such values share the variable's secret.
	exactRE = regexp.MustCompile(`^\$(?:([A-Za-z_][A-Za-z0-9_]*)|\{([A-Za-z_][A-Za-z0-9_]*)(?::?\?[^}]*)?\})$`)
)

type deployYAML struct {
	Service     string               `yaml:"service"`
	Image       string               `yaml:"image"`
	Servers     map[string]role      `yaml:"servers"`
	Proxy       proxy                `yaml:"proxy"`
	Registry    registryConfig       `yaml:"registry"`
	Builder     map[string]string    `yaml:"builder"`
	SSH         sshConfig            `yaml:"ssh"`
	Env         *env                 `yaml:"env,omitempty"`
	Volumes     []string             `yaml:"volumes,omitempty"`
	Accessories map[string]accessory `yaml:"accessories,omitempty"`
}

type role struct {
	Hosts   []string          `yaml:"hosts"`
	Cmd     string            `yaml:"cmd,omitempty"`
	Options map[string]string `yaml:"options,omitempty"`
}

type proxy struct {
	Hosts       []string          `yaml:"hosts"`
	SSL         bool              `yaml:"ssl"`
	AppPort     int               `yaml:"app_port"`
	Healthcheck map[string]string `yaml:"healthcheck"`
	Run         map[string]bool   `yaml:"run"`
}

type registryConfig struct {
	Server   string   `yaml:"server"`
	Username string   `yaml:"username"`
	Password []string `yaml:"password"`
}

type sshConfig struct {
	User     string   `yaml:"user"`
	KeysOnly bool     `yaml:"keys_only"`
	Keys     []string `yaml:"keys"`
}

type env struct {
	Clear  map[string]string `yaml:"clear,omitempty"`
	Secret []string          `yaml:"secret,omitempty"`
}

type accessory struct {
	Image   string            `yaml:"image"`
	Host    string            `yaml:"host"`
	Cmd     string            `yaml:"cmd,omitempty"`
	Env     *env              `yaml:"env,omitempty"`
	Volumes []string          `yaml:"volumes,omitempty"`
	Options map[string]string `yaml:"options,omitempty"`
}

// Config returns deploy.yml for p. Every string taken from the compose file is
// ERB-escaped, because Kamal renders deploy.yml as ERB.
func Config(p *project.Project, t Target) ([]byte, error) {
	var ps problems
	checkTarget(t, &ps)
	g := newGenerator(p, &ps)
	cfg := g.config(t)
	if len(ps) > 0 {
		return nil, ps.errors(p)
	}
	out, err := yaml.Marshal(escapeERB(cfg))
	if err != nil {
		return nil, err
	}
	return out, nil
}

// SecretsFile returns .kamal/secrets for p: names only. Each value comes from
// HOUSTON_S_<NAME> through printenv, whose output Kamal never substitutes
// again (with NAME=$NAME, a value containing $(…) would run in the Kamal
// container).
func SecretsFile(p *project.Project) ([]byte, error) {
	var ps problems
	g := newGenerator(p, &ps)
	g.config(Target{})
	if len(ps) > 0 {
		return nil, ps.errors(p)
	}
	names := []string{registryPassword}
	for name := range g.secretNames {
		names = append(names, name)
	}
	sort.Strings(names)
	var b strings.Builder
	b.WriteString("# Written by houston deploy. Values come from Mission Control through the\n")
	b.WriteString("# environment; printenv's output is never substituted again.\n")
	for _, name := range names {
		fmt.Fprintf(&b, "%s=$(printenv %s%s)\n", name, carrierPrefix, name)
	}
	return []byte(b.String()), nil
}

func checkTarget(t Target, ps *problems) {
	if !validDomain(t.BaseDomain) {
		ps.add("", "the base domain %q isn't a lowercase hostname", t.BaseDomain)
	}
	if t.Arch != "amd64" && t.Arch != "arm64" {
		ps.add("", "the server's architecture %q isn't amd64 or arm64", t.Arch)
	}
}

func validDomain(s string) bool {
	labels := strings.Split(s, ".")
	if len(labels) < 2 || len(s) > 253 {
		return false
	}
	for _, l := range labels {
		if !labelRE.MatchString(l) {
			return false
		}
	}
	return true
}

type generator struct {
	p     *project.Project
	ps    *problems
	kinds map[string]project.VariableKind
	hosts map[string]string // DB_HOST → shop-db
	// secretNames maps each Kamal secret name to where it's defined, so two
	// places can't claim one name with different values.
	secretNames map[string]secretUse
}

type secretUse struct {
	path   string
	shared bool // the name is a compose variable, shared by every exact use
}

func newGenerator(p *project.Project, ps *problems) *generator {
	g := &generator{p: p, ps: ps, kinds: map[string]project.VariableKind{}, hosts: map[string]string{}, secretNames: map[string]secretUse{}}
	for _, v := range p.Variables {
		g.kinds[v.Name] = v.Kind
	}
	for name := range p.Compose.Services {
		g.hosts[project.HostVar(name)] = p.Name + "-" + name
	}
	return g
}

func (g *generator) config(t Target) deployYAML {
	p := g.p
	cfg := deployYAML{
		Service: p.Name,
		Image:   p.Name,
		Proxy: proxy{
			Hosts:       proxyHosts(p, t.BaseDomain),
			AppPort:     p.AppPort,
			Healthcheck: map[string]string{"path": p.Houston.Health},
			Run:         map[string]bool{"publish": false},
		},
		Registry: registryConfig{Server: registry, Username: registryUser, Password: []string{registryPassword}},
		Builder:  map[string]string{"arch": t.Arch},
		SSH:      sshConfig{User: "houston", KeysOnly: true, Keys: []string{sshKey}},
	}

	for _, name := range sortedServices(p) {
		svc := p.Compose.Services[name]
		path := "services." + name
		e := g.env(name, svc.Environment)
		volumes := g.volumes(svc)
		cmd := g.command(path+".command", svc.Command)
		if name == p.AppService {
			cfg.Servers = map[string]role{"web": {Hosts: []string{host}, Cmd: cmd, Options: limits(svc)}}
			cfg.Env = e
			cfg.Volumes = volumes
			continue
		}
		if cfg.Accessories == nil {
			cfg.Accessories = map[string]accessory{}
		}
		cfg.Accessories[name] = accessory{
			Image:   g.plain(path+".image", svc.Image, true),
			Host:    host,
			Cmd:     cmd,
			Env:     e,
			Volumes: volumes,
			Options: g.healthOptions(path+".healthcheck", svc.HealthCheck),
		}
	}
	return cfg
}

func sortedServices(p *project.Project) []string {
	var names []string
	for name := range p.Compose.Services {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func proxyHosts(p *project.Project, base string) []string {
	hosts := []string{p.Name + "." + base}
	seen := map[string]bool{hosts[0]: true}
	for _, d := range p.Houston.Domains {
		if !seen[d] {
			seen[d] = true
			hosts = append(hosts, d)
		}
	}
	return hosts
}

// env splits a service's environment into clear values (literals, and values
// that only name services) and secrets (anything referencing a user's
// variable). A value that is exactly one variable shares that variable's
// secret; any other value gets its own, <SERVICE>__<KEY>.
func (g *generator) env(service string, environment types.MappingWithEquals) *env {
	e := &env{Clear: map[string]string{}}
	keys := make([]string, 0, len(environment))
	for k := range environment {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		path := "services." + service + ".environment." + key
		value := environment[key]
		if value == nil {
			g.ps.add(path, "give it a value (%s: ${%s}); on the server there's no shell environment to pass it through", key, key)
			continue
		}
		if !g.referencesSecret(*value) {
			resolved := g.resolveHosts(path, *value)
			if why := uncarriable(resolved); why != "" {
				g.ps.add(path, "%s", why)
				continue
			}
			e.Clear[key] = resolved
			continue
		}
		if !secretNameRE.MatchString(key) {
			g.ps.add(path, "a variable holding a secret needs a name made of letters, digits and _")
			continue
		}
		name, shared := g.secretName(service, key, *value)
		if other, taken := g.secretNames[name]; taken && !(shared && other.shared) {
			g.ps.add(path, "its secret would be named %s, like %s; rename one", name, other.path)
			if !other.shared {
				g.ps.add(other.path, "its secret would be named %s, like %s; rename one", name, path)
			}
			continue
		}
		if name == registryPassword {
			g.ps.add(path, "%s is reserved for Houston's registry login", name)
			continue
		}
		g.secretNames[name] = secretUse{path: path, shared: shared}
		if name == key {
			e.Secret = append(e.Secret, key)
		} else {
			e.Secret = append(e.Secret, key+":"+name)
		}
	}
	if len(e.Clear) == 0 {
		e.Clear = nil
	}
	if e.Clear == nil && e.Secret == nil {
		return nil
	}
	return e
}

func (g *generator) secretName(service, key, value string) (name string, shared bool) {
	if m := exactRE.FindStringSubmatch(value); m != nil {
		return m[1] + m[2], true
	}
	return strings.ToUpper(nonNameRE.ReplaceAllString(service, "_")) + "__" + strings.ToUpper(key), false
}

// referencesSecret reports whether s names any variable that isn't a service
// host (after $$ escapes, which aren't references).
func (g *generator) referencesSecret(s string) bool {
	for name := range template.ExtractVariables(map[string]any{"v": s}, template.DefaultPattern) {
		if kind, known := g.kinds[name]; !known || kind == project.Secret {
			return true
		}
	}
	return false
}

// resolveHosts substitutes service hosts and unescapes $$, as compose would
// on the server.
func (g *generator) resolveHosts(path, s string) string {
	out, err := template.Substitute(s, func(name string) (string, bool) {
		v, ok := g.hosts[name]
		return v, ok
	})
	if err != nil {
		g.ps.add(path, "%v", err)
	}
	return out
}

// plain resolves a string that Kamal puts on a command line (image, docker
// options). A secret can't go there, and Kamal leaves ${…} in option values
// unescaped for the host's shell.
func (g *generator) plain(path, s string, option bool) string {
	if g.referencesSecret(s) {
		g.ps.add(path, "a secret can only reach the server as environment: put it in environment and refer to it as $$NAME")
		return ""
	}
	out := g.resolveHosts(path, s)
	if option && strings.Contains(out, "${") {
		g.ps.add(path, "write $$NAME instead of $${NAME}: Kamal would let the server's shell expand ${NAME} before the container sees it")
		return ""
	}
	return out
}

// command turns compose's exec-form command into Kamal's cmd string, which
// runs through the host's shell: every argument is single-quoted unless it's
// plainly safe, so the container gets exactly compose's argv.
func (g *generator) command(path string, args types.ShellCommand) string {
	if len(args) == 0 {
		return ""
	}
	quoted := make([]string, len(args))
	for i, a := range args {
		if g.referencesSecret(a) {
			g.ps.add(path, "a secret can only reach the server as environment: put it in environment and refer to it as $$NAME")
			return ""
		}
		quoted[i] = shellQuote(g.resolveHosts(path, a))
	}
	return strings.Join(quoted, " ")
}

var safeArgRE = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

func shellQuote(s string) string {
	if safeArgRE.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// healthOptions turns an accessory's compose healthcheck into docker run
// options. The app's healthcheck is kamal-proxy's (x-houston.health).
func (g *generator) healthOptions(path string, h *types.HealthCheckConfig) map[string]string {
	if h == nil || h.Disable || len(h.Test) == 0 || h.Test[0] == "NONE" {
		return nil
	}
	var cmd string
	switch h.Test[0] {
	case "CMD-SHELL":
		cmd = strings.Join(h.Test[1:], " ")
	case "CMD":
		quoted := make([]string, len(h.Test)-1)
		for i, a := range h.Test[1:] {
			quoted[i] = shellQuote(a)
		}
		cmd = strings.Join(quoted, " ")
	default:
		cmd = strings.Join(h.Test, " ")
	}
	cmd = g.plain(path+".test", cmd, true)
	if cmd == "" {
		return nil
	}
	opts := map[string]string{"health-cmd": cmd}
	duration := func(key string, d *types.Duration) {
		if d != nil {
			opts[key] = d.String()
		}
	}
	duration("health-interval", h.Interval)
	duration("health-timeout", h.Timeout)
	duration("health-start-period", h.StartPeriod)
	duration("health-start-interval", h.StartInterval)
	if h.Retries != nil {
		opts["health-retries"] = strconv.FormatUint(*h.Retries, 10)
	}
	return opts
}

// volumes maps a service's named volumes to <project>_<volume>. Bind mounts
// are dev-only; the loader has already rejected anonymous volumes and binds
// on other services.
func (g *generator) volumes(svc types.ServiceConfig) []string {
	var out []string
	for _, v := range svc.Volumes {
		if v.Type != types.VolumeTypeVolume {
			continue
		}
		spec := g.p.Name + "_" + v.Source + ":" + v.Target
		if v.ReadOnly {
			spec += ":ro"
		}
		out = append(out, spec)
	}
	return out
}

func limits(svc types.ServiceConfig) map[string]string {
	if svc.Deploy == nil || svc.Deploy.Resources.Limits == nil {
		return nil
	}
	l := svc.Deploy.Resources.Limits
	opts := map[string]string{}
	if l.NanoCPUs > 0 {
		opts["cpus"] = strconv.FormatFloat(float64(l.NanoCPUs), 'f', -1, 32)
	}
	if l.MemoryBytes > 0 {
		opts["memory"] = memory(int64(l.MemoryBytes))
	}
	if len(opts) == 0 {
		return nil
	}
	return opts
}

func memory(b int64) string {
	for _, u := range []struct {
		size   int64
		suffix string
	}{{1 << 30, "g"}, {1 << 20, "m"}, {1 << 10, "k"}} {
		if b%u.size == 0 {
			return strconv.FormatInt(b/u.size, 10) + u.suffix
		}
	}
	return strconv.FormatInt(b, 10)
}

// escapeERB returns cfg as plain YAML values with every <% written <%%, so
// Kamal's ERB pass gives back the literal text.
func escapeERB(cfg deployYAML) any {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		panic(err) // plain structs of strings always marshal
	}
	var node yaml.Node
	if err := yaml.Unmarshal(data, &node); err != nil {
		panic(err)
	}
	var walk func(n *yaml.Node)
	walk = func(n *yaml.Node) {
		if n.Kind == yaml.ScalarNode {
			n.Value = strings.ReplaceAll(n.Value, "<%", "<%%")
		}
		for _, c := range n.Content {
			walk(c)
		}
	}
	walk(&node)
	return &node
}

type problems []project.Problem

func (ps *problems) add(path, format string, args ...any) {
	*ps = append(*ps, project.Problem{Path: path, Msg: fmt.Sprintf(format, args...)})
}

func (ps problems) errors(p *project.Project) error {
	sort.SliceStable(ps, func(i, j int) bool { return ps[i].Path < ps[j].Path })
	file := "compose.yml"
	if len(p.Compose.ComposeFiles) > 0 {
		file = filepath.Base(p.Compose.ComposeFiles[0])
	}
	return &project.Errors{File: file, Problems: ps}
}

// CarrierValue is a secret's value as houston deploy passes it to the Kamal
// container (HOUSTON_S_<NAME>). Kamal's dotenv runs the secrets file's
// printenv, then substitutes variables in its output: a backslash before
// every $ that isn't $( keeps the value literal. Values Kamal can't carry
// (see uncarriable) are an error.
func CarrierValue(v string) (string, error) {
	if why := uncarriable(v); why != "" {
		return "", fmt.Errorf("%s", why)
	}
	var b strings.Builder
	for i := 0; i < len(v); i++ {
		if v[i] == '$' && (i+1 == len(v) || v[i+1] != '(') {
			b.WriteByte('\\')
		}
		b.WriteByte(v[i])
	}
	return b.String(), nil
}

// uncarriable says why Kamal can't deliver v unchanged, or "". Kamal writes
// environment into docker env files, which docker reads literally, after
// escaping each value with Ruby's String#dump: a backslash would arrive
// doubled, and a line break or tab as the two characters \n or \t.
func uncarriable(v string) string {
	for _, r := range v {
		switch {
		case r == '\\':
			return "Kamal can't pass a backslash through to the container unchanged"
		case r < 0x20 || r == 0x7f:
			return "Kamal can't pass a line break, tab or other control character through to the container unchanged (base64-encode the value instead)"
		}
	}
	return ""
}
