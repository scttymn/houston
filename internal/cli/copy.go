package cli

import (
	"context"
	"fmt"
	"io"
	"strconv"
)

// runCopy copies a project to the new name its compose.yml gives
// (docs/plans/copy-project.md): once its deploy held with that name, --confirm
// with the old name copies it; --cancel stops a copy before its handover;
// --undo gives the hosts back and deletes the copy.
func runCopy(file, projectFlag, confirm string, cancel, undo, follow bool, stdout, stderr io.Writer) int {
	ctx := context.Background()
	switch {
	case cancel:
		client, name, code := remote(file, projectFlag, true, stderr)
		if code != 0 {
			return code
		}
		c, err := client.CancelCopy(ctx, name)
		if err != nil {
			return remoteFailed(err, stderr)
		}
		fmt.Fprintf(stdout, "Cancelled the copy of %s to %s; %s goes, and %s keeps serving.\n", c.From, c.To, c.To, c.From)
		return 0
	case undo:
		client, name, code := remote(file, projectFlag, true, stderr)
		if code != 0 {
			return code
		}
		if confirm == "" {
			fmt.Fprintf(stderr, "houston copy --undo gives the hosts back and deletes %s: add --confirm %s\n", name, name)
			return exitUsage
		}
		c, d, err := client.UndoCopy(ctx, name, confirm)
		if err != nil {
			return remoteFailed(err, stderr)
		}
		fmt.Fprintf(stdout, "The hosts are %s's again; %s is being deleted (deletion %d).\n", c.From, name, d.ID)
		return 0
	}

	if confirm == "" {
		fmt.Fprintln(stderr, "houston copy copies a project to the name its compose.yml now gives: add --confirm <old project's name>")
		return exitUsage
	}
	project := projectFlag
	if project == "" {
		project = confirm
	}
	client, name, code := remote(file, project, true, stderr)
	if code != 0 {
		return code
	}
	c, err := client.StartCopy(ctx, name, confirm)
	if err != nil {
		return remoteFailed(err, stderr)
	}
	fmt.Fprintf(stdout, "Copying %s to %s: deploy #%d of %s.\n", c.From, c.To, c.Deploy, c.To)
	if !follow {
		return 0
	}
	return runDeployShow(file, c.To, strconv.Itoa(c.Deploy), true, false, stdout, stderr)
}
