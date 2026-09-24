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
		`^2026-09-21 11:20 UTC +auto +created by hand +a07b2d1 +385 MB +11111111$`,
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

func TestVolumes(t *testing.T) {
	var body string
	remoteServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"/api/v1/projects/equip/volumes": respond(`{"volumes":[{"name":"storage","path":"/rails/storage","location":null,"placed":false},{"name":"media","path":"/media","location":"unas-nfs","placed":true}]}`),
		"/api/v1/projects/equip/volumes/storage": func(w http.ResponseWriter, r *http.Request) {
			body = readBody(r)
			w.Write([]byte(`{"name":"storage","path":"/rails/storage","location":"unas-nfs","placed":false}`))
		},
		"/api/v1/projects/equip/volumes/media": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusConflict)
			w.Write([]byte(`{"error":"media is already on unas-nfs; moving a volume is a later feature"}`))
		},
	})
	t.Chdir(t.TempDir())

	code, out, _ := run(&fakeDocker{}, "volumes", "--project", "equip")
	if code != 0 || !regexp.MustCompile(`(?m)^storage +/rails/storage +local disk +not yet placed$`).MatchString(out) ||
		!regexp.MustCompile(`(?m)^media +/media +unas-nfs +placed$`).MatchString(out) {
		t.Errorf("list: exit %d\n%s", code, out)
	}
	code, out, _ = run(&fakeDocker{}, "volumes", "place", "storage", "unas-nfs", "--project", "equip")
	if code != 0 || body != `{"location":"unas-nfs"}` || !strings.Contains(out, "storage will be placed on unas-nfs") {
		t.Errorf("place: exit %d, body %s\n%s", code, body, out)
	}
	code, _, _ = run(&fakeDocker{}, "volumes", "place", "storage", "--local-disk", "--project", "equip")
	if code != 0 || body != `{"location":null}` {
		t.Errorf("--local-disk: exit %d, body %s", code, body)
	}
	code, _, errOut := run(&fakeDocker{}, "volumes", "place", "media", "--local-disk", "--project", "equip")
	if code != 1 || !strings.Contains(errOut, "moving a volume is a later feature") {
		t.Errorf("placed: exit %d, %q", code, errOut)
	}
	code, _, errOut = run(&fakeDocker{}, "volumes", "place", "storage", "--project", "equip")
	if code != 2 || !strings.Contains(errOut, "a location, or --local-disk") {
		t.Errorf("no location: exit %d, %q", code, errOut)
	}
}

func TestStorage(t *testing.T) {
	var body string
	remoteServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"/api/v1/storage": respond(`{"locations":[{"name":"b2-offsite","kind":"b2","where":"b2:sm:houston","live":false,"default":false,"confirmed":true,"used_by":0},
			{"name":"unas-nfs","kind":"nfs","where":"10.0.1.20:/volume1/houston","live":true,"default":true,"confirmed":true,"used_by":3,"prune_error":"Fatal: unable to create lock"}]}`),
		"/api/v1/storage/b2-offsite": func(w http.ResponseWriter, r *http.Request) {
			body = readBody(r)
			w.Write([]byte(`{"name":"b2-offsite","default":true}`))
		},
		"/api/v1/projects/equip/backup_target": func(w http.ResponseWriter, r *http.Request) {
			body = readBody(r)
			w.Write([]byte(`{"backup_location":"b2-offsite"}`))
		},
	})
	t.Chdir(t.TempDir())

	code, out, _ := run(&fakeDocker{}, "storage")
	if code != 0 || !regexp.MustCompile(`(?m)^unas-nfs +nfs +10\.0\.1\.20:/volume1/houston +live volumes · backups +DEFAULT +3 projects +prune failed: Fatal: unable to create lock$`).MatchString(out) ||
		!regexp.MustCompile(`(?m)^b2-offsite +b2 +b2:sm:houston +backups +0 projects$`).MatchString(out) {
		t.Errorf("list: exit %d\n%s", code, out)
	}
	code, out, _ = run(&fakeDocker{}, "storage", "default", "b2-offsite")
	if code != 0 || body != `{"default":true}` || !strings.Contains(out, "b2-offsite is the default") {
		t.Errorf("default: exit %d, body %s\n%s", code, body, out)
	}
	code, out, _ = run(&fakeDocker{}, "storage", "use", "b2-offsite", "--project", "equip")
	if code != 0 || body != `{"location":"b2-offsite"}` || !strings.Contains(out, "equip backs up to b2-offsite") {
		t.Errorf("use: exit %d, body %s\n%s", code, body, out)
	}
	code, _, _ = run(&fakeDocker{}, "storage", "use", "--default", "--project", "equip")
	if code != 0 || body != `{"location":null}` {
		t.Errorf("use --default: exit %d, body %s", code, body)
	}
}

func TestMaintenance(t *testing.T) {
	var body string
	on := false
	remoteServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"/api/v1/projects/equip": func(w http.ResponseWriter, r *http.Request) {
			m := `{"on":false}`
			if on {
				m = `{"on":true,"since":"2026-09-23T10:00:00Z","by":"token agent","message":"Back soon"}`
			}
			w.Write([]byte(strings.Replace(garageJSON, `"name":"garage"`, `"maintenance":`+m+`,"name":"equip"`, 1)))
		},
		"/api/v1/projects/equip/maintenance": func(w http.ResponseWriter, r *http.Request) {
			body = readBody(r)
			if strings.Contains(body, "refuse") {
				w.WriteHeader(http.StatusBadGateway)
				w.Write([]byte(`{"error":"Cloudflare said no: Tunnel configuration is invalid"}`))
				return
			}
			on = strings.Contains(body, `"on":true`)
			if on {
				w.Write([]byte(`{"on":true,"since":"2026-09-23T10:00:00Z","by":"token agent","message":"Back soon"}`))
			} else {
				w.Write([]byte(`{"on":false}`))
			}
		},
	})
	t.Chdir(t.TempDir())

	code, out, _ := run(&fakeDocker{}, "maintenance", "--project", "equip")
	if code != 0 || !strings.Contains(out, "equip: no maintenance page") {
		t.Errorf("show off: exit %d\n%s", code, out)
	}
	code, out, _ = run(&fakeDocker{}, "maintenance", "on", "--message", "Back soon", "--project", "equip")
	if code != 0 || body != `{"message":"Back soon","on":true}` || !strings.Contains(out, "equip shows a maintenance page (since 2026-09-23 10:00 UTC, token agent): Back soon") {
		t.Errorf("on: exit %d, body %s\n%s", code, body, out)
	}
	code, out, _ = run(&fakeDocker{}, "status", "--project", "equip")
	if code != 0 || !strings.Contains(out, "maintenance ON since 2026-09-23 10:00 UTC (token agent): Back soon\n") {
		t.Errorf("status: exit %d\n%s", code, out)
	}
	code, out, _ = run(&fakeDocker{}, "maintenance", "off", "--project", "equip")
	if code != 0 || body != `{"on":false}` || !strings.Contains(out, "equip: no maintenance page") {
		t.Errorf("off: exit %d, body %s\n%s", code, body, out)
	}
	code, _, errOut := run(&fakeDocker{}, "maintenance", "on", "--message", "refuse", "--project", "equip")
	if code != 1 || !strings.Contains(errOut, "Tunnel configuration is invalid") {
		t.Errorf("refused: exit %d, %q", code, errOut)
	}
	code, _, errOut = run(&fakeDocker{}, "maintenance", "sideways", "--project", "equip")
	if code != 2 || !strings.Contains(errOut, "on or off") {
		t.Errorf("bad arg: exit %d, %q", code, errOut)
	}
}

func TestRestore(t *testing.T) {
	followEvery = time.Millisecond
	t.Cleanup(func() { followEvery = 2 * time.Second })
	var body string
	var polls atomic.Int32
	remoteServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"/api/v1/projects/equip/restores": func(w http.ResponseWriter, r *http.Request) {
			body = readBody(r)
			if strings.Contains(body, `"confirm":"nope"`) {
				w.WriteHeader(http.StatusUnprocessableEntity)
				w.Write([]byte(`{"error":"type equip to confirm"}`))
				return
			}
			w.WriteHeader(http.StatusAccepted)
			w.Write([]byte(`{"number":12,"status":"queued","kind":"restore","sha":"d4e0b17000000000000000000000000000000000","ref":"restore:33333333"}`))
		},
		"/api/v1/projects/equip/deploys/12": func(w http.ResponseWriter, r *http.Request) {
			if polls.Add(1) < 2 {
				w.Write([]byte(`{"number":12,"status":"in_flight","kind":"restore","sha":"d4e0b17000000000000000000000000000000000","step":"Restore data","log":"restoring\n","log_next":10}`))
				return
			}
			w.Write([]byte(`{"number":12,"status":"go","kind":"restore","sha":"d4e0b17000000000000000000000000000000000","log":"","log_next":10}`))
		},
	})
	t.Chdir(t.TempDir())

	code, out, errOut := run(&fakeDocker{}, "restore", "33333333", "--confirm", "equip", "--location", "unas-nfs", "--project", "equip", "--follow")
	if code != 0 || body != `{"confirm":"equip","location":"unas-nfs","snapshot":"33333333"}` ||
		!strings.Contains(out, "Queued restore #12 of equip to snapshot 33333333 (d4e0b17)") || !strings.Contains(out, "GO: equip #12 serving d4e0b17") {
		t.Errorf("restore --follow: exit %d, body %s\n%s%s", code, body, out, errOut)
	}
	code, _, errOut = run(&fakeDocker{}, "restore", "33333333", "--confirm", "nope", "--project", "equip")
	if code != 1 || !strings.Contains(errOut, "type equip to confirm") {
		t.Errorf("refused: exit %d, %q", code, errOut)
	}
	code, _, errOut = run(&fakeDocker{}, "restore", "33333333", "--project", "equip")
	if code != 2 || !strings.Contains(errOut, "--confirm equip") {
		t.Errorf("no --confirm: exit %d, %q", code, errOut)
	}
}
