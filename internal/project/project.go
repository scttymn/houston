// Package project loads a project's compose.yml and its x-houston block, and
// rejects anything Houston can't run on the server.
package project

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/compose-spec/compose-go/v2/loader"
	"github.com/compose-spec/compose-go/v2/types"
	"go.yaml.in/yaml/v3"
)

// Project is a validated compose file. Variables stay as references: nothing
// here is ever interpolated from the environment or a .env file.
type Project struct {
	Name       string
	AppService string
	AppPort    int
	Variables  []Variable // sorted by name
	Houston    Houston
	Compose    *types.Project
}

type VariableKind int

const (
	// Secret is a value the user provides: .env locally, Mission Control on the server.
	Secret VariableKind = iota
	// ServiceHost is <SERVICE>_HOST for a service in the file; Houston fills it on the server.
	ServiceHost
)

type Variable struct {
	Name     string
	Required bool
	Kind     VariableKind
	// BlankDefault: optional, and blank when unset (${V:-}), not a default
	// value (${V:-dev}).
	BlankDefault bool
}

const maxFileSize = 1 << 20

var nameRE = regexp.MustCompile(`^[a-z]([a-z0-9-]{0,61}[a-z0-9])?$`)

// Load reads and validates the compose file at path. It returns a *Project, or
// an *Errors listing every problem found — never both.
func Load(path string) (*Project, error) {
	var ps problems
	data, ok := readFile(path, &ps)
	if !ok {
		return nil, ps.errors(path)
	}
	return Parse(path, data)
}

// Parse validates compose file contents as if they were at path, for
// checking a file before writing it (houston init).
func Parse(path string, data []byte) (*Project, error) {
	var ps problems
	raw, ok := parseRaw(data, &ps)
	if !ok {
		return nil, ps.errors(path)
	}

	name := checkName(raw, &ps)
	model, err := loadCompose(path, data, raw, name)
	if err != nil {
		// compose-go stops at its first error; report it alone rather than
		// piling Houston's checks on top of a file Docker itself rejects.
		// Unless the file uses something Houston refuses anyway (extends,
		// which compose-go is told not to follow): that's what to fix.
		var keys problems
		checkTopLevel(raw, &keys)
		checkServiceKeys(raw, &keys)
		if len(keys) > 0 {
			return nil, keys.errors(path)
		}
		return nil, problems{{Msg: composeMessage(path, err)}}.errors(path)
	}

	checkTopLevel(raw, &ps)
	checkServiceKeys(raw, &ps)
	checkTopLevelVolumes(raw, &ps)
	app := checkApp(model, &ps)
	checkVolumeMounts(model, app, &ps)
	hosts := serviceHosts(model, &ps)
	vars := collectVariables(raw, hosts, &ps)

	h, xPort, hasXPort := parseHouston(raw, filepath.Dir(path), &ps)
	port := appPort(model, app, xPort, hasXPort, &ps)

	if len(ps) > 0 {
		return nil, ps.errors(path)
	}
	return &Project{
		Name:       name,
		AppService: app,
		AppPort:    port,
		Variables:  vars,
		Houston:    h,
		Compose:    model,
	}, nil
}

func readFile(path string, ps *problems) ([]byte, bool) {
	// Not followed: Mission Control reads linked repos' compose files, and a
	// symlink could point it at a file on the server.
	if info, err := os.Lstat(path); err == nil && info.Mode()&fs.ModeSymlink != 0 {
		ps.add("", "is a symlink; Houston reads only a plain file (point -f at the file itself)")
		return nil, false
	}
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		ps.add("", "not found. Run `houston init` to create it")
		return nil, false
	}
	if err != nil {
		ps.add("", "can't read it: %v", err)
		return nil, false
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxFileSize+1))
	if err != nil {
		ps.add("", "can't read it: %v", err)
		return nil, false
	}
	if len(data) > maxFileSize {
		ps.add("", "is larger than 1 MiB")
		return nil, false
	}
	return data, true
}

// parseRaw decodes the file as plain YAML: the shape Houston's own checks walk.
func parseRaw(data []byte, ps *problems) (map[string]any, bool) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			ps.add("", "is empty")
		} else {
			ps.add("", "is not valid YAML: %v", err)
		}
		return nil, false
	}
	var next yaml.Node
	if err := dec.Decode(&next); !errors.Is(err, io.EOF) {
		ps.add("", "must hold one YAML document, not several (remove the `---` separators)")
		return nil, false
	}
	if len(doc.Content) == 0 {
		ps.add("", "is empty")
		return nil, false
	}
	if doc.Content[0].Kind != yaml.MappingNode {
		ps.add("", "must be a mapping of keys (name:, services:, x-houston:) at the top level")
		return nil, false
	}
	var raw map[string]any
	if err := doc.Content[0].Decode(&raw); err != nil {
		ps.add("", "is not valid YAML: %v", err)
		return nil, false
	}
	if len(raw) == 0 {
		ps.add("", "is empty")
		return nil, false
	}
	return raw, true
}

// checkName validates the top-level name and returns it ("" when invalid).
func checkName(raw map[string]any, ps *problems) string {
	v, present := raw["name"]
	if !present {
		ps.add("name", "is required: set a top-level `name:` (the app is served at <name>.<base-domain>)")
		return ""
	}
	s, _ := v.(string)
	if err := CheckName(s); err != nil {
		ps.add("name", "%s", err.Error())
		return ""
	}
	return s
}

// CheckName reports why name can't be a project name, or nil.
func CheckName(name string) error {
	switch {
	case name == "admin" || name == "hooks":
		return fmt.Errorf("`%s` is reserved for Mission Control; pick another name", name)
	case !nameRE.MatchString(name):
		return errors.New("must be lowercase letters, digits and dashes, start with a letter, end with a letter or digit, and be at most 63 characters")
	}
	return nil
}

// loadCompose runs Docker's own loader with interpolation and environment
// resolution off, so the model keeps ${VAR} exactly as written.
func loadCompose(path string, data []byte, raw map[string]any, name string) (*types.Project, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if name == "" {
		name = "unnamed" // the real name problem is already reported
	}
	details := types.ConfigDetails{
		WorkingDir:  filepath.Dir(abs),
		ConfigFiles: []types.ConfigFile{{Filename: abs, Content: data}},
		Environment: types.Mapping{},
	}
	if rewritten, changed := variableBinds(raw); changed {
		details.ConfigFiles[0] = types.ConfigFile{Filename: abs, Config: rewritten}
	}
	return loader.LoadWithContext(context.Background(), details, func(o *loader.Options) {
		o.SkipInterpolation = true
		o.SkipResolveEnvironment = true
		o.SkipResolveLabels = true
		o.SkipInclude = true
		// Houston refuses extends (checkServiceKeys); compose-go mustn't
		// read the file it names first.
		o.SkipExtends = true
		o.ResolvePaths = false
		o.SetProjectName(name, true)
	})
}

// composeMessage drops compose-go's "validating <abs path>: " prefix, since
// Errors already starts every line with the file.
func composeMessage(path string, err error) string {
	msg := err.Error()
	if abs, absErr := filepath.Abs(path); absErr == nil {
		msg = strings.TrimPrefix(msg, "validating "+abs+": ")
	}
	return msg
}

// variableBinds returns a deep copy of raw (compose-go modifies the document
// it's given) with short-syntax service volumes whose source starts with a
// variable (${HOME}/.ssh:/root/.ssh) rewritten into long syntax with type:
// bind. Compose decides bind vs named volume after interpolation; Houston
// loads without interpolating, so it decides here: a variable-led source is a
// host path. The source text is kept as written.
func variableBinds(raw map[string]any) (map[string]any, bool) {
	doc := deepCopy(raw).(map[string]any)
	services, _ := doc["services"].(map[string]any)
	changed := false
	for _, svc := range services {
		fields, _ := svc.(map[string]any)
		volumes, _ := fields["volumes"].([]any)
		for i, v := range volumes {
			s, _ := v.(string)
			if spec, ok := bindSpec(s); ok {
				volumes[i] = spec
				changed = true
			}
		}
	}
	return doc, changed
}

func deepCopy(v any) any {
	switch v := v.(type) {
	case map[string]any:
		m := make(map[string]any, len(v))
		for k, item := range v {
			m[k] = deepCopy(item)
		}
		return m
	case []any:
		list := make([]any, len(v))
		for i, item := range v {
			list[i] = deepCopy(item)
		}
		return list
	default:
		return v
	}
}

// bindSpec turns "${HOME}/.ssh:/root/.ssh:ro" into long syntax. It only
// handles variable-led sources with an optional ro/rw mode.
func bindSpec(s string) (map[string]any, bool) {
	if !strings.HasPrefix(s, "$") || strings.HasPrefix(s, "$$") {
		return nil, false
	}
	parts := splitVolume(s)
	if len(parts) < 2 || len(parts) > 3 {
		return nil, false
	}
	spec := map[string]any{"type": "bind", "source": parts[0], "target": parts[1]}
	if len(parts) == 3 {
		switch parts[2] {
		case "ro":
			spec["read_only"] = true
		case "rw":
		default:
			return nil, false
		}
	}
	return spec, true
}

// splitVolume splits a short-syntax volume on the colons outside ${...}, so
// "${DATA:-./x}:/data" is two parts.
func splitVolume(s string) []string {
	var parts []string
	depth, start := 0, 0
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '$' && i+1 < len(s) && s[i+1] == '{':
			depth++
			i++
		case s[i] == '}' && depth > 0:
			depth--
		case s[i] == ':' && depth == 0:
			parts = append(parts, s[start:i])
			start = i + 1
		}
	}
	return append(parts, s[start:])
}

var allowedTopLevel = map[string]bool{"name": true, "services": true, "volumes": true, "x-houston": true}

func checkTopLevel(raw map[string]any, ps *problems) {
	for _, k := range sortedKeys(raw) {
		switch {
		case allowedTopLevel[k]:
		case strings.HasPrefix(k, "x-"):
			ps.add(k, "Houston can't run other extensions; only `x-houston` is allowed")
		default:
			ps.add(k, "Houston can't run `%s` on the server", k)
		}
	}
}

var allowedServiceKeys = map[string]bool{
	"image": true, "build": true, "environment": true, "volumes": true, "ports": true,
	"depends_on": true, "command": true, "healthcheck": true, "restart": true, "deploy": true,
}

// generationName is a data generation's accessory name: <service>-g<n>.
var generationName = regexp.MustCompile(`-g[0-9]+$`)

func checkServiceKeys(raw map[string]any, ps *problems) {
	services, _ := raw["services"].(map[string]any)
	for _, svc := range sortedKeys(services) {
		keys, _ := services[svc].(map[string]any)
		base := "services." + svc
		if generationName.MatchString(svc) {
			ps.add(base, "names ending in -g<number> are reserved for Houston's data generations (a restore's accessories); rename the service")
		}
		for _, k := range sortedKeys(keys) {
			switch {
			case k == "env_file":
				ps.add(base+".env_file", "write `NAME: ${NAME}` under environment instead, so Houston knows the app needs NAME on the server")
			case k == "deploy":
				checkDeployKeys(base+".deploy", keys[k], ps)
			case k == "build":
				checkBuild(base+".build", keys[k], ps)
			case !allowedServiceKeys[k]:
				ps.add(base+"."+k, "Houston can't run `%s` on the server", k)
			}
		}
	}
}

// allowedBuildKeys: a build reaches only the repo's own files. No
// additional_contexts, ssh, secrets or network (they reach the runner), no
// dockerfile_inline, and every build arg has its value in the file.
var allowedBuildKeys = map[string]bool{"context": true, "dockerfile": true, "target": true, "args": true}

func checkBuild(path string, v any, ps *problems) {
	switch build := v.(type) {
	case string:
		checkInsideRepo(path+".context", build, ps)
	case map[string]any:
		for _, k := range sortedKeys(build) {
			switch {
			case !allowedBuildKeys[k]:
				ps.add(path+"."+k, "Houston can't use `build.%s`: a build reaches only the repo's own files (context, dockerfile, target and args)", k)
			case k == "context" || k == "dockerfile":
				s, _ := build[k].(string)
				checkInsideRepo(path+"."+k, s, ps)
			case k == "args":
				checkBuildArgs(path+".args", build[k], ps)
			}
		}
	}
}

// checkInsideRepo: a relative path that stays in the repo (no .., no ~, no
// URL). Symlinks are checked when the build runs (BuildPaths).
func checkInsideRepo(path, value string, ps *problems) {
	clean := filepath.Clean(value)
	if value == "" || filepath.IsAbs(value) || strings.HasPrefix(value, "~") || strings.Contains(value, "://") || strings.Contains(value, "@") ||
		clean == ".." || strings.HasPrefix(clean, "../") {
		ps.add(path, "must be a path inside the repo (relative, without ..), not %q", value)
	}
}

// checkBuildArgs: NAME=value or NAME: value. A bare NAME is filled in from
// the environment Compose runs in, which on a runner is Houston's.
func checkBuildArgs(path string, v any, ps *problems) {
	bare := func() {
		ps.add(path, "each build arg needs a value in the file (NAME=value): a bare NAME is filled in from the server's environment")
	}
	switch args := v.(type) {
	case []any:
		for _, a := range args {
			if s, _ := a.(string); !strings.Contains(s, "=") {
				bare()
				return
			}
		}
	case map[string]any:
		for _, k := range sortedKeys(args) {
			if args[k] == nil {
				bare()
				return
			}
		}
	}
}

// GeneratedDir is dir/.houston/<sub...>, where Houston writes its generated
// files, made as plain directories. A repo could commit .houston (or a
// directory in it) as a symlink to have Houston write outside the
// checkout, so a symlink or a file there is refused.
func GeneratedDir(dir string, sub ...string) (string, error) {
	path := dir
	for _, part := range append([]string{".houston"}, sub...) {
		path = filepath.Join(path, part)
		info, err := os.Lstat(path)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			if err := os.Mkdir(path, 0o755); err != nil {
				return "", err
			}
		case err != nil:
			return "", err
		case !info.IsDir():
			return "", fmt.Errorf("%s must be a plain directory (Houston writes its generated files there), not a symlink or a file; remove it from the repo", strings.TrimPrefix(path, dir+"/"))
		}
	}
	return path, nil
}

// BuildPaths is the app's build context and Dockerfile, resolved (symlinks
// too) and checked to be inside the checkout at dir. A deploy and houston
// test build nothing else: a repo can't point the build at the runner's files.
func (p *Project) BuildPaths(dir string) (context, dockerfile string, err error) {
	build := p.Compose.Services[p.AppService].Build
	ctx, file := ".", "Dockerfile"
	if build != nil && build.Context != "" {
		ctx = build.Context
	}
	if build != nil && build.Dockerfile != "" {
		file = build.Dockerfile
	}
	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", "", err
	}
	inside := func(what, path string) (string, error) {
		real, err := filepath.EvalSymlinks(path)
		if errors.Is(err, fs.ErrNotExist) && what == "Dockerfile" {
			// A missing Dockerfile fails the build itself; a dangling symlink
			// (it could be made to point anywhere) is refused.
			if _, lerr := os.Lstat(path); lerr != nil {
				return path, nil
			}
		}
		if err != nil {
			return "", fmt.Errorf("the build %s %s isn't in the checkout", what, strings.TrimPrefix(path, dir+"/"))
		}
		if rel, err := filepath.Rel(root, real); err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
			return "", fmt.Errorf("the build %s %s resolves outside the checkout (to %s); nothing was built", what, strings.TrimPrefix(path, dir+"/"), real)
		}
		return real, nil
	}
	if !filepath.IsAbs(ctx) {
		ctx = filepath.Join(dir, ctx)
	}
	if context, err = inside("context", ctx); err != nil {
		return "", "", err
	}
	if !filepath.IsAbs(file) {
		file = filepath.Join(context, file)
	}
	if dockerfile, err = inside("Dockerfile", file); err != nil {
		return "", "", err
	}
	return context, dockerfile, nil
}

// checkDeployKeys allows only deploy.resources.limits.{cpus,memory}.
func checkDeployKeys(path string, v any, ps *problems) {
	deploy, _ := v.(map[string]any)
	for _, k := range sortedKeys(deploy) {
		if k != "resources" {
			ps.add(path+"."+k, "Houston can't run `deploy.%s` on the server; only resources.limits is supported", k)
			continue
		}
		resources, _ := deploy[k].(map[string]any)
		for _, rk := range sortedKeys(resources) {
			if rk != "limits" {
				ps.add(path+".resources."+rk, "Houston can't run `deploy.resources.%s` on the server; only limits is supported", rk)
				continue
			}
			limits, _ := resources[rk].(map[string]any)
			for _, lk := range sortedKeys(limits) {
				if lk != "cpus" && lk != "memory" {
					ps.add(path+".resources.limits."+lk, "Houston can't run this limit on the server; use cpus and memory")
				}
			}
		}
	}
}

func checkTopLevelVolumes(raw map[string]any, ps *problems) {
	volumes, _ := raw["volumes"].(map[string]any)
	for _, name := range sortedKeys(volumes) {
		if settings, _ := volumes[name].(map[string]any); len(settings) > 0 {
			ps.add("volumes."+name, "where a volume lives is chosen in Mission Control; remove its settings here")
		}
	}
}

// checkApp finds the one service with build: and returns its name ("" if none).
func checkApp(model *types.Project, ps *problems) string {
	var built []string
	for _, name := range model.ServiceNames() {
		if model.Services[name].Build != nil {
			built = append(built, name)
		}
	}
	sort.Strings(built)
	switch len(built) {
	case 0:
		ps.add("services", "no service has `build:`; Houston deploys the one service you build")
		return ""
	case 1:
		return built[0]
	default:
		ps.add("services", "only one built service per project for now (found %s); a worker role is planned", strings.Join(built, ", "))
		return ""
	}
}

func checkVolumeMounts(model *types.Project, app string, ps *problems) {
	for _, name := range model.ServiceNames() {
		path := "services." + name + ".volumes"
		for _, v := range model.Services[name].Volumes {
			switch {
			case v.Type == types.VolumeTypeBind && name != app:
				ps.add(path, "bind mount `%s` would disappear on the server; bake the file into an image or use a named volume", v.Source)
			case v.Type == types.VolumeTypeVolume && v.Source == "":
				ps.add(path, "give the volume at `%s` a name (e.g. `data:%s`) so its data survives deploys", v.Target, v.Target)
			case v.Type != types.VolumeTypeBind && v.Type != types.VolumeTypeVolume:
				ps.add(path, "Houston supports named volumes (and bind mounts on the app, for dev), not `%s`", v.Type)
			}
		}
	}
}

// serviceHosts maps <SERVICE>_HOST to its service, reporting collisions.
func serviceHosts(model *types.Project, ps *problems) map[string]string {
	hosts := map[string]string{}
	for _, name := range model.ServiceNames() {
		v := HostVar(name)
		if other, taken := hosts[v]; taken {
			ps.add("services", "services `%s` and `%s` both map to %s; rename one", other, name, v)
			continue
		}
		hosts[v] = name
	}
	return hosts
}

// HostVar is the variable that names service's host: DB_HOST for db, MY_CACHE_HOST for my-cache.
func HostVar(service string) string {
	return strings.ToUpper(strings.ReplaceAll(service, "-", "_")) + "_HOST"
}

func appPort(model *types.Project, app string, xPort int, hasXPort bool, ps *problems) int {
	if hasXPort {
		return xPort
	}
	if app == "" {
		return 0
	}
	ports := model.Services[app].Ports
	if len(ports) != 1 {
		ps.add("services."+app+".ports", "Houston needs the port the app listens on: publish exactly one, or set x-houston.app_port")
		return 0
	}
	return int(ports[0].Target)
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sprintf(format string, args ...any) string {
	if len(args) == 0 {
		return format
	}
	return fmt.Sprintf(format, args...)
}
