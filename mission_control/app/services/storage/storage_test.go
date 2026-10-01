package storage_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/scttymn/gantry/crypt"

	"github.com/scttymn/houston/mission_control/app/services/backup"
	"github.com/scttymn/houston/mission_control/app/services/dockercmd/dockercmdtest"
	"github.com/scttymn/houston/mission_control/app/services/storage"
	"github.com/scttymn/houston/mission_control/test"
)

func keys(t *testing.T) {
	k, _ := crypt.ParseKeys(crypt.NewKey())
	crypt.Use(k...)
	t.Cleanup(func() { crypt.Use() })
}

var nfs = storage.Setup{Kind: "nfs", Name: "unas", NFSServer: "10.0.1.20", NFSExport: "/volume1/houston"}

// An NFS share gets its Docker volume first; one that can't be made stops
// there.
func TestSaveNFS(t *testing.T) {
	keys(t)
	d := test.DB(t)
	fake := &dockercmdtest.Fake{}
	fake.On(dockercmdtest.Fail(1, "Error: no such volume"), "volume", "inspect")
	fake.On(dockercmdtest.Fail(1, "Error response from daemon: mount failed\n"), "volume", "create")
	saved, err := nfs.Save(context.Background(), d, fake, time.Now())
	if err != nil || saved.Failed != "Couldn't create the NFS volume: Error response from daemon: mount failed" {
		t.Fatalf("= %+v %v", saved, err)
	}
	ok := &dockercmdtest.Fake{}
	ok.On(dockercmdtest.Fail(1, "Error: no such volume"), "volume", "inspect")
	saved, err = nfs.Save(context.Background(), d, ok, time.Now())
	if err != nil || !saved.OK() || !saved.Location.VerifiedAt.Valid {
		t.Fatalf("= %+v %v", saved, err)
	}
	ran := strings.Join(ok.Ran(), "\n")
	if !strings.Contains(ran, "volume create --driver local --opt type=nfs --opt o=addr=10.0.1.20,rw,nfsvers=4 --opt device=:/volume1/houston houston-storage-unas") ||
		!strings.Contains(ran, "-v houston-storage-unas:/repo") || !strings.Contains(ran, " init") {
		t.Errorf("ran %s", ran)
	}
	there := &dockercmdtest.Fake{} // the volume's there: not made again
	nfs.Save(context.Background(), d, there, time.Now())
	if strings.Contains(strings.Join(there.Ran(), "\n"), "volume create") {
		t.Error("made the volume again")
	}
}

// A repository already there opens with the location's password, or it's
// someone else's.
func TestSaveExistingRepository(t *testing.T) {
	keys(t)
	d := test.DB(t)
	local := storage.Setup{Kind: "local", Name: "disk2", LocalPath: "/srv/backups"}
	fake := &dockercmdtest.Fake{}
	fake.On(dockercmdtest.Fail(1, "Fatal: config file already exists"), "run", "--rm", "-e", "RESTIC_PASSWORD", "-e", "RESTIC_REPOSITORY", "-v", "houston-restic-cache:/root/.cache/restic", "-v", "/srv/backups:/repo", backup.ResticImage, "init")
	saved, err := local.Save(context.Background(), d, fake, time.Now())
	if err != nil || !saved.OK() {
		t.Fatalf("opened = %+v %v", saved, err)
	}
	if ran := strings.Join(fake.Ran(), "\n"); !strings.Contains(ran, " cat config") {
		t.Errorf("ran %s", ran)
	}
	d.Write.Exec(`UPDATE storage_locations SET verified_at = NULL`)
	fake = &dockercmdtest.Fake{}
	fake.On(dockercmdtest.Fail(1, "Fatal: repository already initialized"), "run")
	saved, _ = local.Save(context.Background(), d, fake, time.Now())
	if !strings.HasPrefix(saved.Failed, "There's already a restic repository at /srv/backups, and Houston's password doesn't open it (restic: Fatal: repository already initialized).") {
		t.Errorf("= %+v", saved)
	}
}

func TestProblems(t *testing.T) {
	for _, c := range []struct {
		setup storage.Setup
		want  string
	}{
		{storage.Setup{Kind: "ftp", Name: "x"}, "kind must be nfs, local, s3 or b2"},
		{storage.Setup{Kind: "nfs", Name: "unas", NFSServer: "bad host!", NFSExport: "volume1"}, "nfs_server must be a host name or IP address; nfs_export must be an absolute path like /volume1/houston"},
		{storage.Setup{Kind: "local", Name: "d", LocalPath: "/srv/../etc"}, "local_path must be an absolute path on the server"},
		{storage.Setup{Kind: "s3", Name: "s", S3Bucket: "B", S3Endpoint: "http://x"}, "s3_bucket must be a bucket name; s3_access_key_id can't be blank; s3_secret_access_key can't be blank; s3_endpoint must be an https:// endpoint"},
		{storage.Setup{Kind: "b2", Name: "b", B2Bucket: "ok-bucket"}, "b2_key_id can't be blank; b2_application_key can't be blank"},
		{storage.Setup{Kind: "s3", Name: "s", S3Bucket: "ok-bucket", S3AccessKeyID: "a", S3SecretAccess: "b", S3Endpoint: "https://s3.example.com/"}, ""},
		{storage.Setup{Kind: "nfs", Name: "unas", NFSServer: "10.0.1.20", NFSExport: "/v"}, ""},
	} {
		var got []string
		for _, p := range c.setup.Problems() {
			got = append(got, p.Field+" "+p.Message)
		}
		if strings.Join(got, "; ") != c.want {
			t.Errorf("%+v = %q", c.setup, got)
		}
	}
	if p := storage.Password(); len(p) != 40 || strings.ContainsAny(p, "0OIl+/") {
		t.Errorf("password %q", p)
	}
}
