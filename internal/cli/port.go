package cli

import (
	"context"
	"fmt"
	"io"
)

// houston port [open|close]: Settings › Port 3000
// (docs/plans/security-fixes.md, H3). Mission Control restarts for a few
// seconds to change it; the installer keeps the choice.
func runPort(file, change string, asJSON bool, stdout, stderr io.Writer) int {
	client, _, code := remote(file, "", false, stderr)
	if code != 0 {
		return code
	}
	ctx := context.Background()
	switch change {
	case "":
	case "open", "close":
		v, err := client.SetPort(ctx, change == "open")
		if err != nil {
			return remoteFailed(err, stderr)
		}
		fmt.Fprintln(stdout, v.Message)
		return 0
	default:
		fmt.Fprintf(stderr, "houston port: %q isn't open or close\n", change)
		return exitUsage
	}
	if asJSON {
		return printRaw(ctx, client, "/api/v1/port", stdout, stderr)
	}
	v, err := client.Port(ctx)
	if err != nil {
		return remoteFailed(err, stderr)
	}
	switch {
	case v.Address == "":
		fmt.Fprintln(stdout, "Houston can't tell what port 3000 is bound to right now.")
	case !v.Open:
		fmt.Fprintf(stdout, "CLOSED  port 3000 answers only on the server (%s).\n        Reach it with: ssh -L 3000:127.0.0.1:3000 <you>@<server>, then http://localhost:3000\n        (houston port open opens it to the network)\n", v.Address)
	case v.Address == "0.0.0.0":
		fmt.Fprintln(stdout, "OPEN    port 3000 is open to your network, over plain HTTP.\n        (houston port close closes it; do, on a server with a public address)")
	default:
		fmt.Fprintf(stdout, "OPEN    port 3000 is open on %s, over plain HTTP.\n        (houston port close closes it)\n", v.Address)
	}
	return 0
}
