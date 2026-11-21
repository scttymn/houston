package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// execServer is set up for one exec --server test: a logged-in server, an
// SSH target, and ssh replaced by a recorder exiting with exit.
func execServer(t *testing.T, exit int) *fakeSSH {
	t.Helper()
	remoteServer(t, nil)
	t.Setenv("HOUSTON_SSH", "houston@192.168.1.20")
	ssh := &fakeSSH{exit: exit}
	was := runSSH
	runSSH = ssh.run
	t.Cleanup(func() { runSSH = was })
	return ssh
}

// nowhere is a compose file path that doesn't exist: run outside a project.
func nowhere(t *testing.T) string {
	return filepath.Join(t.TempDir(), "compose.yml")
}

// serverAppLookup is the remote shell's start: the project's running app
// container, or a message and exit 1 when there's none.
func serverAppLookup(name string) string {
	return `c="$(docker ps -q --filter 'label=service=` + name + `' --filter label=role=web | head -n1)"; ` +
		`[ -n "$c" ] || { echo 'houston: ` + name + ` isn'\''t running on the server' >&2; exit 1; }; `
}

func TestExecServer_RunsInServerApp(t *testing.T) {
	terminal(t, true)
	ssh := execServer(t, 7)

	code, stdout, errOut := run(&fakeDocker{}, "-f", nowhere(t), "exec", "--server", "--project", "svnmns", "task", "auth:setup-code")

	if code != 7 {
		t.Errorf("exit = %d, want the command's 7 (stderr: %s)", code, errOut)
	}
	remote := serverAppLookup("svnmns") + `exec docker exec -i -t "$c" 'task' 'auth:setup-code'`
	if want := []string{"ssh", "-t", "houston@192.168.1.20", remote}; !reflect.DeepEqual(ssh.args, want) {
		t.Errorf("ssh args =\n%q\nwant\n%q", ssh.args, want)
	}
	if stdout != "" || errOut != "" {
		t.Errorf("houston wrote output of its own: stdout %q, stderr %q", stdout, errOut)
	}
}

func TestExecServer_PipedInputSkipsTTY(t *testing.T) {
	terminal(t, false)
	ssh := execServer(t, 0)

	code, _, errOut := run(&fakeDocker{}, "-f", nowhere(t), "exec", "--server", "--project", "svnmns", "cat")

	if code != 0 {
		t.Errorf("exit = %d (stderr: %s)", code, errOut)
	}
	remote := serverAppLookup("svnmns") + `exec docker exec -i "$c" 'cat'`
	if want := []string{"ssh", "-T", "houston@192.168.1.20", remote}; !reflect.DeepEqual(ssh.args, want) {
		t.Errorf("ssh args =\n%q\nwant\n%q", ssh.args, want)
	}
}

func TestExecServer_ProjectFromComposeFile(t *testing.T) {
	terminal(t, false)
	ssh := execServer(t, 0)
	_, path := newProject(t, phoenix, nil)

	// Flags after the command are the command's.
	code, _, errOut := run(&fakeDocker{}, "-f", path, "exec", "--server", "ls", "-la", "--server")

	if code != 0 || len(ssh.args) != 4 {
		t.Fatalf("exit = %d, ssh = %q (stderr: %s)", code, ssh.args, errOut)
	}
	if want := serverAppLookup("phoenixapp") + `exec docker exec -i "$c" 'ls' '-la' '--server'`; ssh.args[3] != want {
		t.Errorf("remote =\n%s\nwant\n%s", ssh.args[3], want)
	}
}

// The remote command, run by a real shell with a fake docker on PATH: every
// argument arrives as itself, nothing runs that the arguments name.
func TestExecServer_ArgumentsReachTheContainerIntact(t *testing.T) {
	terminal(t, false)
	ssh := execServer(t, 0)
	args := []string{"echo 'hi'; id", "$(touch pwned)", "`touch pwned`", "a b", `"q"`, `\`, "", "-n", "*"}
	_, _, errOut := run(&fakeDocker{}, append([]string{"-f", nowhere(t), "exec", "--server", "--project", "it's; touch pwned", "cmd"}, args...)...)
	if len(ssh.args) != 4 {
		t.Fatalf("ssh = %q (stderr: %s)", ssh.args, errOut)
	}

	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	must(t, os.Mkdir(bin, 0o755))
	// docker ps prints the container only for the project's exact label.
	fake := `#!/bin/sh
if [ "$1" = ps ]; then
  [ "$4" = "label=service=it's; touch pwned" ] && echo cid
  exit 0
fi
for a in "$@"; do printf '<%s>\n' "$a"; done
`
	must(t, os.WriteFile(filepath.Join(bin, "docker"), []byte(fake), 0o755))
	sh := exec.Command("sh", "-c", ssh.args[3])
	sh.Dir = dir
	sh.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
	out, err := sh.CombinedOutput()
	if err != nil {
		t.Fatalf("remote shell: %v\n%s", err, out)
	}

	want := "<exec>\n<-i>\n<cid>\n<cmd>\n"
	for _, a := range args {
		want += "<" + a + ">\n"
	}
	if string(out) != want {
		t.Errorf("docker got\n%s\nwant\n%s", out, want)
	}
	if _, err := os.Stat(filepath.Join(dir, "pwned")); err == nil {
		t.Error("an argument ran as a command")
	}
}

func TestExecServer_AppNotRunningOnServer(t *testing.T) {
	terminal(t, false)
	ssh := execServer(t, 0)
	run(&fakeDocker{}, "-f", nowhere(t), "exec", "--server", "--project", "svnmns", "true")
	if len(ssh.args) != 4 {
		t.Fatalf("ssh = %q", ssh.args)
	}

	bin := t.TempDir()
	must(t, os.WriteFile(filepath.Join(bin, "docker"), []byte("#!/bin/sh\n[ \"$1\" = ps ] && exit 0\necho ran exec\n"), 0o755))
	sh := exec.Command("sh", "-c", ssh.args[3])
	sh.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
	out, err := sh.CombinedOutput()

	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 1 {
		t.Errorf("err = %v, want exit 1", err)
	}
	if string(out) != "houston: svnmns isn't running on the server\n" {
		t.Errorf("output = %q", out)
	}
}

func TestExecServer_NoSSHTarget(t *testing.T) {
	ssh := execServer(t, 0)
	t.Setenv("HOUSTON_SSH", "")

	code, _, errOut := run(&fakeDocker{}, "-f", nowhere(t), "exec", "--server", "--project", "svnmns", "true")

	if code != exitUsage {
		t.Errorf("exit = %d, want 2", code)
	}
	for _, s := range []string{"exec --server connects over SSH", "houston login --ssh", "ssh-copy-id"} {
		if !strings.Contains(errOut, s) {
			t.Errorf("stderr = %q, want %q", errOut, s)
		}
	}
	if ssh.args != nil {
		t.Errorf("ssh ran: %q", ssh.args)
	}
}

func TestExecServer_Usage(t *testing.T) {
	for name, c := range map[string]struct {
		args []string
		want string
	}{
		"no project":             {[]string{"exec", "--server", "true"}, "--project"},
		"no command":             {[]string{"exec", "--server", "--project", "svnmns"}, "requires at least 1 arg"},
		"--project, no --server": {[]string{"exec", "--project", "svnmns", "true"}, "--project goes with --server"},
	} {
		t.Run(name, func(t *testing.T) {
			ssh := execServer(t, 0)
			d := &fakeDocker{}
			code, _, errOut := run(d, append([]string{"-f", nowhere(t)}, c.args...)...)
			if code != exitUsage || !strings.Contains(errOut, c.want) {
				t.Errorf("exit = %d, stderr = %q; want 2 and %q", code, errOut, c.want)
			}
			if ssh.args != nil || d.calls() != 0 {
				t.Errorf("ran something: ssh %q, docker %q %q", ssh.args, d.outputs, d.runs)
			}
		})
	}
}
