// Package dockercmd runs the docker CLI for Mission Control's work on the
// server: argv only, secrets through the environment (docker run -e NAME,
// never a value in argv), input on stdin, one command's output piped into
// another, and a deadline that stops the whole process group. (The Rails
// app's DockerCommand.)
package dockercmd

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// Result is a finished command: whether it succeeded, its output (stdout
// and stderr together, as they came), and its exit code (124 when its
// deadline stopped it, as timeout's).
type Result struct {
	OK     bool
	Output string
	Code   int
}

// Opts are a command's environment (added to Mission Control's), input and
// deadline (none: the context's).
type Opts struct {
	Env     map[string]string
	Stdin   []byte
	Timeout time.Duration
}

// Runner runs docker commands.
type Runner interface {
	Run(ctx context.Context, args []string, o Opts) Result
	Pipe(ctx context.Context, from, to []string, o Opts) Result
}

// CLI runs Bin (docker).
type CLI struct{ Bin string }

// Docker is the docker CLI.
var Docker = CLI{Bin: "docker"}

// TimedOut is the exit code of a command its deadline stopped.
const TimedOut = 124

func (c CLI) command(ctx context.Context, args []string, o Opts) *exec.Cmd {
	cmd := exec.CommandContext(ctx, c.Bin, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Stopping it stops everything it started: TERM to the group, then KILL.
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
	cmd.WaitDelay = 5 * time.Second
	if len(o.Env) > 0 {
		cmd.Env = os.Environ()
		for k, v := range o.Env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}
	if o.Stdin != nil {
		cmd.Stdin = bytes.NewReader(o.Stdin)
	}
	return cmd
}

func deadline(ctx context.Context, o Opts) (context.Context, context.CancelFunc) {
	if o.Timeout > 0 {
		return context.WithTimeout(ctx, o.Timeout)
	}
	return context.WithCancel(ctx)
}

// Run runs docker with args.
func (c CLI) Run(ctx context.Context, args []string, o Opts) Result {
	ctx, cancel := deadline(ctx, o)
	defer cancel()
	var out syncBuffer
	cmd := c.command(ctx, args, o)
	cmd.Stdout, cmd.Stderr = &out, &out
	return result(ctx, cmd.Run(), out.String())
}

// Pipe runs from | to: the output is both commands' stderr and to's
// stdout, and it succeeds only if both do (the exit code is the first
// failing one's).
func (c CLI) Pipe(ctx context.Context, from, to []string, o Opts) Result {
	ctx, cancel := deadline(ctx, o)
	defer cancel()
	var out syncBuffer
	first, second := c.command(ctx, from, o), c.command(ctx, to, o)
	reader, writer := io.Pipe()
	first.Stdout, first.Stderr = writer, &out
	second.Stdin, second.Stdout, second.Stderr = reader, &out, &out
	if err := first.Start(); err != nil {
		return Result{Output: err.Error(), Code: -1}
	}
	if err := second.Start(); err != nil {
		first.Process.Kill()
		first.Wait()
		return Result{Output: err.Error(), Code: -1}
	}
	firstErr := first.Wait()
	writer.Close()
	secondErr := second.Wait()
	reader.Close()
	if r := result(ctx, firstErr, out.String()); !r.OK {
		return r
	}
	return result(ctx, secondErr, out.String())
}

func result(ctx context.Context, err error, output string) Result {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return Result{Output: output, Code: TimedOut}
	}
	if err == nil {
		return Result{OK: true, Output: output}
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return Result{Output: output, Code: exit.ExitCode()}
	}
	return Result{Output: output + err.Error(), Code: -1}
}

// syncBuffer is a buffer two processes' output can share.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
