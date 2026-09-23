package cli

import (
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const snapshotsJSON = `{"snapshots":[
	{"id":"` + "3333333300000000000000000000000000000000000000000000000000000000" + `","short_id":"33333333","time":"2026-09-22T12:31:00Z","kind":"deploy","reason":"deploy","deploy":7,"sha":"d4e0b17000000000000000000000000000000000","bytes":5},
	{"id":"` + "1111111100000000000000000000000000000000000000000000000000000000" + `","short_id":"11111111","time":"2026-09-21T11:20:00Z","kind":"auto","reason":"manual","sha":"a07b2d1000000000000000000000000000000000","bytes":404000000}]}`

func TestSnapshots(t *testing.T) {
	remoteServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"/api/v1/projects/equip/snapshots": respond(snapshotsJSON),
	})
	t.Chdir(t.TempDir())

	code, out, errOut := run(&fakeDocker{}, "snapshots", "--project", "equip")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if code != 0 || len(lines) != 2 {
		t.Fatalf("exit %d\n%s%s", code, out, errOut)
	}
	for i, want := range []string{
		`^2026-09-22 12:31 UTC +deploy +before deploy #7 +d4e0b17 +5 B +33333333$`,
		`^2026-09-21 11:20 UTC +auto +Back up now +a07b2d1 +385 MB +11111111$`,
	} {
		if !regexp.MustCompile(want).MatchString(lines[i]) {
			t.Errorf("line %d = %q, want %s", i, lines[i], want)
		}
	}

	code, out, _ = run(&fakeDocker{}, "snapshots", "--project", "equip", "--json")
	if code != 0 || !strings.Contains(out, `"short_id":"33333333"`) {
		t.Errorf("--json: exit %d\n%s", code, out)
	}

	code, _, errOut = run(&fakeDocker{}, "snapshots", "--project", "nope")
	if code != 1 || !strings.Contains(errOut, "no project nope") {
		t.Errorf("unknown project: exit %d, %q", code, errOut)
	}
}

func TestBackup(t *testing.T) {
	followEvery = time.Millisecond
	t.Cleanup(func() { followEvery = 2 * time.Second })
	var polls atomic.Int32
	finals := map[string]string{
		"go":      `{"id":12,"status":"go","snapshot_id":"5c5edd4c00000000000000000000000000000000000000000000000000000000","bytes":410000000,"error":"1 file couldn't be read: /data/x"}`,
		"no_go":   `{"id":12,"status":"no_go","error":"restic backup failed (exit 1): Fatal: unable to open repository"}`,
		"skipped": `{"id":12,"status":"skipped","error":"nothing to back up (no named volumes, no Postgres)"}`,
	}
	final := "go"
	remoteServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"/api/v1/projects/equip/backups": func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			w.WriteHeader(http.StatusAccepted)
			polls.Store(0)
			w.Write([]byte(`{"id":12,"status":"queued","kind":"auto","reason":"manual"}`))
		},
		"/api/v1/projects/equip/backups/12": func(w http.ResponseWriter, r *http.Request) {
			switch polls.Add(1) {
			case 1:
				w.Write([]byte(`{"id":12,"status":"queued"}`))
			case 2:
				w.Write([]byte(`{"id":12,"status":"running"}`))
			default:
				w.Write([]byte(finals[final]))
			}
		},
		"/api/v1/projects/fresh/backups": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnprocessableEntity)
			w.Write([]byte(`{"error":"nothing deployed yet"}`))
		},
	})
	t.Chdir(t.TempDir())

	code, out, errOut := run(&fakeDocker{}, "backup", "--project", "equip")
	if code != 0 || !strings.Contains(out, "Queued a backup of equip (run 12).") || polls.Load() != 0 {
		t.Errorf("without --follow: exit %d, %d polls\n%s%s", code, polls.Load(), out, errOut)
	}

	code, out, errOut = run(&fakeDocker{}, "backup", "--project", "equip", "--follow")
	if code != 0 || !strings.Contains(out, "GO: equip backed up: snapshot 5c5edd4c, 391 MB") || !strings.Contains(out, "1 file couldn't be read: /data/x") {
		t.Errorf("follow to GO: exit %d\n%s%s", code, out, errOut)
	}

	final = "no_go"
	code, out, errOut = run(&fakeDocker{}, "backup", "--project", "equip", "--follow")
	if code != 1 || !strings.Contains(out, "NO-GO: equip backup 12: restic backup failed (exit 1): Fatal: unable to open repository") {
		t.Errorf("follow to NO-GO: exit %d\n%s%s", code, out, errOut)
	}

	final = "skipped"
	code, out, _ = run(&fakeDocker{}, "backup", "--project", "equip", "--follow")
	if code != 0 || !strings.Contains(out, "Nothing to back up: nothing to back up (no named volumes, no Postgres)") {
		t.Errorf("follow to skipped: exit %d\n%s", code, out)
	}

	code, _, errOut = run(&fakeDocker{}, "backup", "--project", "fresh")
	if code != 1 || !strings.Contains(errOut, "nothing deployed yet") {
		t.Errorf("refused: exit %d, %q", code, errOut)
	}
}

func TestStatusShowsTheLastBackup(t *testing.T) {
	withBackup := strings.Replace(garageJSON, `"last_deploy":`, `"backup_schedule":"daily 03:00","time_zone":"Europe/Berlin","last_deploy":`, 1)
	withBackup = strings.Replace(withBackup, `"last_deploy":`, `"last_backup":{"id":3,"status":"go","snapshot_id":"5c5edd4c00000000000000000000000000000000000000000000000000000000","bytes":410000000,"finished_at":"2026-09-22T03:00:41Z"},"last_deploy":`, 1)
	failed := strings.Replace(garageJSON, `"last_deploy":`, `"last_backup":{"id":4,"status":"no_go","error":"restic backup failed"},"last_deploy":`, 1)
	body := withBackup
	remoteServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"/api/v1/projects/garage": func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) },
	})
	t.Chdir(t.TempDir())

	for _, c := range []struct{ body, want string }{
		{withBackup, "backup    GO 2026-09-22 03:00 UTC · 5c5edd4c · 391 MB"},
		{withBackup, "schedule  daily 03:00 (Europe/Berlin)"},
		{failed, "backup    NO-GO: restic backup failed"},
		{garageJSON, "backup    none yet"},
	} {
		body = c.body
		code, out, _ := run(&fakeDocker{}, "status", "--project", "garage")
		if code != 0 || !strings.Contains(out, c.want+"\n") {
			t.Errorf("want %q in\n%s", c.want, out)
		}
	}
}

func TestSettings(t *testing.T) {
	var patched string
	remoteServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"/api/v1/settings": func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Method == http.MethodPatch && strings.Contains(readBody(r), "Mars/Olympus"):
				w.WriteHeader(http.StatusUnprocessableEntity)
				w.Write([]byte(`{"error":"no time zone Mars/Olympus (use an IANA name, like Europe/Berlin)"}`))
			case r.Method == http.MethodPatch:
				patched = "yes"
				w.Write([]byte(`{"base_domain":"svnmns.com","time_zone":"Europe/Berlin"}`))
			default:
				w.Write([]byte(`{"base_domain":"svnmns.com","time_zone":"UTC"}`))
			}
		},
	})
	t.Chdir(t.TempDir())

	code, out, _ := run(&fakeDocker{}, "settings")
	if code != 0 || !strings.Contains(out, "base domain  svnmns.com\n") || !strings.Contains(out, "time zone    UTC\n") || patched != "" {
		t.Errorf("show: exit %d\n%s", code, out)
	}
	code, out, _ = run(&fakeDocker{}, "settings", "--time-zone", "Europe/Berlin")
	if code != 0 || !strings.Contains(out, "time zone    Europe/Berlin\n") || patched != "yes" {
		t.Errorf("set: exit %d\n%s", code, out)
	}
	code, _, errOut := run(&fakeDocker{}, "settings", "--time-zone", "Mars/Olympus")
	if code != 1 || !strings.Contains(errOut, "no time zone Mars/Olympus") {
		t.Errorf("unknown zone: exit %d, %q", code, errOut)
	}
}

func readBody(r *http.Request) string {
	b, _ := io.ReadAll(r.Body)
	return string(b)
}
