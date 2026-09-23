package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/scttymn/houston/internal/server"
)

// runLogin implements houston login [url]: checks the token against the
// server (/api/v1/me) and only then saves it for --server commands.
func runLogin(args []string, accessID, accessSecret, sshTarget string, stdin io.Reader, stdout, stderr io.Writer) int {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(stderr, "houston login: %v\n", err)
		return exitFailure
	}
	in := bufio.NewReader(stdin)
	url := ""
	if len(args) > 0 {
		url = args[0]
	} else {
		fmt.Fprint(stderr, "Mission Control's URL (e.g. https://admin.example.com): ")
		url = readLine(in)
	}
	if !strings.HasPrefix(url, "https://") && !strings.HasPrefix(url, "http://") {
		fmt.Fprintln(stderr, "houston login: the URL starts with https:// (or http:// on the LAN)")
		return exitUsage
	}
	token := os.Getenv("HOUSTON_API_TOKEN")
	if token == "" {
		if stdinIsTerminal() {
			fmt.Fprint(stderr, "API token (Settings › API tokens): ")
			secret, err := term.ReadPassword(int(os.Stdin.Fd()))
			fmt.Fprintln(stderr)
			if err != nil {
				fmt.Fprintf(stderr, "houston login: %v\n", err)
				return exitFailure
			}
			token = string(secret)
		} else {
			token = readLine(in)
		}
	}
	c := server.Config{URL: strings.TrimRight(url, "/"), Token: strings.TrimSpace(token), AccessClientID: accessID, AccessClientSecret: accessSecret, SSH: sshTarget}
	me, err := server.New(c).Me(context.Background())
	if err != nil {
		fmt.Fprintf(stderr, "houston login: %v\n", err)
		return exitFailure
	}
	if err := server.Save(home, c); err != nil {
		fmt.Fprintf(stderr, "houston login: can't save the token: %v\n", err)
		return exitFailure
	}
	fmt.Fprintf(stdout, "Logged in to %s as token %s.\n", me.Server, me.Token)
	return 0
}

func runLogout(stdout, stderr io.Writer) int {
	home, err := os.UserHomeDir()
	if err == nil {
		err = server.Remove(home)
	}
	if err != nil {
		fmt.Fprintf(stderr, "houston logout: %v\n", err)
		return exitFailure
	}
	fmt.Fprintln(stdout, "Logged out: the saved server and token are gone.")
	return 0
}

func readLine(r *bufio.Reader) string {
	line, _ := r.ReadString('\n')
	return strings.TrimSpace(line)
}
