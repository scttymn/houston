package cli

import (
	"context"
	"fmt"
	"io"
	"time"
)

// runDelete deletes the project from the server (docs/plans/delete-project.md)
// once its name is typed with --confirm; asking again resumes a deletion that
// stopped partway. With follow, it waits for the result: exit 0 on GO, 1 on NO-GO.
func runDelete(file, projectFlag, confirm string, deleteBackups, follow bool, stdout, stderr io.Writer) int {
	client, name, code := remote(file, projectFlag, true, stderr)
	if code != 0 {
		return code
	}
	if confirm == "" {
		fmt.Fprintf(stderr, "houston delete removes %s and its data from the server: add --confirm %s\n", name, name)
		return exitUsage
	}
	ctx := context.Background()
	d, err := client.Delete(ctx, name, confirm, deleteBackups)
	if err != nil {
		return remoteFailed(err, stderr)
	}
	backups := "its backups are kept, with a final snapshot"
	if d.DeleteBackups {
		backups = "its backups go too"
	}
	fmt.Fprintf(stdout, "Deleting %s (deletion %d); %s.\n", name, d.ID, backups)
	if !follow {
		fmt.Fprintf(stdout, "houston status --project %s says how it goes, until it's gone.\n", name)
		return 0
	}
	step := ""
	for {
		d, err = client.Deletion(ctx, d.ID)
		if err != nil {
			return remoteFailed(err, stderr)
		}
		if d.Step != "" && d.Step != step && d.Status != "go" {
			step = d.Step
			fmt.Fprintf(stdout, "  %s\n", step)
		}
		switch d.Status {
		case "go":
			fmt.Fprintf(stdout, "GO: %s is deleted.", name)
			if d.SnapshotID != "" {
				fmt.Fprintf(stdout, " Its final snapshot %s is kept in %s.", short8(d.SnapshotID), d.SnapshotLocation)
			}
			fmt.Fprintln(stdout)
			if d.RepoURL != "" {
				fmt.Fprintf(stdout, "Remove the deploy key and the webhook from %s: Houston can't.\n", d.RepoURL)
			}
			return 0
		case "no_go":
			fmt.Fprintf(stdout, "NO-GO: deleting %s %s\n", name, d.Error)
			if d.Step != "check" && d.Step != "snapshot" {
				fmt.Fprintln(stdout, "It serves nothing new meanwhile; run this again to finish.")
			}
			return exitFailure
		}
		time.Sleep(followEvery)
	}
}
