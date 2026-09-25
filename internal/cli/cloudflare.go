package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/scttymn/houston/internal/server"
	"golang.org/x/term"
)

// houston cloudflare: what Cloudflare has for this server
// (docs/plans/cloudflare-settings.md).
func runCloudflare(file string, asJSON bool, stdout, stderr io.Writer) int {
	client, _, code := remote(file, "", false, stderr)
	if code != 0 {
		return code
	}
	ctx := context.Background()
	if asJSON {
		return printRaw(ctx, client, "/api/v1/cloudflare", stdout, stderr)
	}
	v, err := client.Cloudflare(ctx)
	if err != nil {
		return remoteFailed(err, stderr)
	}
	for _, p := range v.Problems {
		fmt.Fprintf(stdout, "HOLD      %s\n", p)
	}
	if t := v.Tunnel; t != nil {
		fmt.Fprintf(stdout, "tunnel    %s  %s  %s\n", t.Name, t.Status, t.ID)
		for _, c := range v.Connections {
			fmt.Fprintf(stdout, "  %-8s cloudflared %-10s %-16s since %s\n", c.Colo, c.Version, c.Origin, localTime(c.Since))
		}
	}
	if len(v.Routes)+len(v.MissingRoutes) > 0 {
		fmt.Fprintln(stdout, "routes")
		for _, r := range v.Routes {
			fmt.Fprintf(stdout, "  %s%s\n", routeLine(r), map[bool]string{true: "  DRIFT", false: ""}[r.Drift])
		}
		for _, r := range v.MissingRoutes {
			fmt.Fprintf(stdout, "  %s  MISSING\n", routeLine(r))
		}
		if v.Drift {
			fmt.Fprintln(stdout, "  (houston cloudflare repair puts them back)")
		}
	}
	fmt.Fprintln(stdout, "records")
	if len(v.Records) == 0 {
		fmt.Fprintln(stdout, "  none found")
	}
	for _, r := range v.Records {
		where := "this server"
		if !r.Here {
			where = "another server"
		}
		fmt.Fprintf(stdout, "  %-34s %-18s %s\n", r.Name, r.Project, where)
	}
	return 0
}

func routeLine(r server.CloudflareRoute) string {
	host := "everything else"
	if r.Hostname != nil {
		host = *r.Hostname
	}
	if r.Path != nil {
		host += " " + *r.Path
	}
	return fmt.Sprintf("%-34s -> %s", host, r.Service)
}

func localTime(s string) string {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return s
	}
	return t.Local().Format("Jan 2 15:04")
}

// houston cloudflare token: the new token from stdin or a hidden prompt,
// never an argument; the server checks it before saving it.
func runCloudflareToken(file string, stdin io.Reader, stdout, stderr io.Writer) int {
	client, _, code := remote(file, "", false, stderr)
	if code != 0 {
		return code
	}
	var token string
	if stdinIsTerminal() {
		fmt.Fprint(stderr, "New Cloudflare API token (hidden): ")
		secret, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(stderr)
		if err != nil {
			return remoteFailed(err, stderr)
		}
		token = string(secret)
	} else {
		data, err := io.ReadAll(io.LimitReader(stdin, 4<<10))
		if err != nil {
			return remoteFailed(err, stderr)
		}
		token = strings.TrimSpace(string(data))
	}
	res, err := client.ReplaceCloudflareToken(context.Background(), token)
	if err != nil {
		return remoteFailed(err, stderr)
	}
	for _, c := range res.Checks {
		fmt.Fprintf(stdout, "%-6s %s\n", map[bool]string{true: "GO", false: "NO-GO"}[c.OK], c.Label)
	}
	if !res.Replaced {
		fmt.Fprintln(stdout, "The old token is still in use.")
		return exitFailure
	}
	fmt.Fprintln(stdout, "The new token is in use.")
	return 0
}

// houston cloudflare repair: exit 1 when anything couldn't be put back.
func runCloudflareRepair(file string, stdout, stderr io.Writer) int {
	client, _, code := remote(file, "", false, stderr)
	if code != 0 {
		return code
	}
	results, err := client.RepairCloudflare(context.Background())
	if err != nil {
		return remoteFailed(err, stderr)
	}
	failed := false
	for _, r := range results {
		line := fmt.Sprintf("%-34s %s", r.Item, r.State)
		if r.Reason != "" {
			line += ": " + r.Reason
		}
		fmt.Fprintln(stdout, line)
		failed = failed || (r.State != "OK" && r.State != "DNS OK")
	}
	if failed {
		return exitFailure
	}
	return 0
}
