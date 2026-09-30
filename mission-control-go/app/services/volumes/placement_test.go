package volumes_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/scttymn/gantry/db"

	"github.com/scttymn/houston/mission-control-go/app/models"
	"github.com/scttymn/houston/mission-control-go/app/services/volumes"
	"github.com/scttymn/houston/mission-control-go/test"
	"github.com/scttymn/houston/mission-control-go/test/fakedocker"
)

// setUp is a project shop with volume data, and two storage locations: nas
// (NFS) and disk (a folder).
func setUp(t *testing.T) (*db.DB, models.Project) {
	t.Helper()
	d := test.DB(t)
	for _, q := range []string{
		`INSERT INTO storage_locations (id, name, kind, settings, acknowledged_at) VALUES (1, 'nas', 'nfs', '{"server":"10.0.1.20","export":"/volume1/houston"}', CURRENT_TIMESTAMP)`,
		`INSERT INTO storage_locations (id, name, kind, settings, acknowledged_at) VALUES (2, 'disk', 'local', '{"path":"/srv/houston"}', CURRENT_TIMESTAMP)`,
		`INSERT INTO projects (id, name, app_service, services, volumes, health, port) VALUES (1, 'shop', 'web', '["web"]', '[{"name":"data","path":"/rails/storage"}]', '/up', 3000)`,
	} {
		if _, err := d.Write.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	p, err := models.New(d.Read).ProjectByName(context.Background(), "shop")
	if err != nil {
		t.Fatal(err)
	}
	return d, p
}

func chosen(t *testing.T, d *db.DB, location int) {
	t.Helper()
	if _, err := d.Write.Exec(`INSERT INTO project_volumes (project_id, name, location_id) VALUES (1, 'data', ?)`, location); err != nil {
		t.Fatal(err)
	}
}

func placedAt(t *testing.T, d *db.DB) bool {
	var placed bool
	d.Read.QueryRow(`SELECT placed_at IS NOT NULL FROM project_volumes WHERE name = 'data'`).Scan(&placed)
	return placed
}

// On local disk: a plain Docker volume, made when missing.
func TestPlaceOnLocalDisk(t *testing.T) {
	d, p := setUp(t)
	docker := &fakedocker.Docker{}
	docker.On(1, "Error: no such volume", "volume", "inspect")
	pl := volumes.Placement{DB: d, Docker: docker, Tools: "houston/mission-control:local"}
	if err := pl.Place(context.Background(), p, p.DataGeneration, p.Volumes.V); err != nil {
		t.Fatal(err)
	}
	want := "volume inspect --format {{json .Options}} shop_data\nvolume create shop_data"
	if got := strings.Join(docker.Ran(), "\n"); got != want || !placedAt(t, d) {
		t.Errorf("ran\n%s\nwant\n%s", got, want)
	}
}

// On NFS: the location's own volume first, the directory made in it, then
// a volume pointing into it; a restore's generation has names of its own.
func TestPlaceOnNFS(t *testing.T) {
	d, p := setUp(t)
	chosen(t, d, 1)
	docker := &fakedocker.Docker{}
	docker.On(1, "no such volume", "volume", "inspect")
	pl := volumes.Placement{DB: d, Docker: docker, Tools: "tools:1"}
	if err := pl.Place(context.Background(), p, 2, p.Volumes.V); err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"volume inspect --format {{json .Options}} shop.g2_data",
		"volume inspect houston-storage-nas",
		"volume create --driver local --opt type=nfs --opt o=addr=10.0.1.20,rw,nfsvers=4 --opt device=:/volume1/houston houston-storage-nas",
		"run --rm --user 0 -v houston-storage-nas:/location --entrypoint mkdir tools:1 -p /location/volumes/shop.g2/data",
		"volume create --driver local --opt type=nfs --opt o=addr=10.0.1.20,rw,nfsvers=4 --opt device=:/volume1/houston/volumes/shop.g2/data shop.g2_data",
	}, "\n")
	if got := strings.Join(docker.Ran(), "\n"); got != want || !placedAt(t, d) {
		t.Errorf("ran\n%s\nwant\n%s", got, want)
	}
}

// In a folder: a bind into it.
func TestPlaceInAFolder(t *testing.T) {
	d, p := setUp(t)
	chosen(t, d, 2)
	docker := &fakedocker.Docker{}
	docker.On(1, "no such volume", "volume", "inspect")
	pl := volumes.Placement{DB: d, Docker: docker, Tools: "tools:1"}
	if err := pl.Place(context.Background(), p, 1, p.Volumes.V); err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"volume inspect --format {{json .Options}} shop_data",
		"run --rm --user 0 -v /srv/houston:/location --entrypoint mkdir tools:1 -p /location/volumes/shop/data",
		"volume create --driver local --opt type=none --opt o=bind --opt device=/srv/houston/volumes/shop/data shop_data",
	}, "\n")
	if got := strings.Join(docker.Ran(), "\n"); got != want {
		t.Errorf("ran\n%s\nwant\n%s", got, want)
	}
}

// A volume that exists is checked, never moved: where it is must be where
// it was chosen to live.
func TestPlaceChecksWhatExists(t *testing.T) {
	d, p := setUp(t)
	chosen(t, d, 1)
	docker := &fakedocker.Docker{}
	docker.On(0, `{"device":":/volume1/houston/volumes/shop/data","o":"addr=10.0.1.20,rw,nfsvers=4","type":"nfs"}`, "volume", "inspect")
	pl := volumes.Placement{DB: d, Docker: docker, Tools: "tools:1"}
	if err := pl.Place(context.Background(), p, 1, p.Volumes.V); err != nil || len(docker.Ran()) != 1 || !placedAt(t, d) {
		t.Errorf("where it should be: %v %v", err, docker.Ran())
	}

	// On local disk, chosen for the NAS.
	d2, p2 := setUp(t)
	chosen(t, d2, 1)
	local := &fakedocker.Docker{}
	local.On(0, "null", "volume", "inspect")
	err := volumes.Placement{DB: d2, Docker: local, Tools: "tools:1"}.Place(context.Background(), p2, 1, p2.Volumes.V)
	var refused models.Refused
	if !errors.As(err, &refused) || refused.Msg != "shop_data already exists on local disk, not nas; Houston doesn't move volumes yet: choose local disk, or move it yourself" || placedAt(t, d2) {
		t.Errorf("on local disk: %v", err)
	}

	// Elsewhere on NFS, or the right path but not NFS: refused too, each
	// named as best it can be.
	for have, where := range map[string]string{
		`{"device":":/volume1/elsewhere/shop/data","type":"nfs"}`:        ":/volume1/elsewhere/shop/data",
		`{"device":":/volume1/houston/volumes/shop/data","type":"none"}`: "nas",
	} {
		d, p := setUp(t)
		chosen(t, d, 1)
		docker := &fakedocker.Docker{}
		docker.On(0, have, "volume", "inspect")
		err := volumes.Placement{DB: d, Docker: docker, Tools: "tools:1"}.Place(context.Background(), p, 1, p.Volumes.V)
		if !errors.As(err, &refused) || !strings.HasPrefix(refused.Msg, "shop_data already exists on "+where+", not nas;") {
			t.Errorf("%s: %v", have, err)
		}
	}

	// On the NAS, chosen for local disk: named by its location.
	d3, p3 := setUp(t)
	nas := &fakedocker.Docker{}
	nas.On(0, `{"device":":/volume1/houston/volumes/shop/data","type":"nfs"}`, "volume", "inspect")
	err = volumes.Placement{DB: d3, Docker: nas, Tools: "tools:1"}.Place(context.Background(), p3, 1, p3.Volumes.V)
	if !errors.As(err, &refused) || refused.Msg != "shop_data already exists on nas, not local disk; Houston doesn't move volumes yet: choose nas, or move it yourself" {
		t.Errorf("on the NAS: %v", err)
	}
}

// Docker's refusals are the sync's, in Docker's words.
func TestPlaceFails(t *testing.T) {
	for _, c := range []struct {
		location int
		fail     []string
		want     string
	}{
		{0, []string{"volume", "create"}, "couldn't create shop_data: disk full"},
		{1, []string{"volume", "create", "--driver", "local", "--opt", "type=nfs", "--opt", "o=addr=10.0.1.20,rw,nfsvers=4", "--opt", "device=:/volume1/houston", "houston-storage-nas"},
			"couldn't reach nas's NFS volume: disk full"},
		{2, []string{"run"}, "couldn't make shop_data's directory on disk: disk full"},
	} {
		d, p := setUp(t)
		if c.location > 0 {
			chosen(t, d, c.location)
		}
		docker := &fakedocker.Docker{}
		docker.On(1, "no such volume", "volume", "inspect")
		docker.On(1, "disk full\n", c.fail...)
		err := volumes.Placement{DB: d, Docker: docker, Tools: "tools:1"}.Place(context.Background(), p, 1, p.Volumes.V)
		var refused models.Refused
		if !errors.As(err, &refused) || refused.Msg != c.want || placedAt(t, d) {
			t.Errorf("%v: %v", c.fail, err)
		}
	}
}
