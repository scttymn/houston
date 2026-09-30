// Package dockercmdtest is a docker CLI for tests: it records each command
// (its arguments, environment and input) and answers the first rule whose
// arguments start the command's.
package dockercmdtest

import (
	"context"
	"io"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/scttymn/houston/mission-control-go/app/services/dockercmd"
)

// Call is a command run: args (for a pipe, "from | to"), what it was given.
type Call struct {
	Args    []string
	To      []string // a pipe's second command
	Env     map[string]string
	Stdin   []byte
	Timeout time.Duration
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
	mu        sync.Mutex
	rules     []rule
	downloads []download
	calls     []Call
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
	return f.answer(ctx, Call{Args: args, Env: o.Env, Stdin: o.Stdin, Timeout: o.Timeout}, o)
}

func (f *Fake) Pipe(ctx context.Context, from, to []string, o dockercmd.Opts) dockercmd.Result {
	return f.answer(ctx, Call{Args: from, To: to, Env: o.Env}, o)
}

// Stream answers as Run does, writing the answer's output to w.
func (f *Fake) Stream(ctx context.Context, args []string, o dockercmd.Opts, w io.Writer) dockercmd.Result {
	r := f.answer(ctx, Call{Args: args, Env: o.Env, Timeout: o.Timeout}, o)
	io.WriteString(w, r.Output)
	r.Output = ""
	return r
}

type download struct {
	prefix      []string
	first, rest string
	fail        error
	result      dockercmd.Result
}

// OnDownload answers a download starting with args: its first bytes and
// the rest, ending with fail (nil: it ends well).
func (f *Fake) OnDownload(first, rest string, fail error, args ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.downloads = append(f.downloads, download{prefix: args, first: first, rest: rest, fail: fail})
}

// RefuseDownload answers a download starting with args with result: it
// ended before writing a byte.
func (f *Fake) RefuseDownload(result dockercmd.Result, args ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.downloads = append(f.downloads, download{prefix: args, result: result})
}

// Download answers as the first download rule that fits, once each.
func (f *Fake) Download(ctx context.Context, args []string, o dockercmd.Opts) (*dockercmd.Download, dockercmd.Result) {
	f.mu.Lock()
	f.calls = append(f.calls, Call{Args: args, Env: o.Env})
	var found *download
	for i, d := range f.downloads {
		if len(args) >= len(d.prefix) && slices.Equal(args[:len(d.prefix)], d.prefix) {
			found = &d
			f.downloads = slices.Delete(f.downloads, i, i+1)
			break
		}
	}
	f.mu.Unlock()
	if found == nil {
		return nil, dockercmd.Result{Output: "no such download", Code: 1}
	}
	if found.first == "" {
		return nil, found.result
	}
	return dockercmd.NewDownload([]byte(found.first), strings.NewReader(found.rest), found.fail), dockercmd.Result{OK: true}
}
