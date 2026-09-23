package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/sevenmoons/houston/internal/docker"
	"github.com/sevenmoons/houston/internal/project"
)

// runConsole implements `houston console`: the dev console command inside the
// app container `houston dev` is running.
func runConsole(file string, stderr io.Writer, d docker.Runner) int {
	p, ok := loadProject(file, stderr)
	if !ok {
		return exitUsage
	}
	console := p.Houston.Commands.Console
	if console == nil {
		fmt.Fprintf(stderr, "houston: no x-houston.commands.console in %s\n", file)
		return exitUsage
	}
	if !preflight(d, stderr) {
		return exitFailure
	}
	id, ok := appContainer(d, p, false, stderr)
	if !ok {
		return exitFailure
	}
	if id == "" {
		fmt.Fprintf(stderr, "houston: %s isn't running; start it with `houston dev`\n", p.Name)
		return exitFailure
	}
	args := []string{"exec", "-i"}
	if stdinIsTerminal() {
		args = append(args, "-t")
	}
	return runDocker(d, stderr, append(args, id, "sh", "-c", console.Dev)...)
}

// runLogs implements `houston logs [-f]` for the dev app container, running
// or stopped.
func runLogs(file string, follow bool, stderr io.Writer, d docker.Runner) int {
	p, ok := loadProject(file, stderr)
	if !ok {
		return exitUsage
	}
	if !preflight(d, stderr) {
		return exitFailure
	}
	id, ok := appContainer(d, p, true, stderr)
	if !ok {
		return exitFailure
	}
	if id == "" {
		fmt.Fprintf(stderr, "houston: no app container for %s; start it with `houston dev`\n", p.Name)
		return exitFailure
	}
	args := []string{"logs"}
	if follow {
		args = append(args, "--follow")
	}
	return runDocker(d, stderr, append(args, id)...)
}

// appContainer finds the newest container of the app service in the project
// `houston dev` runs, by compose's labels ("" when there is none). Stopped
// containers count only when all is set. One-off `run` containers and
// `houston test` projects never match.
func appContainer(d docker.Runner, p *project.Project, all bool, stderr io.Writer) (string, bool) {
	args := []string{"ps"}
	if all {
		args = append(args, "-a")
	}
	args = append(args, "-q",
		"--filter", "label=com.docker.compose.project="+p.Name,
		"--filter", "label=com.docker.compose.service="+p.AppService,
		"--filter", "label=com.docker.compose.oneoff=False")
	out, err := d.Output(args...)
	if err != nil {
		fmt.Fprintf(stderr, "houston: docker ps failed: %v\n", err)
		return "", false
	}
	ids := strings.Fields(string(out)) // docker lists newest first
	if len(ids) == 0 {
		return "", true
	}
	return ids[0], true
}

// runDocker runs docker attached and returns its exit code.
func runDocker(d docker.Runner, stderr io.Writer, args ...string) int {
	code, err := d.Run("", nil, args...)
	if err != nil {
		fmt.Fprintf(stderr, "houston: %v\n", err)
		return exitFailure
	}
	return code
}
