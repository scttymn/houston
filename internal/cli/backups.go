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

// runStorage lists the storage locations (never a password or credential).
func runStorage(stdout, stderr io.Writer) int {
	client, _, code := remote("", "", false, stderr)
	if code != 0 {
		return code
	}
	locations, err := client.Storage(context.Background())
	if err != nil {
		return remoteFailed(err, stderr)
	}
	for _, l := range locations {
		holds, marks := "backups", ""
		if l.Live {
			holds = "live volumes · backups"
		}
		if l.Default {
			marks += "  DEFAULT"
		}
		if !l.Confirmed {
			marks += "  not confirmed (save its password in Settings › Storage)"
		}
		line := fmt.Sprintf("%-16s %-6s %-32s %-24s%s  %d projects", l.Name, l.Kind, l.Where, holds, marks, l.UsedBy)
		if l.PruneError != "" {
			line += "  prune failed: " + l.PruneError
		}
		fmt.Fprintln(stdout, line)
	}
	return 0
}

func runStorageDefault(name string, stdout, stderr io.Writer) int {
	client, _, code := remote("", "", false, stderr)
	if code != 0 {
		return code
	}
	if err := client.MakeDefault(context.Background(), name); err != nil {
		return remoteFailed(err, stderr)
	}
	fmt.Fprintf(stdout, "%s is the default: projects without their own target back up there.\n", name)
	return 0
}

// runStorageUse sets where a project backs up: a location, or the default.
func runStorageUse(file, projectFlag string, args []string, useDefault bool, stdout, stderr io.Writer) int {
	if (len(args) == 1) == useDefault {
		fmt.Fprintln(stderr, "houston storage use LOCATION: give a location, or --default")
		return exitUsage
	}
	client, name, code := remote(file, projectFlag, true, stderr)
	if code != 0 {
		return code
	}
	location := ""
	if !useDefault {
		location = args[0]
	}
	where, err := client.UseStorage(context.Background(), name, location)
	if err != nil {
		return remoteFailed(err, stderr)
	}
	fmt.Fprintf(stdout, "%s backs up to %s.\n", name, where)
	return 0
}

// runMaintenance shows a project's maintenance page state; "on" or "off"
// sets it (Houston's page, on every hostname of the project).
func runMaintenance(file, projectFlag string, args []string, message string, stdout, stderr io.Writer) int {
	if len(args) > 1 || (len(args) == 1 && args[0] != "on" && args[0] != "off") {
		fmt.Fprintln(stderr, "houston maintenance [on|off]: on or off, or nothing to see it")
		return exitUsage
	}
	client, name, code := remote(file, projectFlag, true, stderr)
	if code != 0 {
		return code
	}
	ctx := context.Background()
	var m server.Maintenance
	if len(args) == 1 {
		var err error
		if m, err = client.SetMaintenance(ctx, name, args[0] == "on", message); err != nil {
			return remoteFailed(err, stderr)
		}
	} else {
		p, err := client.Project(ctx, name)
		if err != nil {
			return remoteFailed(err, stderr)
		}
		m = p.Maintenance
	}
	if !m.On {
		fmt.Fprintf(stdout, "%s: no maintenance page.\n", name)
		return 0
	}
	line := fmt.Sprintf("%s shows a maintenance page (since %s, %s)", name, maintenanceSince(m), m.By)
	if m.Message != "" {
		line += ": " + m.Message
	}
	fmt.Fprintln(stdout, line+". It stays up until you turn it off.")
	return 0
}

func maintenanceSince(m server.Maintenance) string {
	if m.Since == nil {
		return "—"
	}
	return utc(*m.Since)
}

// runRestore queues a restore of the project to a snapshot, code and data
// together; with follow, follows it like deploys show --follow.
func runRestore(file, projectFlag, snapshot, location, confirm string, follow bool, stdout, stderr io.Writer) int {
	client, name, code := remote(file, projectFlag, true, stderr)
	if code != 0 {
		return code
	}
	if confirm == "" {
		fmt.Fprintf(stderr, "houston restore replaces %s's data with the snapshot's: add --confirm %s\n", name, name)
		return exitUsage
	}
	d, err := client.Restore(context.Background(), name, snapshot, location, confirm)
	if err != nil {
		return remoteFailed(err, stderr)
	}
	fmt.Fprintf(stdout, "Queued restore #%d of %s to snapshot %s (%s).\n", d.Number, name, snapshot, short(d.SHA))
	if !follow {
		return 0
	}
	return runDeployShow(file, name, strconv.Itoa(d.Number), true, false, stdout, stderr)
}
