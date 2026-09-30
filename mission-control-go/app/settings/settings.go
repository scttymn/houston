package settings

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/scttymn/gantry/db"
	"github.com/scttymn/gantry/live"
	"github.com/scttymn/gantry/sign"
	"github.com/scttymn/gantry/web"

	"github.com/scttymn/houston/mission-control-go/app/models"
	"github.com/scttymn/houston/mission-control-go/app/services/cfsettings"
	"github.com/scttymn/houston/mission-control-go/app/services/dockercmd"
	"github.com/scttymn/houston/mission-control-go/app/services/port"
	"github.com/scttymn/houston/mission-control-go/app/services/release"
	"github.com/scttymn/houston/mission-control-go/app/services/serverupdate"
	"github.com/scttymn/houston/mission-control-go/app/services/storage"
	"github.com/scttymn/houston/mission-control-go/app/services/systemstatus"
	"github.com/scttymn/houston/mission-control-go/app/shared/layout"
)

// GitHubDown is what a check says when GitHub didn't answer.
const GitHubDown = "Couldn't reach GitHub just now; try again in a minute."

// followEvery is how often a running update is followed.
const followEvery = 5 * time.Second

// Controller is Settings'.
type Controller struct {
	DB         *db.DB
	Signer     sign.Signer
	Live       *live.Hub
	Board      string // the flight board's stream
	Version    string
	Docker     dockercmd.Runner
	Status     *systemstatus.Status
	Cloudflare *cfsettings.Settings
	Port       *port.Port
	Release    release.Checker
	Updater    serverupdate.Updater
	Follow     interface {
		EnqueueIn(ctx context.Context, a models.UpdateArgs, d time.Duration) (int64, error)
	}
	// MissionControl is the tunnel's address for Mission Control.
	MissionControl string
}

func (c Controller) flash() web.Flash { return web.Flash{Signer: c.Signer} }

// page is the Settings page as it is now; a form's handler fills in what it
// adds (an error, a new token, checks) before drawing it.
func (c Controller) page(w http.ResponseWriter, r *http.Request) (Show, error) {
	ctx, now := r.Context(), time.Now()
	q := models.New(c.DB.Read)
	inst, err := q.CurrentInstallation(ctx)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Show{}, err
	}
	s := Show{Page: layout.For(r, "Settings · Mission Control"), Inst: inst, Version: c.Version, Now: now.In(web.Zone(r)),
		Newer: models.NewerRelease(c.Version, inst.LatestRelease), Tunnel: c.Status.Tunnel(ctx, inst, now), Address: c.Port.Address(ctx),
		MissionControl: c.MissionControl}
	s.Page.Chrome.Settings = true
	s.Page.Head = layout.MorphRefreshes()
	if kind, msg, ok := c.flash().Take(w, r); ok {
		switch kind {
		case "alert":
			s.Alert = msg
		case "go", "nogo":
			s.Page.Toast = &layout.Toast{NoGo: kind == "nogo", Text: msg}
		default:
			s.Notice = msg
		}
	}
	if s.Tokens, err = q.AllTokens(ctx); err != nil {
		return s, err
	}
	if view, ok := c.Cloudflare.Last(inst); ok {
		s.Cloudflare = &view
	}
	if s.Updates, err = q.RecentUpdates(ctx); err != nil {
		return s, err
	}
	locations, err := q.VerifiedLocations(ctx)
	if err != nil {
		return s, err
	}
	for _, l := range locations {
		using, err := q.ProjectsUsingLocation(ctx, models.ProjectsUsingLocationParams{Location: sql.NullInt64{Int64: l.ID, Valid: true},
			IsDefaultAndReady: l.IsDefault && l.AcknowledgedAt.Valid})
		if err != nil {
			return s, err
		}
		row := location{L: l, UsedBy: len(using)}
		if last, err := q.LocationLastWrite(ctx, l.ID); err == nil && last.Valid {
			row.LastWrite = &last.Time
		} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return s, err
		}
		s.Locations = append(s.Locations, row)
	}
	return s, nil
}

func (c Controller) render(w http.ResponseWriter, r *http.Request, status int, s Show) error {
	return web.Render(w, r, status, ShowPage(s))
}

// Show is GET /settings.
func (c Controller) Show(w http.ResponseWriter, r *http.Request) error {
	s, err := c.page(w, r)
	if err != nil {
		return err
	}
	return c.render(w, r, http.StatusOK, s)
}

// to is a redirect to a section of Settings.
func to(section string) func(w http.ResponseWriter, r *http.Request) error {
	return func(w http.ResponseWriter, r *http.Request) error {
		http.Redirect(w, r, "/settings#"+section, http.StatusFound)
		return nil
	}
}

// General, Tokens and Storage are the old sections' pages: Settings now.
var (
	General = to("cloudflare")
	Tokens  = to("tokens")
	Storage = to("storage")
)

// SetTimeZone is PATCH /settings/general {time_zone}: Houston's time zone,
// the one backup schedules run in.
func (c Controller) SetTimeZone(w http.ResponseWriter, r *http.Request) error {
	zone := r.PostFormValue("time_zone")
	if !models.ValidTimeZone(zone) {
		s, err := c.page(w, r)
		if err != nil {
			return err
		}
		s.ZoneError = "Time zone " + zone + " isn't a time zone (use an IANA name, like Europe/Berlin)"
		return c.render(w, r, http.StatusUnprocessableEntity, s)
	}
	if err := models.New(c.DB.Write).SetTimeZone(r.Context(), models.SetTimeZoneParams{TimeZone: zone, UpdatedAt: time.Now()}); err != nil {
		return err
	}
	c.flash().Redirect(w, r, "/settings#time-zone", "notice", "Houston's time zone is "+zone+".")
	return nil
}

// NewToken is POST /settings/tokens {name}: a personal token, shown on the
// page this once.
func (c Controller) NewToken(w http.ResponseWriter, r *http.Request) error {
	token, row, err := models.IssueToken(r.Context(), models.New(c.DB.Write), r.PostFormValue("name"))
	var refused models.Refused
	if err != nil && !errors.As(err, &refused) {
		return err
	}
	s, perr := c.page(w, r)
	if perr != nil {
		return perr
	}
	if err != nil {
		s.TokenError = refused.Msg
		return c.render(w, r, http.StatusUnprocessableEntity, s)
	}
	s.NewToken, s.CreatedName = token, row.Name
	return c.render(w, r, http.StatusOK, s)
}

// RevokeToken is DELETE /settings/tokens/{id}.
func (c Controller) RevokeToken(w http.ResponseWriter, r *http.Request) error {
	n, err := models.New(c.DB.Write).DeleteToken(r.Context(), web.ID(r, "id"))
	if err != nil {
		return err
	}
	if n == 0 {
		return web.Status(http.StatusNotFound, errors.New("no token "+r.PathValue("id")))
	}
	c.flash().Redirect(w, r, "/settings#tokens", "notice", "Revoked. Anything using that token stops working now.")
	return nil
}

// CloudflareLive is GET /settings/cloudflare: the live panel, asked now
// (loaded after the page: Cloudflare can be slow).
func (c Controller) CloudflareLive(w http.ResponseWriter, r *http.Request) error {
	inst, err := models.New(c.DB.Read).CurrentInstallation(r.Context())
	if err != nil {
		return err
	}
	now := time.Now()
	return web.Render(w, r, http.StatusOK, Live(c.Cloudflare.Fetch(r.Context(), inst, now), now.In(web.Zone(r)), c.MissionControl))
}

// CloudflareToken is PATCH /settings/cloudflare/token {api_token}: the new
// token checked, then saved; the checks shown when it doesn't pass.
func (c Controller) CloudflareToken(w http.ResponseWriter, r *http.Request) error {
	inst, err := models.New(c.DB.Read).CurrentInstallation(r.Context())
	if err != nil {
		return err
	}
	replaced, checks := c.Cloudflare.ReplaceToken(r.Context(), inst, r.PostFormValue("api_token"), time.Now())
	if replaced {
		c.Status.Forget()
		c.flash().Redirect(w, r, "/settings#cloudflare", "notice", "Cloudflare token replaced: it passed every check.")
		return nil
	}
	s, err := c.page(w, r)
	if err != nil {
		return err
	}
	s.TokenChecks = checks
	return c.render(w, r, http.StatusUnprocessableEntity, s)
}

// RepairCloudflare is POST /settings/cloudflare/repair: routes and
// records put back, and what was done.
func (c Controller) RepairCloudflare(w http.ResponseWriter, r *http.Request) error {
	inst, err := models.New(c.DB.Read).CurrentInstallation(r.Context())
	if err != nil {
		return err
	}
	results, err := c.Cloudflare.Repair(r.Context(), inst, time.Now())
	if err != nil {
		return err
	}
	c.Cloudflare.Forget(inst)
	s, err := c.page(w, r)
	if err != nil {
		return err
	}
	s.Repair = results
	if s.Repair == nil {
		s.Repair = []cfsettings.Result{}
	}
	return c.render(w, r, http.StatusOK, s)
}

// SetPort is PATCH /settings/port {open}: port 3000 opened to the network
// or closed. Closing it from the port itself goes on to admin.<base>
// first, so the page isn't the one that disappears.
func (c Controller) SetPort(w http.ResponseWriter, r *http.Request) error {
	open := r.PostFormValue("open") == "1"
	err := c.Port.Set(r.Context(), open)
	var refused port.Refused
	if errors.As(err, &refused) {
		c.flash().Redirect(w, r, "/settings#security", "alert", "Port 3000 is unchanged: Houston "+string(refused)+".")
		return nil
	}
	if err != nil {
		return err
	}
	inst, err := models.New(c.DB.Read).CurrentInstallation(r.Context())
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if !open && r.Header.Get("Cf-Ray") == "" && inst.BaseDomain != "" {
		http.Redirect(w, r, "https://admin."+inst.BaseDomain+"/settings#security", http.StatusSeeOther)
		return nil
	}
	words := "closing to the network"
	if open {
		words = "opening to your network"
	}
	c.flash().Redirect(w, r, "/settings#security", "notice", "Port 3000 is "+words+". Mission Control restarts for a few seconds.")
	return nil
}

// CheckRelease is POST /settings/updates/check: GitHub asked now; what it
// said, as a toast.
func (c Controller) CheckRelease(w http.ResponseWriter, r *http.Request) error {
	tag, changed, err := c.Release.Check(r.Context(), c.DB, time.Now())
	if err != nil {
		c.flash().Redirect(w, r, "/settings#releases", "nogo", GitHubDown)
		return nil
	}
	if changed {
		c.Live.Refresh(c.Board, "")
	}
	words := tag + " is the latest."
	if newer := models.NewerRelease(c.Version, tag); newer != "" {
		words = newer + " is out."
	}
	c.flash().Redirect(w, r, "/settings#releases", "go", words)
	return nil
}

// StartUpdate is POST /settings/updates {version}: this server updated,
// then the update's page.
func (c Controller) StartUpdate(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	u, err := c.Updater.Start(ctx, r.PostFormValue("version"), time.Now())
	var refused serverupdate.Refused
	if errors.As(err, &refused) {
		c.flash().Redirect(w, r, "/settings#releases", "nogo", "No update: Houston "+string(refused)+".")
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := c.Follow.EnqueueIn(ctx, models.UpdateArgs{Follow: true}, followEvery); err != nil {
		return err
	}
	c.Live.Refresh(c.Board, "")
	c.flash().Redirect(w, r, "/settings/updates/"+strconv.FormatInt(u.ID, 10), "go",
		"Updating to "+u.ToVersion+". Mission Control restarts on the way; this page comes back when it's up.")
	return nil
}

// ShowUpdate is GET /settings/updates/{id}: an update's steps and log,
// refreshing itself while it runs.
func (c Controller) ShowUpdate(w http.ResponseWriter, r *http.Request) error {
	u, err := models.New(c.DB.Read).UpdateByID(r.Context(), web.ID(r, "id"))
	if errors.Is(err, sql.ErrNoRows) {
		return web.Status(http.StatusNotFound, errors.New("no update "+r.PathValue("id")))
	}
	if err != nil {
		return err
	}
	page := layout.For(r, "Update #"+strconv.FormatInt(u.ID, 10)+" · Releases · Mission Control")
	page.Chrome.Settings = true
	page.Head = layout.MorphRefreshes()
	if kind, msg, ok := c.flash().Take(w, r); ok && (kind == "go" || kind == "nogo") {
		page.Toast = &layout.Toast{NoGo: kind == "nogo", Text: msg}
	}
	return web.Render(w, r, http.StatusOK, UpdatePage(page, u, c.Version, time.Now().In(web.Zone(r))))
}

func (c Controller) storagePage(r *http.Request, title string) layout.Page {
	page := layout.For(r, title)
	page.Chrome.Settings = true
	return page
}

// NewStorage is GET /settings/storage/new.
func (c Controller) NewStorage(w http.ResponseWriter, r *http.Request) error {
	return web.Render(w, r, http.StatusOK, NewStoragePage(c.storagePage(r, "Add storage · Settings"), storage.Setup{Kind: "nfs"}, storage.Saved{}))
}

// CreateStorage is POST /settings/storage: the location tested (restic
// writes there) and saved, then its password to save.
func (c Controller) CreateStorage(w http.ResponseWriter, r *http.Request) error {
	f := func(name string) string { return strings.TrimSpace(r.PostFormValue("storage[" + name + "]")) }
	setup := storage.Setup{Kind: f("kind"), Name: f("name"), NFSServer: f("nfs_server"), NFSExport: f("nfs_export"), LocalPath: f("local_path"),
		S3Endpoint: f("s3_endpoint"), S3Bucket: f("s3_bucket"), S3AccessKeyID: f("s3_access_key_id"), S3SecretAccess: r.PostFormValue("storage[s3_secret_access_key]"),
		B2Bucket: f("b2_bucket"), B2KeyID: f("b2_key_id"), B2ApplicationKey: r.PostFormValue("storage[b2_application_key]")}
	saved, err := setup.Save(r.Context(), c.DB, c.Docker, time.Now())
	if err != nil {
		return err
	}
	if !saved.OK() {
		return web.Render(w, r, http.StatusUnprocessableEntity, NewStoragePage(c.storagePage(r, "Add storage · Settings"), setup, saved))
	}
	http.Redirect(w, r, "/settings/storage/"+saved.Location.Name, http.StatusSeeOther)
	return nil
}

// unconfirmed is the location the request names, while its password page
// exists: verified, and not yet confirmed. Otherwise, back to Settings.
func (c Controller) unconfirmed(w http.ResponseWriter, r *http.Request) (*models.StorageLocation, error) {
	w.Header().Set("Cache-Control", "no-store") // it shows the restic password
	l, err := models.New(c.DB.Read).StorageLocationByName(r.Context(), r.PathValue("name"))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, web.Status(http.StatusNotFound, errors.New("no storage location "+r.PathValue("name")))
	}
	if err != nil {
		return nil, err
	}
	if l.AcknowledgedAt.Valid || !l.VerifiedAt.Valid {
		http.Redirect(w, r, "/settings#storage", http.StatusFound)
		return nil, nil
	}
	return &l, nil
}

// ShowStorage is GET /settings/storage/{name}: its password, shown once.
func (c Controller) ShowStorage(w http.ResponseWriter, r *http.Request) error {
	l, err := c.unconfirmed(w, r)
	if err != nil || l == nil {
		return err
	}
	return web.Render(w, r, http.StatusOK, StoragePage(c.storagePage(r, l.Name+" · Storage · Settings"), *l, false))
}

// StoragePassword is GET /settings/storage/{name}/password.txt.
func (c Controller) StoragePassword(w http.ResponseWriter, r *http.Request) error {
	l, err := c.unconfirmed(w, r)
	if err != nil || l == nil {
		return err
	}
	w.Header().Set("Content-Type", "text/plain")
	w.Header().Set("Content-Disposition", `attachment; filename="houston-`+l.Name+`-restic-password.txt"`)
	_, err = w.Write([]byte(l.ResticPassword.Reveal() + "\n"))
	return err
}

// AcknowledgeStorage is POST /settings/storage/{name}/acknowledge {saved}:
// the password saved, the location ready.
func (c Controller) AcknowledgeStorage(w http.ResponseWriter, r *http.Request) error {
	l, err := c.unconfirmed(w, r)
	if err != nil || l == nil {
		return err
	}
	if r.PostFormValue("saved") != "1" {
		return web.Render(w, r, http.StatusUnprocessableEntity, StoragePage(c.storagePage(r, l.Name+" · Storage · Settings"), *l, true))
	}
	if err := models.New(c.DB.Write).AcknowledgeLocation(r.Context(), models.AcknowledgeLocationParams{Now: sql.NullTime{Time: time.Now(), Valid: true}, ID: l.ID}); err != nil {
		return err
	}
	words := l.Name + " is ready for backups"
	if l.Live() {
		words += " and live volumes"
	}
	c.flash().Redirect(w, r, "/settings#storage", "notice", words+".")
	return nil
}

// MakeDefault is POST /settings/storage/{name}/default.
func (c Controller) MakeDefault(w http.ResponseWriter, r *http.Request) error {
	l, err := models.New(c.DB.Read).StorageLocationByName(r.Context(), r.PathValue("name"))
	if errors.Is(err, sql.ErrNoRows) {
		return web.Status(http.StatusNotFound, errors.New("no storage location "+r.PathValue("name")))
	}
	if err != nil {
		return err
	}
	if !l.AcknowledgedAt.Valid {
		c.flash().Redirect(w, r, "/settings#storage", "alert", l.Name+" isn't set up yet")
		return nil
	}
	if err := models.New(c.DB.Write).MakeDefaultLocation(r.Context(), models.MakeDefaultLocationParams{ID: l.ID, Now: time.Now()}); err != nil {
		return err
	}
	c.flash().Redirect(w, r, "/settings#storage", "notice", l.Name+" is the default: projects without their own target back up there.")
	return nil
}
