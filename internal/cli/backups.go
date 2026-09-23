package cli

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/sevenmoons/houston/internal/humanize"
	"github.com/sevenmoons/houston/internal/server"
)

// runSnapshots lists a project's snapshots on the server, newest first.
func runSnapshots(file, projectFlag string, asJSON bool, stdout, stderr io.Writer) int {
	client, name, code := remote(file, projectFlag, true, stderr)
	if code != 0 {
		return code
	}
	ctx := context.Background()
	if asJSON {
		return printRaw(ctx, client, "/api/v1/projects/"+name+"/snapshots", stdout, stderr)
	}
	snapshots, err := client.Snapshots(ctx, name)
	if err != nil {
		return remoteFailed(err, stderr)
	}
	if len(snapshots) == 0 {
		fmt.Fprintf(stdout, "No snapshots of %s yet.\n", name)
	}
	for _, s := range snapshots {
		fmt.Fprintf(stdout, "%s  %-6s  %-18s  %s  %-8s  %s\n", utc(s.Time), s.Kind, snapshotNote(s), short(s.SHA), humanize.Bytes(s.Bytes), s.ShortID)
	}
	return 0
}

func snapshotNote(s server.Snapshot) string {
	switch {
	case s.Reason == "manual":
		return "Back up now"
	case s.Reason == "schedule":
		return "daily"
	case s.Reason == "restore":
		return "before a restore"
	case s.Deploy > 0:
		return "before deploy #" + strconv.Itoa(s.Deploy)
	}
	return s.Reason
}

// runBackup queues a backup (Back up now); with follow, waits for its
// result: exit 0 on GO or nothing to back up, 1 on NO-GO.
func runBackup(file, projectFlag string, follow bool, stdout, stderr io.Writer) int {
	client, name, code := remote(file, projectFlag, true, stderr)
	if code != 0 {
		return code
	}
	ctx := context.Background()
	b, err := client.BackupNow(ctx, name)
	if err != nil {
		return remoteFailed(err, stderr)
	}
	fmt.Fprintf(stdout, "Queued a backup of %s (run %d).\n", name, b.ID)
	if !follow {
		return 0
	}
	for {
		b, err = client.Backup(ctx, name, strconv.Itoa(b.ID))
		if err != nil {
			return remoteFailed(err, stderr)
		}
		switch b.Status {
		case "go":
			fmt.Fprintf(stdout, "GO: %s backed up: snapshot %s, %s\n", name, short8(b.SnapshotID), humanize.Bytes(b.Bytes))
			if b.Error != "" {
				fmt.Fprintf(stdout, "warning: %s\n", b.Error)
			}
			return 0
		case "skipped":
			fmt.Fprintf(stdout, "Nothing to back up: %s\n", b.Error)
			return 0
		case "no_go":
			fmt.Fprintf(stdout, "NO-GO: %s backup %d: %s\n", name, b.ID, b.Error)
			return exitFailure
		}
		time.Sleep(followEvery)
	}
}

// backupLine is houston status's summary of a project's last backup.
func backupLine(b *server.Backup) string {
	switch {
	case b == nil:
		return "none yet"
	case b.Status == "go":
		when := "—"
		if b.FinishedAt != nil {
			when = utc(*b.FinishedAt)
		}
		return fmt.Sprintf("GO %s · %s · %s", when, short8(b.SnapshotID), humanize.Bytes(b.Bytes))
	case b.Status == "no_go":
		return "NO-GO: " + b.Error
	case b.Status == "skipped":
		return "nothing to back up"
	}
	return "running"
}

func utc(t time.Time) string { return t.UTC().Format("2006-01-02 15:04") + " UTC" }

func short8(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// runSettings shows Houston's settings; with a time zone, sets it first.
func runSettings(timeZone string, stdout, stderr io.Writer) int {
	client, _, code := remote("", "", false, stderr)
	if code != 0 {
		return code
	}
	ctx := context.Background()
	var s server.Settings
	var err error
	if timeZone != "" {
		s, err = client.SetTimeZone(ctx, timeZone)
	} else {
		s, err = client.Settings(ctx)
	}
	if err != nil {
		return remoteFailed(err, stderr)
	}
	fmt.Fprintf(stdout, "base domain  %s\ntime zone    %s\n", s.BaseDomain, s.TimeZone)
	return 0
}

// runVolumes lists a project's volumes and where they live.
func runVolumes(file, projectFlag string, stdout, stderr io.Writer) int {
	client, name, code := remote(file, projectFlag, true, stderr)
	if code != 0 {
		return code
	}
	volumes, err := client.Volumes(context.Background(), name)
	if err != nil {
		return remoteFailed(err, stderr)
	}
	for _, v := range volumes {
		where, placed := v.Location, "placed"
		if where == "" {
			where = "local disk"
		}
		if !v.Placed {
			placed = "not yet placed"
		}
		fmt.Fprintf(stdout, "%-16s %-24s %-16s %s\n", v.Name, v.Path, where, placed)
	}
	return 0
}

// runVolumePlace chooses where a volume lives, until it's made.
func runVolumePlace(file, projectFlag string, args []string, localDisk bool, stdout, stderr io.Writer) int {
	if (len(args) == 2) == localDisk {
		fmt.Fprintln(stderr, "houston volumes place VOLUME LOCATION: give a location, or --local-disk")
		return exitUsage
	}
	client, name, code := remote(file, projectFlag, true, stderr)
	if code != 0 {
		return code
	}
	location := ""
	if !localDisk {
		location = args[1]
	}
	v, err := client.PlaceVolume(context.Background(), name, args[0], location)
	if err != nil {
		return remoteFailed(err, stderr)
	}
	where := v.Location
	if where == "" {
		where = "local disk"
	}
	fmt.Fprintf(stdout, "%s will be placed on %s at the next deploy.\n", v.Name, where)
	return 0
}
