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
	"github.com/sevenmoons/houston/internal/docker"
	"github.com/sevenmoons/houston/internal/project"
	"github.com/sevenmoons/houston/internal/variant"
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

	name, content := "compose.dev.yml", variant.DevOverride(p)
	if production {
		name, content = "compose.production.yml", variant.ProductionOverride(p)
		noteBlankOptional(p, values, stderr)
	}
	override := filepath.Join(dir, ".houston", name)
	if err := writeGenerated(override, content); err != nil {
		fmt.Fprintf(stderr, "houston: can't write .houston/%s: %v\n", name, err)
		return exitFailure
	}
	// -p pins the project name: compose would otherwise let a stray
	// COMPOSE_PROJECT_NAME (shell or .env) rename the containers.
	code, err := d.Run(dir, nil, "compose", "-p", p.Name, "--project-directory", dir, "-f", abs, "-f", override, "up", "--build")
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
func writeGenerated(path string, data []byte) error {
	if err := writeAtomic(filepath.Join(filepath.Dir(path), ".gitignore"), []byte("*\n")); err != nil {
		return err
	}
	return writeAtomic(path, data)
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
