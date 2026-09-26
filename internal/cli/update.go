package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/scttymn/houston/internal/server"
)

// How often houston update asks how it's going, and for how long. The
// server's helper gives each install 20 minutes, and the old version's
// reinstall 20 more.
var (
	updatePoll = 5 * time.Second
	updateWait = 45 * time.Minute
)

// houston update [vX.Y.Z]: update the server to a release from anywhere
// (docs/plans/update-from-mission-control.md), then follow it. Mission
// Control restarts on the way, so for a while it doesn't answer.
func runUpdate(file, version string, stdout, stderr io.Writer) int {
	client, _, code := remote(file, "", false, stderr)
	if code != 0 {
		return code
	}
	ctx := context.Background()
	v, err := client.StartUpdate(ctx, version)
	if err != nil {
		return remoteFailed(err, stderr)
	}
	fmt.Fprintln(stdout, v.Message)
	id := v.Update.ID

	quiet, step := false, ""
	for deadline := time.Now().Add(updateWait); time.Now().Before(deadline); time.Sleep(updatePoll) {
		v, err := client.Update(ctx)
		if errors.Is(err, server.ErrRefused) {
			return remoteFailed(err, stderr)
		}
		if err != nil {
			if !quiet {
				fmt.Fprintln(stderr, "houston: Mission Control isn't answering (it restarts during the update); still waiting")
				quiet = true
			}
			continue
		}
		u := v.Update
		if u == nil || u.ID != id {
			continue
		}
		if u.Step != "" && u.Step != step {
			fmt.Fprintf(stdout, "  %s\n", u.Step)
			step = u.Step
		}
		if u.Status == "running" {
			continue
		}
		switch u.Status {
		case "go":
			fmt.Fprintf(stdout, "GO  the server runs %s\n", u.To)
			return 0
		case "rolled_back":
			fmt.Fprintf(stdout, "NO-GO  the update to %s failed; the server went back to %s\n", u.To, u.From)
		default:
			fmt.Fprintf(stdout, "NO-GO  the update to %s failed\n", u.To)
		}
		if log := strings.TrimRight(u.Log, "\n"); log != "" {
			fmt.Fprintln(stdout, log)
		}
		return exitFailure
	}
	fmt.Fprintf(stderr, "houston: the update hasn't finished after %s; see the flight board, or docker logs houston-update on the server\n", updateWait)
	return exitFailure
}

// houston update --check: the server asks GitHub for the latest release now,
// as Check now on Houston's page does.
func runUpdateCheck(file string, stdout, stderr io.Writer) int {
	client, _, code := remote(file, "", false, stderr)
	if code != 0 {
		return code
	}
	v, err := client.CheckUpdate(context.Background())
	if err != nil {
		return remoteFailed(err, stderr)
	}
	if v.Latest != "" {
		fmt.Fprintf(stdout, "%s houston update installs it.\n", v.Message)
		return 0
	}
	fmt.Fprintln(stdout, v.Message)
	return 0
}
