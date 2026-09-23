package cli

import (
	"reflect"
	"strings"
	"testing"
)

var anyAppQuery = []string{"ps", "-a", "-q",
	"--filter", "label=com.docker.compose.project=phoenixapp",
	"--filter", "label=com.docker.compose.service=app",
	"--filter", "label=com.docker.compose.oneoff=False"}

func TestLogs_ShowsAppLogs(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want []string
	}{
		{nil, []string{"logs", "abc123"}},
		{[]string{"-f"}, []string{"logs", "--follow", "abc123"}},
		{[]string{"--follow"}, []string{"logs", "--follow", "abc123"}},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			_, path := newProject(t, phoenix, nil)
			d := &fakeDocker{psOut: "abc123\n", runExit: 6}

			code, _, stderr := run(d, append([]string{"--file", path, "logs"}, tc.args...)...)

			if code != 6 {
				t.Errorf("exit = %d, want docker's 6 (stderr: %s)", code, stderr)
			}
			if !reflect.DeepEqual(d.lastOutput(), anyAppQuery) {
				t.Errorf("lookup = %q\nwant %q", d.lastOutput(), anyAppQuery)
			}
			if len(d.runs) != 1 || !reflect.DeepEqual(d.runs[0], tc.want) {
				t.Errorf("runs = %q, want %q", d.runs, tc.want)
			}
		})
	}
}

func TestLogs_NoContainer(t *testing.T) {
	_, path := newProject(t, phoenix, nil)
	d := &fakeDocker{psOut: "\n"}

	code, _, stderr := run(d, "--file", path, "logs")

	if code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
	if !strings.Contains(stderr, "no app container for phoenixapp") || !strings.Contains(stderr, "houston dev") {
		t.Errorf("stderr = %q", stderr)
	}
	if len(d.runs) != 0 {
		t.Errorf("logs ran: %q", d.runs)
	}
}
