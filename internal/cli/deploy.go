package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/sevenmoons/houston/internal/deploy"
	"github.com/sevenmoons/houston/internal/docker"
	"github.com/sevenmoons/houston/internal/mission"
)

// runDeploy implements `houston deploy` on a Houston server, as the houston
// user, in a clean checkout of the project.
func runDeploy(file, ref string, stdout, stderr io.Writer, d docker.Runner) int {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(stderr, "houston deploy: %v\n", err)
		return exitFailure
	}
	client, err := mission.FromEnvironment(os.Getenv, home)
	if err != nil {
		fmt.Fprintf(stderr, "houston deploy: %v\n", err)
		return exitFailure
	}
	return deploy.Run(context.Background(), deploy.Options{
		File:    file,
		Ref:     ref,
		Arch:    runtime.GOARCH,
		SSHDir:  filepath.Join(home, ".ssh"),
		Environ: os.Environ(),
		Stdout:  stdout,
		Stderr:  stderr,
	}, deploy.Deps{Docker: d, Git: gitCLI{}, Mission: client})
}

// gitCLI runs git in a directory and returns its stdout.
type gitCLI struct{}

func (gitCLI) Output(dir string, args ...string) (string, error) {
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	if exit, ok := err.(*exec.ExitError); ok && len(exit.Stderr) > 0 {
		return string(out), fmt.Errorf("%w: %s", err, strings.TrimSpace(string(exit.Stderr)))
	}
	return string(out), err
}
