package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/scttymn/houston/internal/server"
)

// runSSH runs ssh attached to the terminal and returns its exit code. Tests
// replace it.
var runSSH = func(args []string) int {
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	err := cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode()
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "houston: %v\n", err)
		return exitFailure
	}
	return 0
}

func runLogsServer(file, projectFlag string, follow bool, tail int, stdout, stderr io.Writer) int {
	client, name, code := remote(file, projectFlag, true, stderr)
	if code != 0 {
		return code
	}
	if err := client.Logs(context.Background(), name, follow, tail, stdout); err != nil {
		return remoteFailed(err, stderr)
	}
	return 0
}

// runConsoleServer runs the console command in the running app container on
// the server (runInServerApp).
func runConsoleServer(file string, stderr io.Writer) int {
	p, ok := loadProject(file, stderr)
	if !ok {
		return exitUsage
	}
	console := p.Houston.Commands.Console
	if console == nil || console.Server == "" {
		fmt.Fprintf(stderr, "houston: no x-houston.commands.console (server) in %s\n", file)
		return exitUsage
	}
	client, _, code := remote(file, p.Name, true, stderr)
	if code != 0 {
		return code
	}
	return runInServerApp(client, p.Name, "console --server", true, []string{"sh", "-c", console.Server}, stderr)
}

// runExecServer implements `houston exec --server CMD [ARGS...]`
// (docs/plans/exec-server.md): the command in the project's running app
// container on the server. Its output goes only to this terminal (it can be
// a one-time credential), and its exit code is the command's.
func runExecServer(file, projectFlag string, command []string, stderr io.Writer) int {
	client, name, code := remote(file, projectFlag, true, stderr)
	if code != 0 {
		return code
	}
	return runInServerApp(client, name, "exec --server", stdinIsTerminal(), command, stderr)
}

// runInServerApp runs argv in project name's running app container on the
// server, over SSH to houston@<LAN address> (LAN-only for now). Every word is
// quoted for the remote shell. tty: ask ssh and docker for a terminal.
// what names the command in the no-target message.
func runInServerApp(client *server.Client, name, what string, tty bool, argv []string, stderr io.Writer) int {
	target := client.SSHTarget(os.Getenv)
	if target == "" {
		fmt.Fprintln(stderr, "houston: "+what+" connects over SSH on the LAN. Say where: houston login --ssh houston@<server's LAN address> <url>\n"+
			"(or HOUSTON_SSH), and authorize your key once: ssh-copy-id houston@<server's LAN address>")
		return exitUsage
	}
	words := make([]string, len(argv))
	for i, a := range argv {
		words[i] = singleQuote(a)
	}
	dockerExec, sshTTY := "docker exec -i", "-T"
	if tty {
		dockerExec, sshTTY = "docker exec -i -t", "-t"
	}
	remoteCmd := `c="$(docker ps -q --filter ` + singleQuote("label=service="+name) + ` --filter label=role=web | head -n1)"; ` +
		`[ -n "$c" ] || { echo ` + singleQuote("houston: "+name+" isn't running on the server") + ` >&2; exit 1; }; ` +
		`exec ` + dockerExec + ` "$c" ` + strings.Join(words, " ")
	return runSSH([]string{"ssh", sshTTY, target, remoteCmd})
}

// singleQuote quotes s for a POSIX shell.
func singleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
