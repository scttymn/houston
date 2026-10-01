package api_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/scttymn/gantry/crypt"

	"github.com/scttymn/houston/mission_control/app"
	"github.com/scttymn/houston/mission_control/app/services/backup"
	"github.com/scttymn/houston/mission_control/app/services/dockercmd"
	"github.com/scttymn/houston/mission_control/app/services/dockercmd/dockercmdtest"
)

// withSnapshots is shop with nas as its backup storage, and the docker
// that lists its snapshots.
func withSnapshots(t *testing.T) (*app.App, http.Handler, *dockercmdtest.Fake) {
	t.Helper()
	a, h := remote(t)
	exec(t, a, `INSERT INTO storage_locations (id, name, kind, settings, restic_password, is_default, acknowledged_at) VALUES
		(1, 'nas', 'nfs', '{"server":"10.0.1.20","export":"/volume1/houston"}', ?, TRUE, CURRENT_TIMESTAMP),
		(2, 'offsite', 's3', '{"bucket":"b"}', ?, FALSE, CURRENT_TIMESTAMP)`, crypt.Of("pw"), crypt.Of("pw2"))
	fake := a.DockerCLI.(*dockercmdtest.Fake)
	return a, h, fake
}

var listArgs = []string{"run", "--rm", "-e", "RESTIC_PASSWORD", "-e", "RESTIC_REPOSITORY", "-v", "houston-restic-cache:/root/.cache/restic",
	"-v", "houston-storage-nas:/repo", backup.ResticImage, "snapshots", "--json", "--host", "houston", "--tag", "project:shop"}

const listed = `[{"id":"aaaa1111","short_id":"aaaa","time":"2026-09-29T03:00:12.345Z","tags":["project:shop","kind:auto","reason:schedule","sha:abc"],"summary":{"total_bytes_processed":10}},
	{"id":"bbbb2222","time":"2026-09-30T10:00:00Z","tags":["project:shop","kind:deploy","reason:deploy","deploy:7","sha:def"]}]`

// A project's snapshots, newest first, read from restic and kept a while.
func TestV1Snapshots(t *testing.T) {
	_, h, fake := withSnapshots(t)
	fake.On(dockercmdtest.OK(listed), listArgs...)
	is(t, v1(h, "GET", "/projects/shop/snapshots", personal, "", nil), 200, `{"snapshots":[
		{"id":"bbbb2222","short_id":"bbbb2222","time":"2026-09-30T10:00:00Z","kind":"deploy","reason":"deploy","deploy":7,"sha":"def","bytes":null},
		{"id":"aaaa1111","short_id":"aaaa","time":"2026-09-29T03:00:12Z","kind":"auto","reason":"schedule","deploy":null,"sha":"abc","bytes":10}]}`)
	v1(h, "GET", "/projects/shop/snapshots", personal, "", nil)
	if n := len(fake.Calls()); n != 1 {
		t.Errorf("listed %d times", n)
	}
	for _, c := range fake.Calls() {
		if c.Env["RESTIC_PASSWORD"] != "pw" || strings.Contains(c.String(), "pw") {
			t.Errorf("the password: %v %s", c.Env, c)
		}
	}
}

func TestV1SnapshotsRefuse(t *testing.T) {
	a, h := remote(t)
	is(t, v1(h, "GET", "/projects/shop/snapshots", personal, "", nil), 409, `{"error":"no backup storage yet (finish setup's storage step)"}`)
	exec(t, a, `INSERT INTO storage_locations (id, name, kind, settings, restic_password, is_default, acknowledged_at) VALUES (1, 'nas', 'nfs', '{}', ?, TRUE, CURRENT_TIMESTAMP)`, crypt.Of("pw"))
	a.DockerCLI.(*dockercmdtest.Fake).On(dockercmdtest.Fail(1, "Fatal: wrong password or no key found\n"), "run", "--rm")
	is(t, v1(h, "GET", "/projects/shop/snapshots", personal, "", nil), 502, `{"error":"can't read snapshots: Fatal: wrong password or no key found"}`)
}

// A snapshot's download: the zip streamed from restic, its name saying
// what it is; every refusal before the first byte.
func TestV1DownloadSnapshot(t *testing.T) {
	_, h, fake := withSnapshots(t)
	fake.On(dockercmdtest.OK(listed), listArgs...)
	fake.OnDownload("PK\x03\x04first", " and the rest", nil, "run", "--rm", "--name", "houston-export")
	w := v1(h, "GET", "/projects/shop/snapshots/aaaa/download", personal, "", nil)
	if w.Code != 200 || w.Body.String() != "PK\x03\x04first and the rest" || w.Header().Get("Content-Type") != "application/zip" ||
		w.Header().Get("Content-Disposition") != `attachment; filename="shop-20260929-0300Z-aaaa.zip"; filename*=UTF-8''shop-20260929-0300Z-aaaa.zip` ||
		w.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("= %d %q %v", w.Code, w.Body.String(), w.Header())
	}
	calls := fake.Calls()
	dump := calls[len(calls)-1].String()
	if !strings.HasPrefix(dump, "run --rm --name houston-export --label houston.export=") ||
		!strings.HasSuffix(dump, backup.ResticImage+" dump --quiet --retry-lock 30s --archive zip aaaa1111 /") {
		t.Errorf("dump %s", dump)
	}

	is(t, v1(h, "GET", "/projects/shop/snapshots/zzzz/download", personal, "", nil), 404, `{"error":"snapshot zzzz isn't in nas's snapshots of shop"}`)
	is(t, v1(h, "GET", "/projects/shop/snapshots/aaaa/download?location=offsite", personal, "", nil), 404, `{"error":"shop never backed up to offsite"}`)

	// Another download, alive: this one waits its turn.
	inUse := dockercmd.Result{Code: 125, Output: `docker: Error response from daemon: Conflict. The container name "/houston-export" is already in use by container "abc".`}
	fake.RefuseDownload(inUse, "run", "--rm", "--name", "houston-export")
	fake.On(dockercmdtest.OK("abc "+time.Now().UTC().Format(time.RFC3339Nano)+" another"), "inspect")
	is(t, v1(h, "GET", "/projects/shop/snapshots/aaaa/download", personal, "", nil), 409,
		`{"error":"another download started at `+time.Now().UTC().Format("15:04")+` UTC is still running; try again when it's done"}`)
}

// A download left behind past its deadline is removed; what isn't a zip,
// or a busy repository, is refused.
func TestV1DownloadSnapshotTroubles(t *testing.T) {
	_, h, fake := withSnapshots(t)
	fake.On(dockercmdtest.OK(listed), listArgs...)
	fake.On(dockercmdtest.OK("old123 "+time.Now().Add(-4*time.Hour).UTC().Format(time.RFC3339Nano)+" stale"), "inspect")
	fake.RefuseDownload(dockercmd.Result{Code: 125, Output: `The container name "/houston-export" is already in use`}, "run", "--rm", "--name", "houston-export")
	fake.OnDownload("PK\x03\x04", "zip", nil, "run", "--rm", "--name", "houston-export")
	if w := v1(h, "GET", "/projects/shop/snapshots/aaaa/download", personal, "", nil); w.Code != 200 || !strings.Contains(strings.Join(fake.Ran(), "\n"), "rm -f old123") {
		t.Errorf("past a stale one: %d, ran %v", w.Code, fake.Ran())
	}

	fake.OnDownload("<html>oops</html>", "", nil, "run", "--rm", "--name", "houston-export")
	is(t, v1(h, "GET", "/projects/shop/snapshots/aaaa/download", personal, "", nil), 502, `{"error":"restic didn't send a zip: <html>oops</html>"}`)
	fake.RefuseDownload(dockercmd.Result{Code: 11, Output: "repository is already locked"}, "run", "--rm", "--name", "houston-export")
	is(t, v1(h, "GET", "/projects/shop/snapshots/aaaa/download", personal, "", nil), 502, `{"error":"the repository is busy (pruning?); try again in a few minutes"}`)
	fake.RefuseDownload(dockercmd.Result{Code: 1, Output: "Fatal: no such snapshot\n"}, "run", "--rm", "--name", "houston-export")
	is(t, v1(h, "GET", "/projects/shop/snapshots/aaaa/download", personal, "", nil), 502, `{"error":"restic couldn't read the snapshot (exit 1): Fatal: no such snapshot"}`)
}
