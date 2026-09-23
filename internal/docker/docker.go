// Package docker is the only place Houston runs the docker CLI.
package docker

import (
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
)

// Runner runs the docker CLI.
type Runner interface {
	// LookPath reports whether docker is on PATH.
	LookPath() error
	// Output runs docker with args and returns its stdout.
	Output(args ...string) ([]byte, error)
	// Run runs docker with args in dir, attached to Houston's stdio, and
	// returns its exit code.
	Run(dir string, args ...string) (int, error)
}

// New returns a Runner for the real docker CLI.
func New() Runner { return cliRunner{} }

type cliRunner struct{}

func (cliRunner) LookPath() error {
	_, err := exec.LookPath("docker")
	return err
}

func (cliRunner) Output(args ...string) ([]byte, error) {
	return exec.Command("docker", args...).Output()
}

// Run waits out Ctrl-C instead of dying on it. The terminal sends SIGINT to
// docker as well, and docker needs time to stop containers; returning early
// would hand the prompt back mid-shutdown. The signal is caught rather than
// ignored so the child starts with the default disposition.
func (cliRunner) Run(dir string, args ...string) (int, error) {
	interrupts := make(chan os.Signal, 1)
	signal.Notify(interrupts, os.Interrupt)
	defer signal.Stop(interrupts)

	cmd := exec.Command("docker", args...)
	cmd.Dir = dir
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	err := cmd.Run()

	var exit *exec.ExitError
	if errors.As(err, &exit) {
		if status, ok := exit.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			return 128 + int(status.Signal()), nil
		}
		return exit.ExitCode(), nil
	}
	if err != nil {
		return 1, err
	}
	return 0, nil
}
