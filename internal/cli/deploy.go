package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"github.com/sevenmoons/houston/internal/deploy"
	"github.com/sevenmoons/houston/internal/docker"
	"github.com/sevenmoons/houston/internal/mission"
	"github.com/sevenmoons/houston/internal/runner"
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
	// Kamal reaches this server over SSH with the houston user's key.
	if _, err := os.Stat(filepath.Join(home, ".ssh", "id_ed25519")); err != nil {
		fmt.Fprintln(stderr, "houston deploy: run it as the houston user (sudo -iu houston): Kamal deploys over SSH with its key, ~houston/.ssh/id_ed25519")
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
	}, deploy.Deps{Docker: d, Git: gitCLI{}, Mission: client, Exec: execCLI{}})
}

// runRunner implements houston runner: a houston-runner-N container's loop.
func runRunner(name, workspace string, stderr io.Writer, d docker.Runner) int {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(stderr, "houston runner: %v\n", err)
		return exitFailure
	}
	client, err := mission.FromEnvironment(os.Getenv, home)
	if err != nil {
		fmt.Fprintf(stderr, "houston runner: %v\n", err)
		return exitFailure
	}
	self, err := os.Executable()
	if err != nil {
		fmt.Fprintf(stderr, "houston runner: %v\n", err)
		return exitFailure
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	r := &runner.Runner{
		Name: name, Workspace: workspace, Mission: client, Git: gitCLI{}, Sleep: runner.Sleep, Docker: d,
		Deploy: func(ctx context.Context, o deploy.Options) int {
			return deploy.Run(ctx, o, deploy.Deps{Docker: d, Git: gitCLI{}, Mission: client, Exec: execCLI{}})
		},
		Base: deploy.Options{Arch: runtime.GOARCH, SSHDir: filepath.Join(home, ".ssh"), Environ: os.Environ(), Stdout: os.Stdout, Stderr: stderr, Houston: self},
	}
	fmt.Fprintf(stderr, "houston runner: %s polling %s\n", name, client.URL)
	r.Run(ctx)
	return 0
}

// execCLI runs a program with its output streamed to out.
type execCLI struct{}

func (execCLI) Stream(ctx context.Context, dir string, env []string, out io.Writer, name string, args ...string) (int, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = dir, env, out, out
	err := cmd.Run()
	if exit, ok := err.(*exec.ExitError); ok {
		return exit.ExitCode(), nil
	}
	if err != nil {
		return 1, err
	}
	return 0, nil
}

// gitCLI runs git in a directory and returns its stdout.
type gitCLI struct{}

// Run runs git in dir with env, returning its combined output.
func (gitCLI) Run(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (gitCLI) Output(dir string, args ...string) (string, error) {
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	if exit, ok := err.(*exec.ExitError); ok && len(exit.Stderr) > 0 {
		return string(out), fmt.Errorf("%w: %s", err, strings.TrimSpace(string(exit.Stderr)))
	}
	return string(out), err
}
