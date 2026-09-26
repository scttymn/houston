package cli

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/compose-spec/compose-go/v2/dotenv"
	"github.com/scttymn/houston/internal/docker"
	"github.com/scttymn/houston/internal/project"
	"github.com/scttymn/houston/internal/variant"
)

// runDev implements `houston dev` (docs/plans/dev-localhost.md): compose.yml
// plus a generated override (the dev build target, or with --production the
// production one), run with `docker compose up --build`, and served by name
// at <name>.localhost, or <branch>.<name>.localhost, through
// houston-dev-proxy, with no host ports (--ports keeps compose.yml's).
func runDev(file string, o devOptions, stderr io.Writer, d docker.Runner) int {
	abs, err := filepath.Abs(file)
	if err != nil {
		fmt.Fprintf(stderr, "houston: %v\n", err)
		return exitUsage
	}
	p, ok := loadProject(file, stderr)
	if !ok {
		return exitUsage
	}
	hostPort, err := devProxyPort()
	if err != nil {
		fmt.Fprintf(stderr, "houston: %v\n", err)
		return exitUsage
	}
	dir := filepath.Dir(abs)
	root, own, common := gitDirs(dir)
	// --as names this instance, and this checkout remembers it; --as= forgets
	// it. Otherwise the remembered name, else the branch.
	as := o.as
	if !o.asSet {
		as = savedDevName(own)
	}
	instance, err := devInstance(dir, p, as)
	if err != nil {
		fmt.Fprintf(stderr, "houston: %v\n", err)
		return exitUsage
	}
	if o.fresh && instance == "" {
		fmt.Fprintln(stderr, "houston: --fresh gives a branch a new copy of main's data; this is the main instance (use it on a branch, or with --as)")
		return exitUsage
	}
	if !preflight(d, stderr) {
		return exitFailure
	}
	envFile := filepath.Join(dir, ".env")
	fromMain := ""
	if _, err := os.Stat(envFile); errors.Is(err, fs.ErrNotExist) {
		if fromMain = mainCheckoutEnv(dir, root, own, common); fromMain != "" {
			envFile = fromMain
			fmt.Fprintf(stderr, "houston: no .env here; using the main checkout's (%s)\n", fromMain)
		}
	}
	values, ok := warnAboutVariables(envFile, p, stderr)
	if !ok {
		return exitUsage
	}
	warnAboutOverrideFiles(dir, stderr)

	n := devNaming(p.Name, instance, o.production)
	if name := dnsLabel(o.as); o.asSet && own != "" && savedDevName(own) != name {
		if err := saveDevName(own, name); err != nil {
			fmt.Fprintf(stderr, "warning: couldn't remember --as: %v\n", err)
		} else if name == "" {
			fmt.Fprintf(stderr, "houston: this checkout goes back to its branch's name: %s\n", n.Host)
		} else {
			fmt.Fprintf(stderr, "houston: this checkout is %s from now on (houston dev --as= goes back to its branch's name)\n", n.Host)
		}
	}
	if other := nameInUse(d, n.Project, dir); other != "" {
		fmt.Fprintf(stderr, "houston: %s is already running from %s; give this one another name with --as <name>\n", n.Host, other)
		return exitUsage
	}
	if err := ensureDevProxy(d, hostPort); err != nil {
		fmt.Fprintf(stderr, "houston: %v\n", err)
		return exitFailure
	}
	if instance != "" {
		if common != "" {
			// houston dev prune removes it once no checkout runs it.
			if err := recordDevInstance(common, devRecord{Project: n.Project, App: p.Name, Host: n.Host, Dir: dir}); err != nil {
				fmt.Fprintf(stderr, "warning: couldn't record %s for houston dev prune: %v\n", n.Host, err)
			}
		}
		if err := copyMainData(d, p, n, o.fresh, stderr); err != nil {
			fmt.Fprintf(stderr, "houston: %v\n", err)
			if errors.As(err, new(usageError)) {
				return exitUsage
			}
			return exitFailure
		}
	}

	route := variant.Route{Alias: n.Alias, KeepPorts: o.keepPorts}
	name, content, port := "compose.dev.yml", variant.DevOverride(p, route), p.DevPort
	if o.production {
		name, content, port = "compose.production.yml", variant.ProductionOverride(p, route), p.AppPort
		// Its own project, so its own volumes: dev's were written by the dev
		// stage (often as root), which a production image running as a user
		// can't write. Fresh volumes are seeded from the image, as on the server.
		noteBlankOptional(p, values, stderr)
	}
	override, err := writeGenerated(dir, name, content)
	if err != nil {
		fmt.Fprintf(stderr, "houston: can't write .houston/%s: %v\n", name, err)
		return exitFailure
	}
	stop := routeWhenUp(d, p.Name, n, port, p.Houston.Health, hostPort, stderr)
	// -p pins the project name: compose would otherwise let a stray
	// COMPOSE_PROJECT_NAME (shell or .env) rename the containers.
	args := []string{"compose", "-p", n.Project}
	if fromMain != "" {
		args = append(args, "--env-file", fromMain)
	}
	args = append(args, "--project-directory", dir, "-f", abs, "-f", override, "up", "--build")
	code, err := d.Run(dir, nil, args...)
	stop()
	if err != nil {
		fmt.Fprintf(stderr, "houston: %v\n", err)
		return exitFailure
	}
	return code
}

// devOptions are houston dev's flags.
type devOptions struct {
	production, keepPorts, fresh bool
	as                           string
	asSet                        bool // --as was given, even empty (--as= forgets the remembered name)
}

func preflight(d docker.Runner, stderr io.Writer) bool {
	switch {
	case d.LookPath() != nil:
		fmt.Fprintln(stderr, "houston: Docker isn't installed (or isn't on PATH). Install OrbStack or Docker Desktop.")
	case failed(d.Output("version", "--format", "{{.Server.Version}}")):
		fmt.Fprintln(stderr, "houston: Docker isn't running (start OrbStack or Docker Desktop).")
	case failed(d.Output("compose", "version", "--short")):
		fmt.Fprintln(stderr, "houston: Houston needs Docker Compose v2 or later (`docker compose version` failed).")
	default:
		return true
	}
	return false
}

func failed(_ []byte, err error) bool { return err != nil }

// mainCheckoutEnv is the main checkout's .env, at the same place as dir in
// this linked worktree ("" when this isn't one, or main has none): git leaves
// untracked files like .env out of a new worktree.
func mainCheckoutEnv(dir, root, own, common string) string {
	if own == "" || filepath.Base(common) != ".git" {
		return ""
	}
	rel, err := filepath.Rel(root, dir)
	if err != nil {
		return ""
	}
	path := filepath.Join(filepath.Dir(common), rel, ".env")
	if _, err := os.Stat(path); err != nil {
		return ""
	}
	return path
}

// warnAboutVariables warns, by name only, about required secrets that will be
// blank because neither envPath (the .env Compose reads) nor the shell sets
// them. It parses it with compose-go's parser so both agree. It returns its
// values, and false when it can't be parsed.
func warnAboutVariables(envPath string, p *project.Project, stderr io.Writer) (map[string]string, bool) {
	values := map[string]string{}
	_, statErr := os.Stat(envPath)
	missingFile := errors.Is(statErr, fs.ErrNotExist)
	if !missingFile {
		var err error
		values, err = dotenv.ReadFile(envPath, func(string) (string, bool) { return "", false })
		if err != nil {
			fmt.Fprintf(stderr, "houston: .env: %v\n", err)
			return nil, false
		}
	}

	var blank []string
	for _, v := range p.Variables {
		if !v.Required || v.Kind != project.Secret {
			continue
		}
		if values[v.Name] != "" {
			continue
		}
		if shell, ok := os.LookupEnv(v.Name); ok && shell != "" {
			continue // compose prefers the shell's value
		}
		blank = append(blank, v.Name)
	}
	switch {
	case len(blank) == 0:
	case missingFile:
		fmt.Fprintf(stderr, "warning: no .env next to the compose file; these required variables will be blank: %s\n", strings.Join(blank, ", "))
	default:
		fmt.Fprintf(stderr, "warning: .env has no value for: %s (the app gets them blank)\n", strings.Join(blank, ", "))
	}
	return values, true
}

// noteBlankOptional names the optional variables that come out blank in
// dev --production: in dev an app may fall back without them (a key file its
// .dockerignore keeps out of the image, say), but the production image may
// need them.
func noteBlankOptional(p *project.Project, values map[string]string, stderr io.Writer) {
	var blank []string
	for _, v := range p.Variables {
		if v.Kind != project.Secret || !v.BlankDefault || values[v.Name] != "" {
			continue
		}
		if shell, ok := os.LookupEnv(v.Name); ok && shell != "" {
			continue
		}
		blank = append(blank, v.Name)
	}
	switch len(blank) {
	case 0:
	case 1:
		fmt.Fprintf(stderr, "note: %s is blank in .env; the production image may need it\n", blank[0])
	default:
		fmt.Fprintf(stderr, "note: %s are blank in .env; the production image may need them\n", strings.Join(blank, ", "))
	}
}

func warnAboutOverrideFiles(dir string, stderr io.Writer) {
	for _, name := range []string{"compose.override.yml", "compose.override.yaml"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			fmt.Fprintf(stderr, "warning: Houston ignores %s; put dev settings in the compose file\n", name)
		}
	}
}

// writeGenerated writes a file into .houston/, which ignores itself in git so
// generated files are never committed, even in repos that never ran init.
// writeGenerated writes .houston/<name> in dir (a plain directory: see
// project.GeneratedDir), and returns its path.
func writeGenerated(dir, name string, data []byte) (string, error) {
	houston, err := project.GeneratedDir(dir)
	if err != nil {
		return "", err
	}
	if err := writeAtomic(filepath.Join(houston, ".gitignore"), []byte("*\n")); err != nil {
		return "", err
	}
	path := filepath.Join(houston, name)
	return path, writeAtomic(path, data)
}

// writeAtomic replaces path with data via a temp file and rename, so a crash
// never leaves a half-written file behind. An existing file keeps its mode;
// a new one gets 0644.
func writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	mode := fs.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
