package serverupdate_test

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/scttymn/gantry/db"

	"github.com/scttymn/houston/mission_control/app/models"
	"github.com/scttymn/houston/mission_control/app/services/dockercmd"
	"github.com/scttymn/houston/mission_control/app/services/dockercmd/dockercmdtest"
	"github.com/scttymn/houston/mission_control/app/services/owncontainer"
	"github.com/scttymn/houston/mission_control/app/services/serverupdate"
	"github.com/scttymn/houston/mission_control/test"
)

const (
	runner = "ghcr.io/scttymn/houston-runner:v0.4.2"
	labels = `{"com.docker.compose.project":"houston","com.docker.compose.project.config_files":"/srv/houston/compose.yml","com.docker.compose.project.working_dir":"/srv/houston"}`
)

var labelArgs = []string{"inspect", "--format", "{{json .Config.Labels}}", "mc"}

// updater is a v0.4.2 server, installed by the installer, with v0.4.3 out.
func updater(t *testing.T) (serverupdate.Updater, *dockercmdtest.Fake, *bytes.Buffer) {
	t.Helper()
	d := test.DB(t)
	if _, err := d.Write.Exec(`INSERT INTO installations (id, base_domain, latest_release) VALUES (1, 'svnmns.com', 'v0.4.3')`); err != nil {
		t.Fatal(err)
	}
	fake := &dockercmdtest.Fake{}
	logs := &bytes.Buffer{}
	return serverupdate.Updater{DB: d, Docker: fake, Own: owncontainer.Own{Docker: fake, Hostname: "mc"}, Version: "v0.4.2", Repo: "scttymn/houston",
		RunnerImage: runner, Runners: "3", Log: slog.New(slog.NewTextHandler(logs, nil))}, fake, logs
}

func helperRuns(fake *dockercmdtest.Fake) [][]string {
	var runs [][]string
	for _, c := range fake.Calls() {
		if c.Args[0] == "run" {
			runs = append(runs, c.Args)
		}
	}
	return runs
}

func count(t *testing.T, d *db.DB) int {
	t.Helper()
	var n int
	d.Read.QueryRow(`SELECT count(*) FROM server_updates`).Scan(&n)
	return n
}

func TestStart(t *testing.T) {
	u, fake, _ := updater(t)
	fake.On(dockercmdtest.OK(labels), labelArgs...)
	now := time.Now()
	update, err := u.Start(context.Background(), "", now)
	if err != nil {
		t.Fatal(err)
	}
	if update.ToVersion != "v0.4.3" || update.FromVersion != "v0.4.2" || update.Status != "running" || !update.StartedAt.Equal(now) {
		t.Errorf("update %+v", update)
	}
	script, _ := os.ReadFile("update-helper.sh")
	want := []string{"run", "-d", "--name", "houston-update", "--privileged", "--pid=host", "--user", "0",
		"-e", "HOUSTON_UPDATE_TO=v0.4.3", "-e", "HOUSTON_UPDATE_FROM=v0.4.2", "-e", "HOUSTON_REPO=scttymn/houston",
		"-e", "HOUSTON_DIR=/srv/houston", "-e", "HOUSTON_RUNNERS=3", "--entrypoint", "sh", runner, "-c", string(script)}
	if runs := helperRuns(fake); len(runs) != 1 || !slices.Equal(runs[0], want) {
		t.Errorf("runs %q", runs)
	}
	ran := fake.Ran()
	if i := slices.Index(ran, "rm houston-update"); i < 0 || !strings.HasPrefix(ran[i+1], "run -d") { // a finished helper is removed first
		t.Errorf("ran %q", ran)
	}

	// One at a time: refused, and the database holds the lock too.
	_, err = u.Start(context.Background(), "v0.5.0", now)
	if err == nil || err.Error() != "the update to v0.4.3 is already running" || len(helperRuns(fake)) != 1 {
		t.Errorf("a second = %v", err)
	}
	if _, err := u.DB.Write.Exec(`INSERT INTO server_updates (to_version, from_version, started_at) VALUES ('v0.5.0', 'v0.4.2', ?)`, now); !db.IsUnique(err) {
		t.Errorf("a second row: %v", err)
	}

	u.DB.Write.Exec(`DELETE FROM server_updates`)
	if _, err := u.Start(context.Background(), "v0.5.0", now); err != nil || !slices.Contains(helperRuns(fake)[1], "HOUSTON_UPDATE_TO=v0.5.0") {
		t.Errorf("a version = %v", err)
	}
}

// Refused before anything changes: no row, no helper.
func TestStartRefused(t *testing.T) {
	for _, c := range []struct {
		name, version, want string
		arrange             func(u *serverupdate.Updater, fake *dockercmdtest.Fake)
	}{
		{"not a release", "", "this server runs dev, not a release, so it updates from where it was built", func(u *serverupdate.Updater, _ *dockercmdtest.Fake) { u.Version = "dev" }},
		{"a checkout's build", "", "this server runs source abc1234, not a release, so it updates from where it was built",
			func(u *serverupdate.Updater, _ *dockercmdtest.Fake) { u.Version = "source abc1234" }},
		{"not newer", "v0.4.2", "v0.4.2 isn't newer than v0.4.2, which this server runs", nil},
		{"older", "v0.4.1", "v0.4.1 isn't newer than v0.4.2, which this server runs", nil},
		{"the latest known is this one", "", "this server already runs the latest release (v0.4.2)",
			func(u *serverupdate.Updater, _ *dockercmdtest.Fake) {
				u.DB.Write.Exec(`UPDATE installations SET latest_release = 'v0.4.2'`)
			}},
		{"the latest known is older", "", "this server already runs the latest release (v0.4.2)",
			func(u *serverupdate.Updater, _ *dockercmdtest.Fake) {
				u.DB.Write.Exec(`UPDATE installations SET latest_release = 'v0.4.1'`)
			}},
		{"a bad tag", "v0.4.3; rm -rf /", "v0.4.3; rm -rf / isn't a release (vX.Y.Z)", nil},
		{"a long bad tag", "v0.4.3" + strings.Repeat("x", 50), "v0.4.3" + strings.Repeat("x", 31) + "... isn't a release (vX.Y.Z)", nil},
		{"a prerelease", "v0.5.0-rc.1", "v0.5.0-rc.1 isn't a release (vX.Y.Z)", nil},
		{"no latest known", "", "no newer release is known yet",
			func(u *serverupdate.Updater, _ *dockercmdtest.Fake) {
				u.DB.Write.Exec(`UPDATE installations SET latest_release = ''`)
			}},
		{"not installed by the installer", "", "can't update from here: this Mission Control wasn't started by Houston's installer",
			func(_ *serverupdate.Updater, fake *dockercmdtest.Fake) { fake.On(dockercmdtest.OK("{}"), labelArgs...) }},
		{"no runner image", "", "can't update from here yet: run the installer once more (it tells Mission Control the runner image to do it with)",
			func(u *serverupdate.Updater, _ *dockercmdtest.Fake) { u.RunnerImage = "" }},
		{"the helper didn't start", "", `couldn't start the update: docker: Error response from daemon: Conflict. The container name "/houston-update" is already in use`,
			func(_ *serverupdate.Updater, fake *dockercmdtest.Fake) {
				fake.On(dockercmdtest.Fail(125, "Unable to find image\ndocker: Error response from daemon: Conflict. The container name \"/houston-update\" is already in use\n"), "run")
			}},
	} {
		u, fake, _ := updater(t)
		if c.arrange != nil {
			c.arrange(&u, fake)
		}
		fake.On(dockercmdtest.OK(labels), labelArgs...)
		_, err := u.Start(context.Background(), c.version, time.Now())
		if err == nil || err.Error() != c.want {
			t.Errorf("%s: = %v", c.name, err)
		}
		helpers := 0
		if c.name == "the helper didn't start" {
			helpers = 1
		}
		if n := count(t, u.DB); n != 0 || len(helperRuns(fake)) != helpers {
			t.Errorf("%s: %d rows, helper runs %q", c.name, n, helperRuns(fake))
		}
	}
}

// A deploy or backup underway would be cut short: refused, no row.
func TestStartBusy(t *testing.T) {
	u, fake, _ := updater(t)
	fake.On(dockercmdtest.OK(labels), labelArgs...)
	u.DB.Write.Exec(`INSERT INTO storage_locations (id, name, kind) VALUES (1, 'nas', 'nfs')`)
	u.DB.Write.Exec(`INSERT INTO projects (id, name, app_service, services, health, port) VALUES (1, 'equip', 'web', '["web"]', '/', 80)`)
	u.DB.Write.Exec(`INSERT INTO deploys (project_id, number, status, sha, ref, heartbeat_at) VALUES (1, 1, 'go', 'a', 'main', CURRENT_TIMESTAMP)`)
	for status, want := range map[string]string{"queued": "deploy #2 of equip is queued; update when it's done", "in_flight": "deploy #2 of equip is in flight; update when it's done"} {
		u.DB.Write.Exec(`INSERT INTO deploys (project_id, number, status, sha, ref, heartbeat_at) VALUES (1, 2, ?, 'a', 'main', CURRENT_TIMESTAMP)`, status)
		if _, err := u.Start(context.Background(), "", time.Now()); err == nil || err.Error() != want {
			t.Errorf("%s: = %v", status, err)
		}
		u.DB.Write.Exec(`DELETE FROM deploys WHERE number = 2`)
	}
	u.DB.Write.Exec(`INSERT INTO deploys (project_id, number, kind, status, sha, ref, heartbeat_at) VALUES (1, 2, 'restore', 'queued', 'a', 'main', CURRENT_TIMESTAMP)`)
	if _, err := u.Start(context.Background(), "", time.Now()); err == nil || err.Error() != "restore #2 of equip is queued; update when it's done" {
		t.Errorf("a restore: = %v", err)
	}
	u.DB.Write.Exec(`DELETE FROM deploys WHERE number = 2`)
	for _, c := range []struct{ operation, status, want string }{
		{"backup", "queued", "a backup of equip is queued; update when it's done"},
		{"backup", "running", "a backup of equip is running; update when it's done"},
		{"restore", "running", "a restore of equip is running; update when it's done"},
	} {
		u.DB.Write.Exec(`INSERT INTO backup_runs (project_id, location_id, operation, kind, reason, status, heartbeat_at) VALUES (1, 1, ?, 'auto', 'manual', ?, CURRENT_TIMESTAMP)`,
			c.operation, c.status)
		if _, err := u.Start(context.Background(), "", time.Now()); err == nil || err.Error() != c.want {
			t.Errorf("%s %s: = %v", c.operation, c.status, err)
		}
		u.DB.Write.Exec(`DELETE FROM backup_runs`)
	}
	if n := count(t, u.DB); n != 0 || len(helperRuns(fake)) != 0 {
		t.Errorf("%d rows, runs %q", n, helperRuns(fake))
	}
}

const helperLog = "==> Pulling Houston v0.4.3\n==> Starting Houston\n"

// running is an update to v0.4.3 started ago, its helper's state (its
// "<status> <exit code>", or "" when it's gone) and log.
func running(t *testing.T, u serverupdate.Updater, fake *dockercmdtest.Fake, ago time.Duration, state, log string) {
	t.Helper()
	if _, err := u.DB.Write.Exec(`INSERT INTO server_updates (to_version, from_version, started_at) VALUES ('v0.4.3', 'v0.4.2', ?)`, time.Now().Add(-ago)); err != nil {
		t.Fatal(err)
	}
	if state == "" {
		fake.On(dockercmdtest.Fail(1, "Error: No such object: houston-update"), "inspect")
	} else {
		fake.On(dockercmdtest.OK(state+"\n"), "inspect")
	}
	fake.On(dockercmdtest.OK(log), "logs", "--tail", "500", "houston-update")
}

func last(t *testing.T, u serverupdate.Updater) models.ServerUpdate {
	t.Helper()
	update, err := models.New(u.DB.Read).LastUpdate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return update
}

// Once the helper has ended, how it went is recorded, once.
func TestSettle(t *testing.T) {
	for _, c := range []struct{ state, version, status, log string }{
		{"exited 0", "v0.4.3", "go", helperLog},
		{"exited 3", "v0.4.3", "rolled_back", helperLog},
		{"exited 1", "v0.4.3", "no_go", helperLog},
		{"dead 137", "v0.4.3", "no_go", helperLog},
		{"", "v0.4.3", "no_go", "The update helper (houston-update) is gone, so how the update went is unknown."},
		// Exited 0, but this Mission Control isn't the new version.
		{"exited 0", "v0.4.2", "no_go", helperLog + "The installer finished, but Mission Control runs v0.4.2.\n"},
	} {
		u, fake, _ := updater(t)
		u.Version = c.version
		running(t, u, fake, 2*time.Minute, c.state, helperLog)
		now := time.Now()
		first, err := u.Settle(context.Background(), now)
		if err != nil || !first {
			t.Errorf("%q: = %v, %v", c.state, first, err)
		}
		if again, _ := u.Settle(context.Background(), now); again {
			t.Errorf("%q: settled twice", c.state)
		}
		if update := last(t, u); update.Status != c.status || update.Log != c.log || !update.FinishedAt.Time.Equal(now) {
			t.Errorf("%q: %+v", c.state, update)
		}
	}
}

// A running update says what it's doing: its log so far, its latest step,
// and the board refreshes when the step changes.
func TestSettleFollows(t *testing.T) {
	u, fake, logs := updater(t)
	log := "==> houston update: installing v0.4.3\n==> Docker is installed\n==> Pulling Houston v0.4.3\n"
	running(t, u, fake, 2*time.Minute, "running 0", log)
	if moved, err := u.Settle(context.Background(), time.Now()); !moved || err != nil {
		t.Errorf("= %v %v", moved, err)
	}
	if update := last(t, u); update.Step != "Pulling Houston v0.4.3" || update.Log != log || update.Status != "running" {
		t.Errorf("update %+v", update)
	}
	if moved, _ := u.Settle(context.Background(), time.Now()); moved {
		t.Error("the same step moved")
	}
	if strings.Contains(logs.String(), "has run for") {
		t.Errorf("logged %s", logs)
	}

	steps := func(log string) string {
		fake := &dockercmdtest.Fake{}
		fake.On(dockercmdtest.OK("running 0\n"), "inspect")
		fake.On(dockercmdtest.OK(log), "logs")
		u.Docker = fake
		u.Settle(context.Background(), time.Now())
		return last(t, u).Step
	}
	if got := steps(log + "Status: Downloaded newer image\n"); got != "Pulling Houston v0.4.3" || last(t, u).Log != log+"Status: Downloaded newer image\n" {
		t.Errorf("a longer log: %q", got)
	}
	if got := steps(log + "==> Writing /opt/houston/compose.yml\n==> Starting Houston\n"); got != "Starting Houston" {
		t.Errorf("step %q", got)
	}
	if got := steps("==> houston update: installing v0.4.3\n"); got != "Installing v0.4.3" {
		t.Errorf("step %q", got)
	}
	if got := steps("==> houston update: v0.4.3 didn't install; putting v0.4.2 back\n"); got != "v0.4.3 didn't install; putting v0.4.2 back" {
		t.Errorf("step %q", got)
	}
	// No log: no change.
	fake = &dockercmdtest.Fake{}
	fake.On(dockercmdtest.OK("running 0\n"), "inspect")
	fake.On(dockercmdtest.Fail(1, "Cannot connect"), "logs")
	u.Docker = fake
	u.Settle(context.Background(), time.Now())
	if update := last(t, u); update.Step != "v0.4.3 didn't install; putting v0.4.2 back" || update.Status != "running" {
		t.Errorf("update %+v", update)
	}
}

// Docker not answering changes nothing; nothing running, nothing asked.
func TestSettleNothing(t *testing.T) {
	u, fake, _ := updater(t)
	if moved, err := u.Settle(context.Background(), time.Now()); moved || err != nil || len(fake.Calls()) != 0 {
		t.Errorf("= %v %v, ran %q", moved, err, fake.Ran())
	}
	running(t, u, &dockercmdtest.Fake{}, time.Minute, "running 0", "")
	fake.On(dockercmdtest.Fail(1, "Cannot connect to the Docker daemon"), "inspect")
	u.Settle(context.Background(), time.Now())
	if update := last(t, u); update.Status != "running" || update.FinishedAt.Valid {
		t.Errorf("update %+v", update)
	}
}

// A long update says so.
func TestSettleLong(t *testing.T) {
	u, fake, logs := updater(t)
	running(t, u, fake, 21*time.Minute+time.Second, "running 0", helperLog)
	u.Settle(context.Background(), time.Now())
	if !strings.Contains(logs.String(), "the update to v0.4.3 has run for 21 minutes; see docker logs houston-update on the server") {
		t.Errorf("logged %s", logs)
	}
}

func TestStepName(t *testing.T) {
	for line, want := range map[string]string{
		"==> houston update: installing v0.4.3\n": "Installing v0.4.3",
		"==> v0.4.3 didn't install":               "v0.4.3 didn't install",
		"==> docker is up":                        "Docker is up",
		"==> " + strings.Repeat("x", 130):         "X" + strings.Repeat("x", 116) + "...",
	} {
		if got := serverupdate.StepName(line); got != want {
			t.Errorf("%q = %q", line, got)
		}
	}
}

// hooked runs on before each command.
type hooked struct {
	dockercmd.Runner
	on func(args []string)
}

func (h hooked) Run(ctx context.Context, args []string, o dockercmd.Opts) dockercmd.Result {
	h.on(args)
	return h.Runner.Run(ctx, args, o)
}

// Two checks at once record it once: the other one's result stands, and
// this one doesn't refresh the board.
func TestSettleOnce(t *testing.T) {
	u, fake, _ := updater(t)
	running(t, u, fake, time.Minute, "exited 3", helperLog)
	u.Docker = hooked{Runner: fake, on: func(args []string) {
		if args[0] == "logs" { // the other check writes while this one reads the log
			u.DB.Write.Exec(`UPDATE server_updates SET status = 'rolled_back', log = 'the other check''s', finished_at = CURRENT_TIMESTAMP`)
		}
	}}
	if changed, err := u.Settle(context.Background(), time.Now()); changed || err != nil {
		t.Errorf("= %v %v", changed, err)
	}
	if update := last(t, u); update.Log != "the other check's" {
		t.Errorf("update %+v", update)
	}
}
