// Package serverupdate updates this server to a Houston release, started
// from Mission Control (the Rails app's ServerUpdate), so it doesn't take
// SSH on the server's network. The helper container houston-update runs
// the release's installer on the host (update-helper.sh) and, if that
// fails, installs the version the server ran again. Settle records how it
// went.
//
// One runs at a time: the running row is the lock (a partial unique
// index). While it runs, runners get no deploys and backups wait: the
// installer recreates the runners, which would cut one short.
package serverupdate

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/scttymn/gantry/db"

	"github.com/scttymn/houston/mission-control-go/app/models"
	"github.com/scttymn/houston/mission-control-go/app/services/dockercmd"
	"github.com/scttymn/houston/mission-control-go/app/services/owncontainer"
)

//go:embed update-helper.sh
var script string

const (
	// Helper is the helper container's name.
	Helper = "houston-update"
	// Long is when an update is taking long: the helper gives each install
	// 20 minutes, so it ends within about 40.
	Long = 20 * time.Minute
	// logLines is how much of the helper's log is kept: plenty for an
	// installer run, bounded.
	logLines = "500"
)

// Refused is an update that can't start now, in words.
type Refused string

func (r Refused) Error() string { return string(r) }

// Updater starts and settles updates.
type Updater struct {
	DB     *db.DB
	Docker dockercmd.Runner
	Own    owncontainer.Own
	// Version is the Houston this Mission Control is (app.HoustonVersion).
	Version string
	// Repo is Houston's on GitHub; RunnerImage (HOUSTON_RUNNER_IMAGE) runs
	// the helper; Runners (HOUSTON_RUNNERS) is how many the installer keeps.
	Repo, RunnerImage, Runners string
	Log                        *slog.Logger
}

// Start starts the update to version ("": the latest release known).
// Refused, with nothing saved and no helper, when it can't run now. The
// caller follows it (the job, every few seconds).
func (u Updater) Start(ctx context.Context, version string, now time.Time) (models.ServerUpdate, error) {
	from := u.Version
	if !models.IsRelease(from) {
		return models.ServerUpdate{}, Refused("this server runs " + from + ", not a release, so it updates from where it was built")
	}
	q := models.New(u.DB.Read)
	to := version
	if to == "" {
		inst, err := q.CurrentInstallation(ctx)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return models.ServerUpdate{}, err
		}
		to = inst.LatestRelease
	}
	switch {
	case to == "":
		return models.ServerUpdate{}, Refused("no newer release is known yet")
	case !models.IsRelease(to):
		return models.ServerUpdate{}, Refused(truncate(to, 40) + " isn't a release (vX.Y.Z)")
	case models.NewerRelease(from, to) == "" && version == "":
		return models.ServerUpdate{}, Refused("this server already runs the latest release (" + from + ")")
	case models.NewerRelease(from, to) == "":
		return models.ServerUpdate{}, Refused(to + " isn't newer than " + from + ", which this server runs")
	}
	if current, err := q.RunningUpdate(ctx); err == nil {
		return models.ServerUpdate{}, Refused("the update to " + current.ToVersion + " is already running")
	} else if !errors.Is(err, sql.ErrNoRows) {
		return models.ServerUpdate{}, err
	}
	_, dir := u.Own.ComposeFile(ctx)
	if dir == "" {
		return models.ServerUpdate{}, Refused("can't update from here: this Mission Control wasn't started by Houston's installer")
	}
	if u.RunnerImage == "" {
		return models.ServerUpdate{}, Refused("can't update from here yet: run the installer once more (it tells Mission Control the runner image to do it with)")
	}

	update, err := models.New(u.DB.Write).StartUpdate(ctx, models.StartUpdateParams{ToVersion: to, FromVersion: from, StartedAt: now})
	if db.IsUnique(err) {
		return update, Refused("another update was just started")
	}
	if err != nil {
		return update, err
	}
	// Saved first, then checked: once the row is saved no deploy or backup
	// can be claimed, so one claimed before it is caught here.
	if err := u.run(ctx, to, from, dir); err != nil {
		if deleteErr := models.New(u.DB.Write).DeleteUpdate(context.WithoutCancel(ctx), update.ID); deleteErr != nil {
			return update, deleteErr
		}
		return update, err
	}
	u.Log.Info("updating", "to", to, "from", from, "helper", Helper)
	return update, nil
}

// run checks nothing's underway, then starts the helper.
func (u Updater) run(ctx context.Context, to, from, dir string) error {
	q := models.New(u.DB.Read)
	if d, err := q.FirstBusyDeploy(ctx); err == nil {
		what := "deploy"
		if d.Kind == "restore" {
			what = "restore"
		}
		return Refused(fmt.Sprintf("%s #%d of %s is %s; update when it's done", what, d.Number, d.Project, strings.ReplaceAll(d.Status, "_", " ")))
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if r, err := q.FirstBusyRun(ctx); err == nil {
		what := "a backup"
		if r.Operation == "restore" {
			what = "a restore"
		}
		return Refused(fmt.Sprintf("%s of %s is %s; update when it's done", what, r.Project, r.Status))
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	u.Docker.Run(ctx, []string{"rm", Helper}, dockercmd.Opts{Timeout: 10 * time.Second}) // a finished one; a running one stays, and the run below is refused
	ran := u.Docker.Run(ctx, []string{"run", "-d", "--name", Helper, "--privileged", "--pid=host", "--user", "0",
		"-e", "HOUSTON_UPDATE_TO=" + to, "-e", "HOUSTON_UPDATE_FROM=" + from, "-e", "HOUSTON_REPO=" + u.Repo,
		"-e", "HOUSTON_DIR=" + dir, "-e", "HOUSTON_RUNNERS=" + u.Runners,
		"--entrypoint", "sh", u.RunnerImage, "-c", script}, dockercmd.Opts{Timeout: 30 * time.Second})
	if !ran.OK {
		lines := strings.Split(strings.TrimSpace(ran.Output), "\n")
		return Refused("couldn't start the update: " + strings.TrimSpace(lines[len(lines)-1]))
	}
	return nil
}

// Settle records how the running update went, once its helper has ended,
// and follows it meanwhile (its log so far, its latest step). Docker not
// answering changes nothing: the next look tries again. changed is whether
// the flight board should refresh: a new step, or the update done.
func (u Updater) Settle(ctx context.Context, now time.Time) (changed bool, err error) {
	update, err := models.New(u.DB.Read).RunningUpdate(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	state := u.Docker.Run(ctx, []string{"inspect", "--format", "{{.State.Status}} {{.State.ExitCode}}", Helper}, dockercmd.Opts{Timeout: 10 * time.Second})
	var result, log string
	switch {
	case state.OK:
		fields := strings.Fields(state.Output)
		if len(fields) < 2 || (fields[0] != "exited" && fields[0] != "dead") {
			moved, err := u.follow(ctx, update, now)
			if update.StartedAt.Before(now.Add(-Long)) {
				u.Log.Warn(fmt.Sprintf("the update to %s has run for %d minutes; see docker logs %s on the server",
					update.ToVersion, int(now.Sub(update.StartedAt).Minutes()), Helper))
			}
			return moved, err
		}
		var code int
		fmt.Sscan(fields[1], &code)
		logs := u.Docker.Run(ctx, []string{"logs", "--tail", logLines, Helper}, dockercmd.Opts{Timeout: 10 * time.Second})
		result, log = u.outcome(update, code, logs.Output)
	case noSuchContainer.MatchString(state.Output):
		result, log = "no_go", "The update helper ("+Helper+") is gone, so how the update went is unknown."
	default:
		u.Log.Warn("couldn't check the update", "to", update.ToVersion, "err", strings.TrimSpace(state.Output))
		return false, nil
	}
	written, err := models.New(u.DB.Write).SettleUpdate(ctx, models.SettleUpdateParams{Status: result, Log: log,
		Now: sql.NullTime{Time: now, Valid: true}, ID: update.ID})
	if err != nil || written != 1 {
		return false, err
	}
	if result == "go" {
		u.Log.Info("the update: "+result, "to", update.ToVersion)
	} else {
		u.Log.Warn("the update: "+result, "to", update.ToVersion)
	}
	return true, nil
}

var noSuchContainer = regexp.MustCompile(`(?i)no such (object|container)`)

// follow saves what the running update is doing: its log so far and the
// installer's latest step (a "==> " line); moved is a new step.
func (u Updater) follow(ctx context.Context, update models.ServerUpdate, now time.Time) (moved bool, err error) {
	ran := u.Docker.Run(ctx, []string{"logs", "--tail", logLines, Helper}, dockercmd.Opts{Timeout: 10 * time.Second})
	if !ran.OK {
		return false, nil
	}
	step := update.Step
	lines := strings.Split(ran.Output, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.HasPrefix(lines[i], "==> ") {
			step = StepName(lines[i])
			break
		}
	}
	moved = step != update.Step
	if !moved && ran.Output == update.Log {
		return false, nil
	}
	return moved, models.New(u.DB.Write).FollowUpdate(ctx, models.FollowUpdateParams{Step: step, Log: ran.Output, UpdatedAt: now, ID: update.ID})
}

// outcome is how it went: the helper exits 0 when it installed the new
// version (and this Mission Control, restarted, is it), 3 when it failed
// and put the old one back.
func (u Updater) outcome(update models.ServerUpdate, code int, log string) (string, string) {
	switch code {
	case 0:
		if u.Version == update.ToVersion {
			return "go", log
		}
		return "no_go", log + "The installer finished, but Mission Control runs " + u.Version + ".\n"
	case 3:
		return "rolled_back", log
	}
	return "no_go", log
}

// StepName is an installer's "==> " line as a step: capitalized, short.
func StepName(line string) string {
	s := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(line, "==> "), "houston update: "))
	if len(s) > 0 && s[0] >= 'a' && s[0] <= 'z' && (len(s) == 1 || s[1] < '0' || s[1] > '9') {
		s = strings.ToUpper(s[:1]) + s[1:]
	}
	return truncate(s, 120)
}

// truncate is Rails' String#truncate: at most n characters, "..." at the end
// of a cut one.
func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n-3]) + "..."
}
