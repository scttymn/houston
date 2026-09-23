package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
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
// the server, over SSH to houston@<LAN address> (LAN-only for now).
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
	target := client.SSHTarget(os.Getenv)
	if target == "" {
		fmt.Fprintln(stderr, "houston: console --server connects over SSH on the LAN. Say where: houston login --ssh houston@<server's LAN address> <url>\n"+
			"(or HOUSTON_SSH), and authorize your key once: ssh-copy-id houston@<server's LAN address>")
		return exitUsage
	}
	container := fmt.Sprintf(`"$(docker ps -q --filter label=service=%s --filter label=role=web | head -n1)"`, p.Name)
	remoteCmd := "docker exec -it " + container + " sh -c " + singleQuote(console.Server)
	return runSSH([]string{"ssh", "-t", target, remoteCmd})
}

// singleQuote quotes s for a POSIX shell.
func singleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
