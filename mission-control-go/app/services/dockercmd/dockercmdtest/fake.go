// Package dockercmdtest is a docker CLI for tests: it records each command
// (its arguments, environment and input) and answers the first rule whose
// arguments start the command's.
package dockercmdtest

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/scttymn/houston/mission-control-go/app/services/dockercmd"
)

// Call is a command run: args (for a pipe, "from | to"), what it was given.
type Call struct {
	Args  []string
	To    []string // a pipe's second command
	Env   map[string]string
	Stdin []byte
}

// String is the call as one line: its args, and a pipe's second command.
func (c Call) String() string {
	s := strings.Join(c.Args, " ")
	if c.To != nil {
		s += " | " + strings.Join(c.To, " ")
	}
	return s
}

type rule struct {
	prefix []string
	result dockercmd.Result
	wait   time.Duration
}

// Fake is the docker CLI. A command no rule answers succeeds with no output.
type Fake struct {
	mu    sync.Mutex
	rules []rule
	calls []Call
}

// On answers a command starting with args with result.
func (f *Fake) On(result dockercmd.Result, args ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rules = append(f.rules, rule{prefix: args, result: result})
}

// Slow makes a command starting with args take d first.
func (f *Fake) Slow(d time.Duration, args ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rules = append(f.rules, rule{prefix: args, result: dockercmd.Result{OK: true}, wait: d})
}

// OK and Fail are results.
func OK(output string) dockercmd.Result { return dockercmd.Result{OK: true, Output: output} }
func Fail(code int, output string) dockercmd.Result {
	return dockercmd.Result{Output: output, Code: code}
}

// Calls are the commands run so far.
func (f *Fake) Calls() []Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

// Ran are the commands run, each as one line.
func (f *Fake) Ran() []string {
	var out []string
	for _, c := range f.Calls() {
		out = append(out, c.String())
	}
	return out
}

func (f *Fake) answer(ctx context.Context, c Call, o dockercmd.Opts) dockercmd.Result {
	f.mu.Lock()
	f.calls = append(f.calls, c)
	var found *rule
	for i := range f.rules {
		if len(c.Args) >= len(f.rules[i].prefix) && slices.Equal(c.Args[:len(f.rules[i].prefix)], f.rules[i].prefix) {
			found = &f.rules[i]
			break
		}
	}
	f.mu.Unlock()
	if found == nil {
		return dockercmd.Result{OK: true}
	}
	if found.wait > 0 {
		timer := time.NewTimer(found.wait)
		defer timer.Stop()
		deadline := make(<-chan time.Time)
		if o.Timeout > 0 {
			deadline = time.After(o.Timeout)
		}
		select {
		case <-timer.C:
		case <-deadline:
			return dockercmd.Result{Code: dockercmd.TimedOut}
		case <-ctx.Done():
			return dockercmd.Result{Code: dockercmd.TimedOut}
		}
	}
	return found.result
}

func (f *Fake) Run(ctx context.Context, args []string, o dockercmd.Opts) dockercmd.Result {
	return f.answer(ctx, Call{Args: args, Env: o.Env, Stdin: o.Stdin}, o)
}

func (f *Fake) Pipe(ctx context.Context, from, to []string, o dockercmd.Opts) dockercmd.Result {
	return f.answer(ctx, Call{Args: from, To: to, Env: o.Env}, o)
}
