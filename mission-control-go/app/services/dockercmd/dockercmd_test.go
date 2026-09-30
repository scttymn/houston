package dockercmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// sh is a runner of sh in place of docker, for its semantics.
var sh = CLI{Bin: "sh"}

// A command's output (stdout and stderr together), its exit, its input,
// and secrets only through the environment.
func TestRun(t *testing.T) {
	ctx := context.Background()
	r := sh.Run(ctx, []string{"-c", `echo out; echo err >&2; exit 3`}, Opts{})
	if r.OK || r.Code != 3 || r.Output != "out\nerr\n" {
		t.Errorf("= %+v", r)
	}
	r = sh.Run(ctx, []string{"-c", `cat; printf " $SECRET"`}, Opts{Stdin: []byte("in"), Env: map[string]string{"SECRET": "s3cret"}})
	if !r.OK || r.Code != 0 || r.Output != "in s3cret" {
		t.Errorf("= %+v", r)
	}
	if r := (CLI{Bin: "no-such-binary"}).Run(ctx, nil, Opts{}); r.OK || r.Output == "" {
		t.Errorf("no binary: %+v", r)
	}
}

// A command past its time is stopped, with the process group, as exit 124
// (timeout's).
func TestRunTimesOut(t *testing.T) {
	began := time.Now()
	r := sh.Run(context.Background(), []string{"-c", `sleep 30 & sleep 30; echo late`}, Opts{Timeout: 200 * time.Millisecond})
	if r.OK || r.Code != 124 || time.Since(began) > 10*time.Second {
		t.Errorf("= %+v after %v", r, time.Since(began))
	}
}

// from | to: both commands' stderr and to's stdout; it succeeds only if
// both do.
func TestPipe(t *testing.T) {
	ctx := context.Background()
	file := filepath.Join(t.TempDir(), "piped")
	r := sh.Pipe(ctx, []string{"-c", `printf "data"; echo "from's note" >&2`}, []string{"-c", `cat > ` + file + `; echo "to's note" >&2`}, Opts{})
	got, _ := os.ReadFile(file)
	if !r.OK || string(got) != "data" || !strings.Contains(r.Output, "from's note") || !strings.Contains(r.Output, "to's note") {
		t.Errorf("= %+v, file %q", r, got)
	}
	if r := sh.Pipe(ctx, []string{"-c", `printf x; exit 2`}, []string{"-c", `cat >/dev/null`}, Opts{}); r.OK || r.Code != 2 {
		t.Errorf("from failing: %+v", r)
	}
	if r := sh.Pipe(ctx, []string{"-c", `printf x`}, []string{"-c", `cat >/dev/null; exit 5`}, Opts{}); r.OK || r.Code != 5 {
		t.Errorf("to failing: %+v", r)
	}
	if r := sh.Pipe(ctx, []string{"-c", `sleep 30`}, []string{"-c", `cat`}, Opts{Timeout: 200 * time.Millisecond}); r.Code != 124 {
		t.Errorf("timed out: %+v", r)
	}
}
