package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/scttymn/gantry/crypt"
	"github.com/scttymn/gantry/db"
	"github.com/scttymn/gantry/web"

	"github.com/scttymn/houston/mission-control-go/app/models"
	"github.com/scttymn/houston/mission-control-go/app/services/backup"
	"github.com/scttymn/houston/mission-control-go/app/services/gitremote"
	"github.com/scttymn/houston/mission-control-go/app/services/maintenance"
)

// v1Body is a small write's JSON (Rails' params): what's sent, each field
// still JSON.
const v1Body = 1 << 20

// field is a body's field as a string, and whether it was one.
func field(b Body, name string) (string, bool) {
	var s string
	err := json.Unmarshal(b[name], &s)
	return s, err == nil && len(b[name]) > 0 && b[name][0] == '"'
}

// isTrue is whether a body's field is the JSON literal true (the Go
// clients send it so).
func isTrue(b Body, name string) bool { return string(b[name]) == "true" }

// UpdateSettings is PATCH or PUT /api/v1/settings {time_zone}: Houston's
// time zone, an IANA name; backup schedules run in it.
func (c V1) UpdateSettings(w http.ResponseWriter, r *http.Request) error {
	body, err := readBody(r, v1Body)
	if err != nil {
		return err
	}
	zone, _ := field(body, "time_zone")
	if !models.ValidTimeZone(zone) {
		return web.Status(http.StatusUnprocessableEntity, fmt.Errorf("Time zone %s isn't a time zone (use an IANA name, like Europe/Berlin)", zone))
	}
	if err := models.New(c.DB.Write).SetTimeZone(r.Context(), models.SetTimeZoneParams{TimeZone: zone, UpdatedAt: time.Now()}); err != nil {
		return err
	}
	return c.Settings(w, r)
}

// maxSecret is the largest value a secret may have.
const maxSecret = 64 << 10

// secretKey is the project and the key the path names, if compose.yml
// references it.
func (c V1) secretKey(r *http.Request) (models.Project, string, error) {
	p, err := c.project(r)
	if err != nil {
		return p, "", err
	}
	key := r.PathValue("key")
	for _, v := range p.Variables.V {
		if v.Name == key {
			return p, key, nil
		}
	}
	return p, key, web.Status(http.StatusNotFound, fmt.Errorf("%s's compose.yml doesn't reference %s", p.Name, key))
}

// SetSecret is PUT or PATCH /api/v1/projects/{name}/secrets/{key} {value}:
// write-only; the value never comes back.
func (c V1) SetSecret(w http.ResponseWriter, r *http.Request) error {
	p, key, err := c.secretKey(r)
	if err != nil {
		return err
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, maxSecret+1))
	if err != nil {
		return err
	}
	if len(data) > maxSecret {
		return web.Status(http.StatusRequestEntityTooLarge, fmt.Errorf("a value can be at most %d KiB", maxSecret>>10))
	}
	var body struct {
		Value json.RawMessage `json:"value"`
	}
	json.Unmarshal(data, &body)
	var value string
	if json.Unmarshal(body.Value, &value) != nil && len(body.Value) > 0 && body.Value[0] != 'n' {
		value = string(body.Value) // a number, as Rails would take it
	}
	return c.saveSecret(w, r, p, key, value)
}

// GenerateSecret is POST .../secrets/{key}/generate: a value nobody needs
// to know.
func (c V1) GenerateSecret(w http.ResponseWriter, r *http.Request) error {
	p, key, err := c.secretKey(r)
	if err != nil {
		return err
	}
	return c.saveSecret(w, r, p, key, models.GeneratedSecret())
}

func (c V1) saveSecret(w http.ResponseWriter, r *http.Request, p models.Project, key, value string) error {
	if err := models.ValidSecret(key, value); err != nil {
		return web.Status(http.StatusUnprocessableEntity, err)
	}
	if err := models.New(c.DB.Write).SaveSecret(r.Context(), models.SaveSecretParams{ProjectID: p.ID, Key: key, Value: crypt.Of(value), Now: time.Now()}); err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, map[string]any{"name": key, "set": true})
}

// RemoveSecret is DELETE .../secrets/{key}.
func (c V1) RemoveSecret(w http.ResponseWriter, r *http.Request) error {
	p, key, err := c.secretKey(r)
	if err != nil {
		return err
	}
	if err := models.New(c.DB.Write).DeleteSecret(r.Context(), models.DeleteSecretParams{ProjectID: p.ID, Key: key}); err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, map[string]any{"name": key, "set": false})
}

// RotateWebhook is POST .../webhook/rotate: a new secret, shown until a
// push verifies it.
func (c V1) RotateWebhook(w http.ResponseWriter, r *http.Request) error {
	p, err := c.project(r)
	if err == nil {
		err = linked(p)
	}
	if err != nil {
		return err
	}
	p, err = models.New(c.DB.Write).RotateWebhookSecret(r.Context(), models.RotateWebhookSecretParams{
		WebhookSecret: crypt.Of(models.NewToken()), UpdatedAt: time.Now(), ID: p.ID})
	if err != nil {
		return err
	}
	return c.webhookView(w, r, p)
}

// Maintenance is PATCH or PUT .../maintenance {on, message}: Houston's page
// for the project. On routes its hostnames to Mission Control at the
// tunnel; off routes them back. A toggle Cloudflare refuses is rolled back,
// so the database and the tunnel always agree.
func (c V1) Maintenance(w http.ResponseWriter, r *http.Request) error {
	body, err := readBody(r, v1Body)
	if err != nil {
		return err
	}
	p, err := c.project(r)
	if err != nil {
		return err
	}
	ctx, now := r.Context(), time.Now()
	s := maintenance.Switch{DB: c.DB, Services: c.Services, Cloudflare: c.Cloudflare}
	if isTrue(body, "on") {
		message, _ := field(body, "message")
		token, _ := web.Get(r, tokenKey)
		p, err = s.On(ctx, p, "token "+token.Name, message, now)
	} else {
		p, err = s.Off(ctx, p, now)
	}
	var failed maintenance.Failed
	var refused models.Refused
	switch {
	case errors.As(err, &failed):
		return badGatewayAnswer(w, string(failed))
	case errors.As(err, &refused):
		return web.Status(http.StatusUnprocessableEntity, refused)
	case err != nil:
		return err
	}
	c.Live.Refresh(FlightBoard, "")
	return web.JSON(w, http.StatusOK, viewMaintenance(p))
}

func viewMaintenance(p models.Project) maintenanceView {
	if !p.MaintenanceSince.Valid {
		return maintenanceView{On: false}
	}
	return maintenanceView{On: true, Since: &p.MaintenanceSince.Time, By: orNull(p.MaintenanceBy), Message: orNull(p.MaintenanceMessage)}
}

func badGatewayAnswer(w http.ResponseWriter, msg string) error {
	return web.JSON(w, http.StatusBadGateway, map[string]string{"error": msg})
}

// location is the storage location a body names (none: nil), or the 422.
func (c V1) location(r *http.Request, body Body) (*models.StorageLocation, error) {
	name, _ := field(body, "location")
	if name == "" {
		return nil, nil
	}
	l, err := models.New(c.DB.Read).StorageLocationByName(r.Context(), name)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, web.Status(http.StatusUnprocessableEntity, fmt.Errorf("no storage location %s", name))
	}
	return &l, err
}

// ChooseVolume is PATCH or PUT .../volumes/{volume} {location}: where the
// volume lives (none: local disk), until Houston makes it; moving it after
// is a later feature.
func (c V1) ChooseVolume(w http.ResponseWriter, r *http.Request) error {
	body, err := readBody(r, v1Body)
	if err != nil {
		return err
	}
	p, err := c.project(r)
	if err != nil {
		return err
	}
	location, err := c.location(r, body)
	if err != nil {
		return err
	}
	name := r.PathValue("volume")
	_, err = models.ChooseVolume(r.Context(), c.DB, p, name, location, time.Now())
	var none models.NoVolume
	var refused models.Refused
	var placed models.Placed
	switch {
	case errors.As(err, &none):
		return web.Status(http.StatusNotFound, none)
	case errors.As(err, &refused):
		return web.Status(http.StatusUnprocessableEntity, refused)
	case errors.As(err, &placed):
		return web.Status(http.StatusConflict, placed)
	case err != nil:
		return err
	}
	view := volumeView{Name: name}
	for _, v := range p.Volumes.V {
		if v.Name == name {
			view.Path = v.Path
		}
	}
	if location != nil {
		view.Location = &location.Name
	}
	return web.JSON(w, http.StatusOK, view)
}

// BackupTarget is PATCH or PUT .../backup_target {location}: where the
// project's backups go (none: the default).
func (c V1) BackupTarget(w http.ResponseWriter, r *http.Request) error {
	body, err := readBody(r, v1Body)
	if err != nil {
		return err
	}
	p, err := c.project(r)
	if err != nil {
		return err
	}
	location, err := c.location(r, body)
	if err != nil {
		return err
	}
	target, err := models.ChooseBackupTarget(r.Context(), c.DB, p, location, time.Now())
	var refused models.Refused
	if errors.As(err, &refused) {
		return web.Status(http.StatusUnprocessableEntity, refused)
	}
	if err != nil {
		return err
	}
	var name *string
	if target != nil {
		name = &target.Name
	}
	return web.JSON(w, http.StatusOK, map[string]any{"backup_location": name})
}

// BackupNow is POST .../backups: a backup now (202).
func (c V1) BackupNow(w http.ResponseWriter, r *http.Request) error {
	p, err := c.project(r)
	if err != nil {
		return err
	}
	ctx, now := r.Context(), time.Now()
	var run models.BackupRun
	err = c.DB.Tx(ctx, func(tx *db.Tx) (err error) {
		run, err = models.RequestManualBackup(ctx, tx, c.Backup, p, now)
		return err
	})
	var refused models.Refused
	if errors.As(err, &refused) {
		return web.Status(http.StatusUnprocessableEntity, refused)
	}
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusAccepted, viewBackup(run, now))
}

// DefaultStorage is PATCH or PUT /api/v1/storage/{location} {default: true}:
// the location backups go to when a project has none of its own.
func (c V1) DefaultStorage(w http.ResponseWriter, r *http.Request) error {
	body, err := readBody(r, v1Body)
	if err != nil {
		return err
	}
	name := r.PathValue("location")
	ctx := r.Context()
	l, err := models.New(c.DB.Read).StorageLocationByName(ctx, name)
	if errors.Is(err, sql.ErrNoRows) {
		return web.Status(http.StatusNotFound, fmt.Errorf("no storage location %s", name))
	}
	if err != nil {
		return err
	}
	if !l.AcknowledgedAt.Valid {
		return web.Status(http.StatusUnprocessableEntity, fmt.Errorf("%s isn't set up yet", l.Name))
	}
	if !isTrue(body, "default") {
		return web.Status(http.StatusUnprocessableEntity, errors.New("send {default: true}"))
	}
	if err := models.New(c.DB.Write).MakeDefaultLocation(ctx, models.MakeDefaultLocationParams{ID: l.ID, Now: time.Now()}); err != nil {
		return err
	}
	if l, err = models.New(c.DB.Read).StorageLocationByName(ctx, name); err != nil {
		return err
	}
	v, err := c.viewStorage(ctx, l)
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, v)
}

// DeployNow is POST /api/v1/projects/{name}/deploys {fresh}: the head of
// what the deploy rule matches, queued now; fresh: true rebuilds, without
// Docker's layer cache.
func (c V1) DeployNow(w http.ResponseWriter, r *http.Request) error {
	body, err := readBody(r, v1Body)
	if err != nil && r.ContentLength != 0 {
		return err
	}
	p, err := c.project(r)
	if err != nil {
		return err
	}
	if p.RepoUrl == "" {
		return web.Status(http.StatusUnprocessableEntity, errors.New("link the repo first (houston link)"))
	}
	now := time.Now()
	d, err := models.QueueHead(r.Context(), c.DB, c.Refs, p, isTrue(body, "fresh"), now)
	var refused models.Refused
	if errors.As(err, &refused) {
		return badGatewayAnswer(w, refused.Msg)
	}
	if err != nil {
		return err
	}
	c.Live.Refresh(FlightBoard, "")
	return web.JSON(w, http.StatusOK, viewDeploy(d, now))
}

// RequestRestore is POST /api/v1/projects/{name}/restores {snapshot,
// location, confirm}: a restore of the project to a snapshot, code and
// data together, queued for a runner (202).
func (c V1) RequestRestore(w http.ResponseWriter, r *http.Request) error {
	body, err := readBody(r, v1Body)
	if err != nil {
		return err
	}
	p, err := c.project(r)
	if err != nil {
		return err
	}
	ctx, now := r.Context(), time.Now()
	q := models.New(c.DB.Read)
	var location *models.StorageLocation
	if name, _ := field(body, "location"); name != "" {
		if l, err := q.StorageLocationByName(ctx, name); err == nil {
			location = &l
		}
	} else if l, err := q.BackupLocationFor(ctx, p.BackupLocationID.Int64); err == nil {
		location = &l
	}
	snapshot, _ := field(body, "snapshot")
	confirm, _ := field(body, "confirm")
	commit := func(ctx context.Context, p models.Project, sha string) string {
		return c.Git.Commit(ctx, gitremote.Link{RepoURL: p.RepoUrl, DeployKey: p.DeployKeyPrivate.Reveal()}, sha)
	}
	restore, err := backup.RequestRestore(ctx, c.DB, c.SnapshotList, commit, p, snapshot, location, confirm, now)
	var refused models.Refused
	if errors.As(err, &refused) {
		return web.Status(http.StatusUnprocessableEntity, refused)
	}
	if err != nil {
		return err
	}
	c.Live.Refresh(FlightBoard, "")
	return web.JSON(w, http.StatusAccepted, viewDeploy(restore, now))
}
