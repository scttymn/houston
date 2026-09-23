package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/compose-spec/compose-go/v2/dotenv"
	"github.com/sevenmoons/houston/internal/project"
	"github.com/sevenmoons/houston/internal/stack"
	"go.yaml.in/yaml/v3"
)

// change is one file init will write, with the line it prints afterwards.
type change struct {
	path    string
	content string
	message string
}

// runInit implements `houston init` for Rails + SQLite. Every file is its own
// idempotent step, so a rerun finishes what an interrupted run started. All
// changes are planned and checked before anything is written.
func runInit(file string, stdin io.Reader, stdout, stderr io.Writer) int {
	composePath, err := filepath.Abs(file)
	if err != nil {
		fmt.Fprintf(stderr, "houston: %v\n", err)
		return exitUsage
	}
	dir := filepath.Dir(composePath)
	fail := func(format string, args ...any) int {
		fmt.Fprintf(stderr, "houston: "+format+"\n", args...)
		return exitFailure
	}

	if err := stack.DetectRails(dir); err != nil {
		return fail("%v", err)
	}
	dockerfilePath := filepath.Join(dir, "Dockerfile")
	src, err := os.ReadFile(dockerfilePath)
	if errors.Is(err, fs.ErrNotExist) {
		return fail("no Dockerfile; Rails 7.1+ generates one with `rails new`. Add it, then run `houston init` again")
	} else if err != nil {
		return fail("can't read the Dockerfile: %v", err)
	}
	docker, err := stack.RailsDockerfile(string(src))
	if err != nil {
		return fail("%v", err)
	}

	var changes []change
	if docker.AddedStages || docker.NamedFinal {
		msg := "added dev and test stages to Dockerfile"
		switch {
		case docker.AddedStages && docker.NamedFinal:
			msg += "; named the final stage production"
		case docker.NamedFinal:
			msg = "named the final Dockerfile stage production"
		}
		changes = append(changes, change{dockerfilePath, docker.Content, msg})
	}

	compose, composeChange, code := planCompose(composePath, dir, docker.Workdir, stdin, stdout, stderr)
	if code != 0 {
		return code
	}
	if composeChange != nil {
		changes = append(changes, *composeChange)
	}
	envChange, err := planDotEnv(filepath.Join(dir, ".env"), compose)
	if err != nil {
		return fail("%v", err)
	}
	if envChange != nil {
		changes = append(changes, *envChange)
	}
	if ignoreChange, err := planGitignore(filepath.Join(dir, ".gitignore")); err != nil {
		return fail("%v", err)
	} else if ignoreChange != nil {
		changes = append(changes, *ignoreChange)
	}

	if len(changes) == 0 {
		fmt.Fprintln(stdout, "houston: already set up; nothing to change")
		return 0
	}
	for _, c := range changes {
		if err := writeAtomic(c.path, []byte(c.content)); err != nil {
			return fail("can't write %s: %v", filepath.Base(c.path), err)
		}
		fmt.Fprintln(stdout, c.message)
	}
	fmt.Fprintln(stdout, "Next: houston dev")
	return 0
}

// planCompose returns the project the compose file will describe, and the
// change to make (nil when it already has x-houston). The result is checked
// with project.Parse before anything is written.
func planCompose(path, dir, workdir string, stdin io.Reader, stdout, stderr io.Writer) (*project.Project, *change, int) {
	existing, err := os.ReadFile(path)
	var content, message string
	switch {
	case errors.Is(err, fs.ErrNotExist):
		name, ok := askName(dir, stdin, stdout, stderr)
		if !ok {
			return nil, nil, exitUsage
		}
		content = stack.RailsCompose(name, workdir)
		message = "created " + filepath.Base(path)
	case err != nil:
		fmt.Fprintf(stderr, "houston: can't read %s: %v\n", filepath.Base(path), err)
		return nil, nil, exitFailure
	case hasXHouston(existing):
		p, err := project.Parse(path, existing)
		if err != nil {
			fmt.Fprint(stderr, err)
			return nil, nil, exitFailure
		}
		return p, nil, 0
	default:
		content = string(existing)
		if !strings.HasSuffix(content, "\n") {
			content += "\n"
		}
		content += "\n" + stack.RailsXHouston
		message = "added x-houston to " + filepath.Base(path)
	}
	p, err := project.Parse(path, []byte(content))
	if err != nil {
		fmt.Fprint(stderr, err)
		return nil, nil, exitFailure
	}
	return p, &change{path, content, message}, 0
}

func hasXHouston(data []byte) bool {
	var top map[string]any
	if yaml.Unmarshal(data, &top) != nil {
		return false
	}
	_, ok := top["x-houston"]
	return ok
}

var notNameChars = regexp.MustCompile(`[^a-z0-9-]+`)

// askName asks for the project name when stdin is a terminal, defaulting to
// the folder name. Without a terminal the default is used as is.
func askName(dir string, stdin io.Reader, stdout, stderr io.Writer) (string, bool) {
	name := strings.Trim(notNameChars.ReplaceAllString(strings.ReplaceAll(strings.ToLower(filepath.Base(dir)), "_", "-"), ""), "-")
	if stdinIsTerminal() {
		fmt.Fprintf(stdout, "Project name [%s]: ", name)
		line, _ := bufio.NewReader(stdin).ReadString('\n')
		if typed := strings.TrimSpace(line); typed != "" {
			name = typed
		}
	}
	if err := project.CheckName(name); err != nil {
		fmt.Fprintf(stderr, "houston: project name %q %v\n", name, err)
		return "", false
	}
	return name, true
}

// planDotEnv adds a line for every secret the compose file references and
// .env lacks, never touching existing values.
func planDotEnv(path string, p *project.Project) (*change, error) {
	var names []string
	for _, v := range p.Variables {
		if v.Kind == project.Secret {
			names = append(names, v.Name)
		}
	}
	existing, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		if len(names) == 0 {
			return nil, nil
		}
		return &change{path, lines(names), fmt.Sprintf("created .env (%s)", strings.Join(names, ", "))}, nil
	} else if err != nil {
		return nil, fmt.Errorf("can't read .env: %w", err)
	}
	values, err := dotenv.UnmarshalBytesWithLookup(existing, func(string) (string, bool) { return "", false })
	if err != nil {
		return nil, fmt.Errorf(".env: %w", err)
	}
	var missing []string
	for _, n := range names {
		if _, ok := values[n]; !ok {
			missing = append(missing, n)
		}
	}
	if len(missing) == 0 {
		return nil, nil
	}
	return &change{path, withNewline(string(existing)) + lines(missing), fmt.Sprintf("added %s to .env", strings.Join(missing, ", "))}, nil
}

var envIgnored = map[string]bool{".env": true, "/.env": true, ".env*": true, "/.env*": true}

// planGitignore makes sure .env is ignored.
func planGitignore(path string) (*change, error) {
	existing, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return &change{path, "/.env\n", "created .gitignore (/.env)"}, nil
	} else if err != nil {
		return nil, fmt.Errorf("can't read .gitignore: %w", err)
	}
	for _, l := range strings.Split(string(existing), "\n") {
		if envIgnored[strings.TrimSpace(l)] {
			return nil, nil
		}
	}
	return &change{path, withNewline(string(existing)) + "/.env\n", "added /.env to .gitignore"}, nil
}

func lines(names []string) string {
	var b strings.Builder
	for _, n := range names {
		b.WriteString(n + "=\n")
	}
	return b.String()
}

func withNewline(s string) string {
	if s != "" && !strings.HasSuffix(s, "\n") {
		return s + "\n"
	}
	return s
}
