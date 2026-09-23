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
)

// change is one file init will write, with the line it prints afterwards.
type change struct {
	path    string
	content string
	message string
}

// runInit implements `houston init`: it upserts what Houston needs to run
// and deploy the folder (a Dockerfile's stages, compose.yml with x-houston,
// .env, .gitignore), with generic defaults that serve a static site. It knows
// no frameworks: the developer changes the defaults for their project. What
// exists is never replaced, only completed. Every change is planned and
// checked before anything is written, and a rerun finishes what an
// interrupted run started (docs/plans/init-generic.md).
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

	// The compose file first: it says which Dockerfile the app builds.
	var changes []change
	compose, composeChange, code := planCompose(composePath, dir, stdin, stdout, stderr)
	if code != 0 {
		return code
	}
	contextDir, dockerfilePath, err := buildFiles(compose, dir)
	if err != nil {
		return fail("%v", err)
	}
	dockerChange, err := planDockerfile(dockerfilePath, dir)
	if err != nil {
		return fail("%v", err)
	}
	if dockerChange != nil {
		changes = append(changes, *dockerChange)
	}
	if composeChange != nil {
		changes = append(changes, *composeChange)
	}
	// BuildKit reads <Dockerfile>.dockerignore instead of .dockerignore
	// when there is one.
	ignorePath := filepath.Join(contextDir, ".dockerignore")
	if _, err := os.Stat(dockerfilePath + ".dockerignore"); err == nil {
		ignorePath = dockerfilePath + ".dockerignore"
	}
	ignoreDocker, err := planDockerignore(ignorePath, dir, defaultDockerignore(contextDir, composePath, dockerfilePath))
	if err != nil {
		return fail("%v", err)
	}
	if ignoreDocker != nil {
		changes = append(changes, *ignoreDocker)
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
	fmt.Fprintln(stdout, "Next: edit the defaults for your project, then houston dev")
	return 0
}

// buildFiles is where the app is built from, as deploy reads it: the build
// context (default: the compose file's folder) and its Dockerfile (default:
// Dockerfile in the context). init completes only local files.
func buildFiles(p *project.Project, dir string) (contextDir, dockerfile string, err error) {
	build := p.Compose.Services[p.AppService].Build
	context, file := ".", "Dockerfile"
	if build != nil && build.Context != "" {
		context = build.Context
	}
	if build != nil && build.DockerfileInline != "" {
		return "", "", errors.New("the app's Dockerfile is written inline in the compose file (dockerfile_inline); houston init completes Dockerfile files: add dev, test and production stages to it by hand")
	}
	if build != nil && build.Dockerfile != "" {
		file = build.Dockerfile
	}
	for _, p := range []string{context, file} {
		if strings.Contains(p, "$") {
			return "", "", fmt.Errorf("the app builds from %s, which Houston can't resolve here (a variable); write the path, or complete its Dockerfile by hand", p)
		}
	}
	for _, remote := range []string{"http://", "https://", "git://", "ssh://", "git@", "github.com/"} {
		if strings.HasPrefix(context, remote) {
			return "", "", fmt.Errorf("the app builds from %s; houston init only completes a Dockerfile in this folder", context)
		}
	}
	if !filepath.IsAbs(context) {
		context = filepath.Join(dir, context)
	}
	if !filepath.IsAbs(file) {
		file = filepath.Join(context, file)
	}
	for _, p := range []string{context, file} {
		if rel, err := filepath.Rel(dir, p); err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
			return "", "", fmt.Errorf("the app builds from %s, outside this folder; houston init only completes a Dockerfile in this folder", relName(p, dir))
		}
	}
	return context, file, nil
}

// planDockerfile writes the default Dockerfile where there's none, and adds
// the stages Houston builds to one that lacks them. Messages name it
// relative to dir.
func planDockerfile(path, dir string) (*change, error) {
	name := relName(path, dir)
	src, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return &change{path, defaultDockerfile, "created " + name}, nil
	} else if err != nil {
		return nil, fmt.Errorf("can't read %s: %w", name, err)
	}
	content, summary, err := upsertDockerfile(string(src))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	if summary == "" {
		return nil, nil
	}
	return &change{path, content, name + ": " + summary}, nil
}

func relName(path, dir string) string {
	if rel, err := filepath.Rel(dir, path); err == nil {
		return filepath.ToSlash(rel)
	}
	return path
}

// defaultDockerignore keeps what the default production stage mustn't serve
// out of the image: .git, .env, .houston, and the build files this project
// uses (the compose file and Dockerfile, by their names in the context, and
// the ignore files). A build file outside the context isn't in it anyway.
func defaultDockerignore(contextDir string, buildFiles ...string) string {
	b := strings.Builder{}
	b.WriteString("# Kept out of the image: the default production stage copies this folder and\n" +
		"# serves it. The first three stay out whatever the project does; the build\n" +
		"# files are left out of a fresh site too.\n.git\n.env\n.houston\n")
	for _, f := range buildFiles {
		if rel, err := filepath.Rel(contextDir, f); err == nil && rel != ".." && !strings.HasPrefix(rel, "../") {
			b.WriteString(filepath.ToSlash(rel) + "\n")
		}
	}
	b.WriteString(".dockerignore\n.gitignore\n")
	return b.String()
}

// dockerignoreHas: the patterns that already exclude each entry.
var dockerignoreHas = map[string][]string{
	".git":     {".git", ".git/", "/.git", "/.git/", ".git*", "/.git*"},
	".env":     {".env", "/.env", ".env*", "/.env*"},
	".houston": {".houston", ".houston/", "/.houston", "/.houston/"},
}

// planDockerignore writes the default .dockerignore (fresh is what it
// would be), or appends the entries an existing one doesn't already exclude.
func planDockerignore(path, dir, fresh string) (*change, error) {
	name := relName(path, dir)
	existing, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return &change{path, fresh, "created " + name}, nil
	} else if err != nil {
		return nil, fmt.Errorf("can't read %s: %w", name, err)
	}
	have := map[string]bool{}
	for _, l := range strings.Split(string(existing), "\n") {
		have[strings.TrimSpace(l)] = true
	}
	var missing []string
	for _, entry := range []string{".git", ".env", ".houston"} {
		covered := false
		for _, pattern := range dockerignoreHas[entry] {
			covered = covered || have[pattern]
		}
		if !covered {
			missing = append(missing, entry)
		}
	}
	if len(missing) == 0 {
		return nil, nil
	}
	return &change{path, withNewline(string(existing)) + strings.Join(missing, "\n") + "\n", "added " + andList(missing) + " to " + name}, nil
}

// planCompose returns the project the compose file will describe, and the
// change to make (nil when nothing's missing). The result is checked with
// project.Parse before anything is written.
func planCompose(path, dir string, stdin io.Reader, stdout, stderr io.Writer) (*project.Project, *change, int) {
	existing, err := os.ReadFile(path)
	var content, message string
	switch {
	case errors.Is(err, fs.ErrNotExist):
		// Another compose file here is the project's, not a blank slate.
		if filepath.Base(path) == "compose.yml" {
			for _, other := range []string{"compose.yaml", "docker-compose.yml", "docker-compose.yaml"} {
				if _, err := os.Stat(filepath.Join(dir, other)); err == nil {
					fmt.Fprintf(stderr, "houston: this folder has %s; Houston reads compose.yml: rename it, or run houston -f %s init\n", other, other)
					return nil, nil, exitFailure
				}
			}
		}
		name, ok := askName(dir, stdin, stdout, stderr)
		if !ok {
			return nil, nil, exitUsage
		}
		content, message = defaultCompose(name), "created "+filepath.Base(path)
	case err != nil:
		fmt.Fprintf(stderr, "houston: can't read %s: %v\n", filepath.Base(path), err)
		return nil, nil, exitFailure
	default:
		var added []string
		var appended bool
		content, added, appended, err = upsertXHouston(string(existing))
		if err != nil {
			fmt.Fprintf(stderr, "houston: %s: %v\n", filepath.Base(path), err)
			return nil, nil, exitFailure
		}
		switch {
		case appended:
			message = "added x-houston to " + filepath.Base(path)
		case len(added) > 0:
			message = "added " + andList(added) + " to x-houston in " + filepath.Base(path)
		}
	}
	p, err := project.Parse(path, []byte(content))
	if err != nil {
		fmt.Fprint(stderr, err)
		return nil, nil, exitFailure
	}
	if message == "" {
		return p, nil, 0
	}
	return p, &change{path, content, message}, 0
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
