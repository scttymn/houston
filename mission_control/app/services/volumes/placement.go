// Package volumes makes a project's named volumes where they were chosen
// to live, at sync, before Kamal mounts them: a plain Docker volume on
// local disk, or one with driver options pointing into its storage
// location (a directory per project and volume). A volume that exists
// already is checked, never moved.
package volumes

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/scttymn/gantry/db"

	"github.com/scttymn/houston/mission_control/app/models"
	"github.com/scttymn/houston/mission_control/app/services/dockercmd"
)

// Placement makes volumes with Docker.
type Placement struct {
	DB     *db.DB
	Docker dockercmd.Runner
	// Tools is the image whose mkdir makes a volume's directory in its
	// location (HOUSTON_TOOLS_IMAGE: Mission Control's own).
	Tools string
}

// option is one of docker volume create's --opt, in order.
type option struct{ key, value string }

// Place makes p's volumes of one generation: its own (a restore makes the
// next one's, from its own commit's list).
func (pl Placement) Place(ctx context.Context, p models.Project, generation int64, vols []models.Volume) error {
	g := models.Generation{Project: p.Name, Number: generation}
	q := models.New(pl.DB.Write)
	for _, v := range vols {
		if err := q.EnsureProjectVolume(ctx, models.EnsureProjectVolumeParams{ProjectID: p.ID, Name: v.Name}); err != nil {
			return err
		}
		volume, err := q.ProjectVolumeByName(ctx, models.ProjectVolumeByNameParams{ProjectID: p.ID, Name: v.Name})
		if err != nil {
			return err
		}
		var location *models.StorageLocation
		if volume.LocationID.Valid {
			l, err := q.StorageLocationByID(ctx, volume.LocationID.Int64)
			if err != nil {
				return err
			}
			location = &l
		}
		name := g.Volume(v.Name)
		want := options(location, g.Directory(v.Name))
		code, out := pl.run(ctx, "volume", "inspect", "--format", "{{json .Options}}", name)
		if code == 0 {
			var have map[string]string
			json.Unmarshal([]byte(out), &have) // null: none
			if have["type"] != value(want, "type") || have["device"] != value(want, "device") {
				where, err := pl.describe(ctx, have)
				if err != nil {
					return err
				}
				return models.Refused{Msg: fmt.Sprintf("%s already exists on %s, not %s; Houston doesn't move volumes yet: choose %s, or move it yourself", name, where, whereWords(location), where)}
			}
		} else {
			if location != nil {
				if err := pl.makeDirectory(ctx, *location, g, v.Name); err != nil {
					return err
				}
			}
			args := []string{"volume", "create"}
			if len(want) > 0 {
				args = append(args, "--driver", "local")
				for _, o := range want {
					args = append(args, "--opt", o.key+"="+o.value)
				}
			}
			if code, out := pl.run(ctx, append(args, name)...); code != 0 {
				return models.Refused{Msg: fmt.Sprintf("couldn't create %s: %s", name, strings.TrimSpace(out))}
			}
		}
		now := time.Now()
		if err := q.MarkVolumePlaced(ctx, models.MarkVolumePlacedParams{PlacedAt: sql.NullTime{Time: now, Valid: true}, UpdatedAt: now, ID: volume.ID}); err != nil {
			return err
		}
	}
	return nil
}

// options are docker volume create's for where a volume lives: none for
// local disk.
func options(l *models.StorageLocation, directory string) []option {
	switch {
	case l == nil:
		return nil
	case l.Kind == "nfs":
		s := l.Settings.V
		return []option{{"type", "nfs"}, {"o", "addr=" + s["server"] + ",rw,nfsvers=4"}, {"device", ":" + s["export"] + "/" + directory}}
	case l.Kind == "local":
		return []option{{"type", "none"}, {"o", "bind"}, {"device", l.Settings.V["path"] + "/" + directory}}
	}
	return nil
}

func value(opts []option, key string) string {
	for _, o := range opts {
		if o.key == key {
			return o.value
		}
	}
	return ""
}

func whereWords(l *models.StorageLocation) string {
	if l == nil {
		return "local disk"
	}
	return l.Name
}

// describe is where a volume with these options lives, in words: local
// disk, a location's name, or its device.
func (pl Placement) describe(ctx context.Context, have map[string]string) (string, error) {
	device := have["device"]
	if device == "" {
		return "local disk", nil
	}
	locations, err := models.New(pl.DB.Read).LiveStorageLocations(ctx)
	if err != nil {
		return "", err
	}
	for _, l := range locations {
		prefix := l.Settings.V["path"] + "/"
		if l.Kind == "nfs" {
			prefix = ":" + l.Settings.V["export"] + "/"
		}
		if strings.HasPrefix(device, prefix) {
			return l.Name, nil
		}
	}
	return device, nil
}

// volumeName is an NFS location's own Docker volume.
func volumeName(l models.StorageLocation) string { return "houston-storage-" + l.Name }

// makeDirectory makes the volume's directory in its location: it must
// exist before a volume can point into it.
func (pl Placement) makeDirectory(ctx context.Context, l models.StorageLocation, g models.Generation, volume string) error {
	root := l.Settings.V["path"]
	if l.Kind == "nfs" {
		// docker run -v with a missing named volume would make an empty local one.
		root = volumeName(l)
		if code, _ := pl.run(ctx, "volume", "inspect", root); code != 0 {
			s := l.Settings.V
			if code, out := pl.run(ctx, "volume", "create", "--driver", "local", "--opt", "type=nfs", "--opt", "o=addr="+s["server"]+",rw,nfsvers=4",
				"--opt", "device=:"+s["export"], root); code != 0 {
				return models.Refused{Msg: fmt.Sprintf("couldn't reach %s's NFS volume: %s", l.Name, strings.TrimSpace(out))}
			}
		}
	}
	code, out := pl.run(ctx, "run", "--rm", "--user", "0", "-v", root+":/location", "--entrypoint", "mkdir", pl.Tools, "-p", "/location/"+g.Directory(volume))
	if code != 0 {
		return models.Refused{Msg: fmt.Sprintf("couldn't make %s's directory on %s: %s", g.Volume(volume), l.Name, strings.TrimSpace(out))}
	}
	return nil
}

// run is docker args' exit code and output (stdout and stderr together).
func (pl Placement) run(ctx context.Context, args ...string) (int, string) {
	r := pl.Docker.Run(ctx, args, dockercmd.Opts{Timeout: 2 * time.Minute})
	if r.OK {
		return 0, r.Output
	}
	if r.Code == 0 {
		return -1, r.Output
	}
	return r.Code, r.Output
}
