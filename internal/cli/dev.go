package cli

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/compose-spec/compose-go/v2/dotenv"
	"github.com/scttymn/houston/internal/docker"
	"github.com/scttymn/houston/internal/project"
	"github.com/scttymn/houston/internal/variant"
)

// runDev implements `houston dev`: compose.yml plus a generated override that
// forces the dev build target (or, with --production, the production one),
// run with `docker compose up --build`.
func runDev(file string, production bool, stderr io.Writer, d docker.Runner) int {
	abs, err := filepath.Abs(file)
	if err != nil {
		fmt.Fprintf(stderr, "houston: %v\n", err)
		return exitUsage
	}
	p, ok := loadProject(file, stderr)
	if !ok {
		return exitUsage
	}
	if !preflight(d, stderr) {
		return exitFailure
	}
	dir := filepath.Dir(abs)
	values, ok := warnAboutVariables(dir, p, stderr)
	if !ok {
		return exitUsage
	}
	warnAboutOverrideFiles(dir, stderr)

	name, content, projectName := "compose.dev.yml", variant.DevOverride(p), p.Name
	if production {
		name, content = "compose.production.yml", variant.ProductionOverride(p)
		// Its own project, so its own volumes: dev's were written by the dev
		// stage (often as root), which a production image running as a user
		// can't write. Fresh volumes are seeded from the image, as on the server.
		projectName = p.Name + "-production"
		noteBlankOptional(p, values, stderr)
	}
	override, err := writeGenerated(dir, name, content)
	if err != nil {
		fmt.Fprintf(stderr, "houston: can't write .houston/%s: %v\n", name, err)
		return exitFailure
	}
	stop := announceWhenUp(p, stderr)
	// -p pins the project name: compose would otherwise let a stray
	// COMPOSE_PROJECT_NAME (shell or .env) rename the containers.
	code, err := d.Run(dir, nil, "compose", "-p", projectName, "--project-directory", dir, "-f", abs, "-f", override, "up", "--build")
	stop()
	if err != nil {
		fmt.Fprintf(stderr, "houston: %v\n", err)
		return exitFailure
	}
	return code
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

// warnAboutVariables warns, by name only, about required secrets that will be
// blank because neither .env nor the shell sets them. Compose reads .env
// itself; this parses it with compose-go's parser so both agree. It returns
// .env's values, and false when .env can't be parsed.
func warnAboutVariables(dir string, p *project.Project, stderr io.Writer) (map[string]string, bool) {
	envPath := filepath.Join(dir, ".env")
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

// devProbe asks the app for its health path; any HTTP answer means it's up.
// A plain TCP check isn't enough: Docker's port forwarding accepts as soon
// as the container starts, before the app listens.
var devProbe = func(url string) error {
	c := http.Client{Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Get(url)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

var devPollEvery = 500 * time.Millisecond

// announceWhenUp prints where the app answers on this machine, once it does:
// the app's own log names its port inside the container, which compose.yml
// may publish elsewhere. The returned func stops the probing; it's called
// when compose returns. Nothing is printed for a port compose.yml doesn't fix.
func announceWhenUp(p *project.Project, stderr io.Writer) (stop func()) {
	ports := p.Compose.Services[p.AppService].Ports
	if len(ports) == 0 || ports[0].Published == "" || strings.Contains(ports[0].Published, "-") {
		return func() {}
	}
	host := ports[0].HostIP
	if host == "" || host == "0.0.0.0" {
		host = "localhost"
	}
	base := "http://" + host + ":" + ports[0].Published
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-done:
				return
			case <-time.After(devPollEvery):
			}
			if devProbe(base+p.Houston.Health) == nil {
				fmt.Fprintf(stderr, "houston: %s is up at %s\n", p.Name, base)
				return
			}
		}
	}()
	return func() { close(done); wg.Wait() }
}
