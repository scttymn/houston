// Package fakedocker is a docker CLI for tests: it records each command,
// and answers the first rule whose args start the command's.
package fakedocker

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
)

// Rule is an answer: for a command starting with Args, Code and Output.
type Rule struct {
	Args   []string
	Code   int
	Output string
}

// Docker is the fake. A command no rule answers exits 0 with no output.
type Docker struct {
	mu    sync.Mutex
	rules []Rule
	ran   [][]string
}

// On adds a rule: a command starting with args exits code with output.
func (d *Docker) On(code int, output string, args ...string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.rules = append(d.rules, Rule{Args: args, Code: code, Output: output})
}

// Ran are the commands run, each joined with spaces.
func (d *Docker) Ran() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []string
	for _, args := range d.ran {
		out = append(out, strings.Join(args, " "))
	}
	return out
}

func (d *Docker) answer(args []string) Rule {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.ran = append(d.ran, args)
	for _, r := range d.rules {
		if len(args) >= len(r.Args) && slices.Equal(args[:len(r.Args)], r.Args) {
			return r
		}
	}
	return Rule{}
}

func (d *Docker) LookPath() error { return nil }

func (d *Docker) Output(args ...string) ([]byte, error) {
	r := d.answer(args)
	if r.Code != 0 {
		return []byte(r.Output), fmt.Errorf("exit status %d: %s", r.Code, strings.TrimSpace(r.Output))
	}
	return []byte(r.Output), nil
}

func (d *Docker) Run(dir string, env []string, args ...string) (int, error) {
	return d.answer(args).Code, nil
}

func (d *Docker) Stream(ctx context.Context, dir string, env []string, out io.Writer, args ...string) (int, error) {
	r := d.answer(args)
	io.WriteString(out, r.Output)
	return r.Code, nil
}
