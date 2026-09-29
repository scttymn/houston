package cli

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	"github.com/scttymn/houston/internal/docker"
	"github.com/scttymn/houston/internal/variant"
)

// runExec implements `houston exec CMD [ARGS...]`
// (docs/plans/exec-and-init-name.md): the command in this checkout's dev
// instance, the one `houston dev` runs here. In the app container when it's
// running; otherwise in a one-off container of the same Compose project, so
// the same image, code, volumes and .env. The exit code is the command's.
func runExec(file string, command []string, stderr io.Writer, d docker.Runner) int {
	abs, err := filepath.Abs(file)
	if err != nil {
		fmt.Fprintf(stderr, "houston: %v\n", err)
		return exitUsage
	}
	p, ok := loadProject(file, stderr)
	if !ok {
		return exitUsage
	}
	dir := filepath.Dir(abs)
	root, own, common := gitDirs(dir)
	instance, err := devInstance(dir, p, savedDevName(own))
	if err != nil {
		fmt.Fprintf(stderr, "houston: %v\n", err)
		return exitUsage
	}
	n := devNaming(p.Name, instance, false)
	if !preflight(d, stderr) {
		return exitFailure
	}

	id, ok := serviceContainer(d, n.Project, p.AppService, false, stderr)
	if !ok {
		return exitFailure
	}
	if id != "" {
		args := []string{"exec", "-i"}
		if stdinIsTerminal() {
			args = append(args, "-t")
		}
		return runDocker(d, stderr, slices.Concat(args, []string{id}, command)...)
	}

	// Not running: a one-off, set up as houston dev sets up the app.
	envFile := filepath.Join(dir, ".env")
	fromMain := ""
	if _, err := os.Stat(envFile); errors.Is(err, fs.ErrNotExist) {
		if fromMain = mainCheckoutEnv(dir, root, own, common); fromMain != "" {
			envFile = fromMain
		}
	}
	if _, ok := warnAboutVariables(envFile, p, stderr); !ok {
		return exitUsage
	}
	if instance != "" {
		if err := copyMainData(d, p, n, false, stderr); err != nil {
			fmt.Fprintf(stderr, "houston: %v\n", err)
			return exitFailure
		}
	}
	override, err := writeGenerated(dir, "compose.exec.yml", variant.ExecOverride(p))
	if err != nil {
		fmt.Fprintf(stderr, "houston: can't write .houston/compose.exec.yml: %v\n", err)
		return exitFailure
	}
	base := []string{"compose", "-p", n.Project}
	if fromMain != "" {
		base = append(base, "--env-file", fromMain)
	}
	base = append(base, "--project-directory", dir, "-f", abs, "-f", override)

	// Compose starts the app's dependencies for the run; when nothing of the
	// project was running, they're stopped again afterwards.
	wasRunning := len(running(d, n.Project)) > 0
	run := []string{"run", "--rm"}
	if !stdinIsTerminal() {
		run = append(run, "-T")
	}
	code, runErr := d.Run(dir, nil, slices.Concat(base, run, []string{p.AppService}, command)...)
	if !wasRunning {
		if _, err := d.Output(slices.Concat(base, []string{"stop"})...); err != nil {
			fmt.Fprintf(stderr, "warning: couldn't stop what the run started; stop it with: docker compose -p %s stop\n", n.Project)
		}
	}
	if runErr != nil {
		fmt.Fprintf(stderr, "houston: %v\n", runErr)
		return exitFailure
	}
	return code
}
