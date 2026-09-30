package backup

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/scttymn/houston/mission-control-go/app/models"
	"github.com/scttymn/houston/mission-control-go/app/services/dockercmd"
)

// An export's refusals, all before its first byte.
type (
	NotFound string
	Busy     string
	Failed   string
)

func (e NotFound) Error() string { return string(e) }
func (e Busy) Error() string     { return string(e) }
func (e Failed) Error() string   { return string(e) }

const (
	exportContainer = "houston-export"
	// exportLabel carries each download's own token, so its cleanup
	// removes only its own container: by then the name may be the next's.
	exportLabel    = "houston.export"
	exportDeadline = 3 * time.Hour
	// exportStale: a container older than this outlived its deadline, and
	// nothing will stop it.
	exportStale = exportDeadline + 10*time.Minute
	zipMagic    = "PK\x03\x04"
)

// Export is one download of a snapshot: everything it holds (the volumes'
// files under data/, the databases and houston.json under out/) as a zip,
// streamed out of restic as it's read. One runs at a time, server-wide:
// the restic container's name is the lock, and Docker takes it atomically.
// Only the project's own listed snapshots can be downloaded; repositories
// are shared between projects.
type Export struct {
	Filename string
	docker   dockercmd.Downloader
	download *dockercmd.Download
	token    string
	log      *slog.Logger
	what     string
}

// OpenExport starts the download of snapshot (its id or short id) of p
// from the location named location.
func OpenExport(ctx context.Context, q *models.Queries, docker dockercmd.Downloader, snapshots *Snapshots, log *slog.Logger,
	p models.Project, location, snapshot, by string) (*Export, error) {
	locations, err := LocationsFor(ctx, q, p)
	if err != nil {
		return nil, err
	}
	var l *models.StorageLocation
	for i := range locations {
		if locations[i].Name == location {
			l = &locations[i]
		}
	}
	if l == nil {
		named := location
		if named == "" {
			named = "that location"
		}
		return nil, NotFound(fmt.Sprintf("%s never backed up to %s", p.Name, named))
	}
	list, err := snapshots.For(ctx, p.Name, *l)
	var unavailable Unavailable
	if errors.As(err, &unavailable) {
		return nil, Failed(fmt.Sprintf("can't read %s's snapshots: %s", l.Name, unavailable))
	}
	if err != nil {
		return nil, err
	}
	var found *Snapshot
	for i := range list {
		if snapshot != "" && (list[i].ID == snapshot || list[i].ShortID == snapshot) {
			found = &list[i]
			break
		}
	}
	if found == nil {
		return nil, NotFound(fmt.Sprintf("snapshot %s isn't in %s's snapshots of %s", snapshot, l.Name, p.Name))
	}
	token := make([]byte, 16)
	rand.Read(token)
	e := &Export{Filename: fmt.Sprintf("%s-%sZ-%s.zip", p.Name, found.Time.UTC().Format("20060102-1504"), found.ShortID),
		docker: docker, token: hex.EncodeToString(token), log: log,
		what: fmt.Sprintf("download: %s %s %s from %s", by, p.Name, found.ShortID, l.Name)}
	return e, e.start(ctx, *l, found.ID)
}

func (e *Export) start(ctx context.Context, l models.StorageLocation, id string) error {
	env := ResticEnv(l)
	args := ResticArgs(l, env, []string{"dump", "--quiet", "--retry-lock", "30s", "--archive", "zip", id, "/"}, exportContainer, nil)
	args = append(args[:4:4], append([]string{"--label", exportLabel + "=" + e.token}, args[4:]...)...) // after --name
	dump := func() (*dockercmd.Download, dockercmd.Result) {
		return e.docker.Download(ctx, args, dockercmd.Opts{Env: env, Timeout: exportDeadline})
	}
	d, r := dump()
	if d == nil && inUse(r) && e.staleRemoved(ctx) {
		d, r = dump()
	}
	if d == nil {
		return e.refuse(ctx, r)
	}
	e.download = d
	if !strings.HasPrefix(string(d.First), zipMagic) {
		e.stop(ctx)
		first := string(d.First[:min(200, len(d.First))])
		return Failed("restic didn't send a zip: " + strings.TrimSpace(strings.ToValidUTF8(first, "")))
	}
	e.log.Info(e.what + ": started")
	return nil
}

func inUse(r dockercmd.Result) bool {
	return r.Code == 125 && strings.Contains(r.Output, "is already in use")
}

// holder is the container holding the export's name now: its id, when it
// started, and its token; ok is false when there's none.
func (e *Export) holder(ctx context.Context) (id string, started time.Time, token string, ok bool) {
	r := e.docker.Run(ctx, []string{"inspect", "-f", `{{.Id}} {{.Created}} {{index .Config.Labels "` + exportLabel + `"}}`, exportContainer}, dockercmd.Opts{})
	if !r.OK {
		return "", time.Time{}, "", false
	}
	fields := strings.SplitN(strings.TrimSpace(r.Output), " ", 3)
	if len(fields) < 2 {
		return "", time.Time{}, "", false
	}
	started, err := time.Parse(time.RFC3339Nano, fields[1])
	if err != nil {
		return "", time.Time{}, "", false
	}
	if len(fields) == 3 {
		token = fields[2]
	}
	return fields[0], started, token, true
}

// staleRemoved removes the container holding the name, by its id, if it
// started too long ago to be alive and well: by name, a download that took
// the name since would be hit. None there: the name is free.
func (e *Export) staleRemoved(ctx context.Context) bool {
	id, started, _, ok := e.holder(ctx)
	if !ok {
		return true
	}
	if time.Since(started) < exportStale {
		return false
	}
	e.docker.Run(ctx, []string{"rm", "-f", id}, dockercmd.Opts{})
	return true
}

func (e *Export) refuse(ctx context.Context, r dockercmd.Result) error {
	if inUse(r) {
		since := ""
		if _, started, _, ok := e.holder(ctx); ok {
			since = " started at " + started.UTC().Format("15:04 UTC")
		}
		return Busy(fmt.Sprintf("another download%s is still running; try again when it's done", since))
	}
	if r.Code == 11 {
		return Failed("the repository is busy (pruning?); try again in a few minutes")
	}
	out := strings.TrimSpace(lastLines(r.Output, 5))
	if len(out) > 500 {
		out = out[:497] + "..."
	}
	return Failed(fmt.Sprintf("restic couldn't read the snapshot (exit %d): %s", r.Code, out))
}

// Headers are the response's: a zip to save, never cached, never buffered
// by a proxy.
func (e *Export) Headers(h http.Header) {
	h.Set("Content-Type", "application/zip")
	h.Set("Content-Disposition", `attachment; filename="`+e.Filename+`"; filename*=UTF-8''`+e.Filename)
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
}

// Send writes the zip to w as restic reads it. A client that goes away
// (ctx) stops it; its container is removed if it's still this one's.
func (e *Export) Send(ctx context.Context, w io.Writer) error {
	sent, err := w.Write(e.download.First)
	written := int64(sent)
	if err == nil {
		var n int64
		n, err = io.Copy(w, e.download.Rest)
		written += n
	}
	if err != nil || ctx.Err() != nil {
		e.stop(context.WithoutCancel(ctx))
		e.log.Info(fmt.Sprintf("%s: stopped by the client after %d bytes", e.what, written))
		return err
	}
	if err := e.download.Wait(); err != nil {
		e.log.Warn(fmt.Sprintf("%s: broke after %d bytes: %v", e.what, written, err))
		return err
	}
	e.log.Info(fmt.Sprintf("%s: finished, %d bytes", e.what, written))
	return nil
}

// stop stops the docker CLI, then removes this download's container if
// it's still there (stopping the CLI doesn't stop the container).
func (e *Export) stop(ctx context.Context) {
	e.download.Close()
	if id, _, token, ok := e.holder(ctx); ok && token == e.token {
		e.docker.Run(ctx, []string{"rm", "-f", id}, dockercmd.Opts{})
	}
}
