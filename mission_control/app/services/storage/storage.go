// Package storage adds a backup location:
// the form's fields checked, the location saved with a new restic password
// (before restic init, so a retry after a crash reuses it and can open what
// it made), and restic made to write there.
package storage

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"math/big"
	"regexp"
	"strings"
	"time"

	"github.com/scttymn/gantry/crypt"
	"github.com/scttymn/gantry/db"

	"github.com/scttymn/houston/mission_control/app/models"
	"github.com/scttymn/houston/mission_control/app/services/backup"
	"github.com/scttymn/houston/mission_control/app/services/dockercmd"
)

// Kinds are the kinds of location.
var Kinds = []string{"nfs", "local", "s3", "b2"}

var (
	host     = regexp.MustCompile(`(?i)^(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)*[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
	ipv4     = regexp.MustCompile(`^(?:\d{1,3}\.){3}\d{1,3}$`)
	bucket   = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
	name     = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
	endpoint = regexp.MustCompile(`^https://[^\s/]+/?$`)
	already  = regexp.MustCompile(`(?i)already (exists|initialized)`)
	control  = regexp.MustCompile(`[\x00-\x1f]`)
)

// absolute is an absolute path with no .. part and no control character
// (so no second line a check wouldn't see).
func absolute(p string) bool {
	if !strings.HasPrefix(p, "/") || control.MatchString(p) {
		return false
	}
	for _, part := range strings.Split(p, "/") {
		if part == ".." {
			return false
		}
	}
	return true
}

// Setup is the form's.
type Setup struct {
	Kind, Name                                          string
	NFSServer, NFSExport, LocalPath                     string
	S3Endpoint, S3Bucket, S3AccessKeyID, S3SecretAccess string
	B2Bucket, B2KeyID, B2ApplicationKey                 string
}

// Problem is a field's, as the form shows it: "Name must be…".
type Problem struct{ Field, Message string }

// labels are the fields' names as the form says them.
var labels = map[string]string{"kind": "Kind", "name": "Name", "nfs_server": "Nfs server", "nfs_export": "Nfs export", "local_path": "Local path",
	"s3_endpoint": "S3 endpoint", "s3_bucket": "S3 bucket", "s3_access_key_id": "S3 access key", "s3_secret_access_key": "S3 secret access key",
	"b2_bucket": "B2 bucket", "b2_key_id": "B2 key", "b2_application_key": "B2 application key"}

// Label is a field's name in the form's words.
func Label(field string) string { return labels[field] }

// Problems are what's wrong with the form, field by field, in its order.
func (s Setup) Problems() []Problem {
	var out []Problem
	add := func(field, message string) { out = append(out, Problem{field, message}) }
	kinds := strings.Join(Kinds, " ")
	if !strings.Contains(" "+kinds+" ", " "+s.Kind+" ") || s.Kind == "" {
		add("kind", "must be nfs, local, s3 or b2")
	}
	if !name.MatchString(s.Name) {
		add("name", "must be lowercase letters, digits and dashes")
	}
	switch s.Kind {
	case "nfs":
		if !host.MatchString(s.NFSServer) && !ipv4.MatchString(s.NFSServer) {
			add("nfs_server", "must be a host name or IP address")
		}
		if !absolute(s.NFSExport) {
			add("nfs_export", "must be an absolute path like /volume1/houston")
		}
	case "local":
		if !absolute(s.LocalPath) {
			add("local_path", "must be an absolute path on the server")
		}
	case "s3":
		if !bucket.MatchString(s.S3Bucket) {
			add("s3_bucket", "must be a bucket name")
		}
		if strings.TrimSpace(s.S3AccessKeyID) == "" {
			add("s3_access_key_id", "can't be blank")
		}
		if strings.TrimSpace(s.S3SecretAccess) == "" {
			add("s3_secret_access_key", "can't be blank")
		}
		if s.S3Endpoint != "" && !endpoint.MatchString(s.S3Endpoint) {
			add("s3_endpoint", "must be an https:// endpoint")
		}
	case "b2":
		if !bucket.MatchString(s.B2Bucket) {
			add("b2_bucket", "must be a bucket name")
		}
		if strings.TrimSpace(s.B2KeyID) == "" {
			add("b2_key_id", "can't be blank")
		}
		if strings.TrimSpace(s.B2ApplicationKey) == "" {
			add("b2_application_key", "can't be blank")
		}
	}
	return out
}

func (s Setup) settings() map[string]string {
	switch s.Kind {
	case "nfs":
		return map[string]string{"server": s.NFSServer, "export": s.NFSExport}
	case "local":
		return map[string]string{"path": s.LocalPath}
	case "s3":
		out := map[string]string{"bucket": s.S3Bucket}
		if s.S3Endpoint != "" {
			out["endpoint"] = s.S3Endpoint
		}
		return out
	case "b2":
		return map[string]string{"bucket": s.B2Bucket}
	}
	return map[string]string{}
}

func (s Setup) credentials() string {
	var c map[string]string
	switch s.Kind {
	case "s3":
		c = map[string]string{"access_key_id": s.S3AccessKeyID, "secret_access_key": s.S3SecretAccess}
	case "b2":
		c = map[string]string{"key_id": s.B2KeyID, "application_key": s.B2ApplicationKey}
	default:
		c = map[string]string{}
	}
	b, _ := json.Marshal(c)
	return string(b)
}

// base58 is Bitcoin's alphabet: no 0, O, I or l.
const base58 = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

// Password is a new restic password: 40 base58 characters.
func Password() string {
	b := make([]byte, 40)
	for i := range b {
		n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(base58))))
		b[i] = base58[n.Int64()]
	}
	return string(b)
}

// Saved is how saving went: the location (with Problems or a Failed check,
// nothing more happened).
type Saved struct {
	Location models.StorageLocation
	Problems []Problem
	Failed   string // what restic or Docker said, as a check that failed
}

// OK is a location ready to confirm.
func (s Saved) OK() bool { return len(s.Problems) == 0 && s.Failed == "" }

// Save checks the form, saves the location (reusing one of the name not
// yet confirmed, and its password), makes an NFS share's volume, and has
// restic write a repository there (or open the one there with its
// password).
func (s Setup) Save(ctx context.Context, d *db.DB, docker dockercmd.Runner, now time.Time) (Saved, error) {
	if problems := s.Problems(); problems != nil {
		return Saved{Problems: problems}, nil
	}
	q := models.New(d.Write)
	settings := models.Settings{V: s.settings()}
	l, err := q.StorageLocationByName(ctx, s.Name)
	switch {
	case err == nil && l.AcknowledgedAt.Valid:
		return Saved{Problems: []Problem{{"name", "is already used by another storage location"}}}, nil
	case err == nil:
		l, err = q.ResetLocation(ctx, models.ResetLocationParams{Kind: s.Kind, Settings: settings, Credentials: crypt.Of(s.credentials()), UpdatedAt: now, ID: l.ID})
	case errors.Is(err, sql.ErrNoRows):
		l, err = q.CreateLocation(ctx, models.CreateLocationParams{Name: s.Name, Kind: s.Kind, Settings: settings, Credentials: crypt.Of(s.credentials()),
			ResticPassword: crypt.Of(Password())})
	}
	if err != nil {
		return Saved{}, err
	}
	out := Saved{Location: l}
	if ran := EnsureVolume(ctx, docker, l); !ran.OK {
		out.Failed = "Couldn't create the NFS volume: " + lastLine(ran.Output)
		return out, nil
	}
	env := backup.ResticEnv(l)
	restic := func(command ...string) dockercmd.Result {
		return docker.Run(ctx, backup.ResticArgs(l, env, command, "", nil), dockercmd.Opts{Env: env, Timeout: 5 * time.Minute})
	}
	if init := restic("init"); !init.OK && already.MatchString(init.Output) {
		if opened := restic("cat", "config"); !opened.OK {
			out.Failed = "There's already a restic repository at " + models.WhereItIs(l) + ", and Houston's password doesn't open it (restic: " + lastLine(opened.Output) +
				"). Pick another location, or remove that repository yourself."
			return out, nil
		}
	} else if !init.OK {
		out.Failed = "restic couldn't write there: " + lastLine(init.Output)
		return out, nil
	}
	if err := q.SetLocationVerified(ctx, models.SetLocationVerifiedParams{Now: sql.NullTime{Time: now, Valid: true}, ID: l.ID}); err != nil {
		return out, err
	}
	out.Location, err = models.New(d.Read).StorageLocationByID(ctx, l.ID)
	return out, err
}

// EnsureVolume makes an NFS share's Docker volume, houston-storage-<name>,
// unless it's there: restic and live volumes mount it. Other kinds need
// none.
func EnsureVolume(ctx context.Context, docker dockercmd.Runner, l models.StorageLocation) dockercmd.Result {
	if l.Kind != "nfs" {
		return dockercmd.Result{OK: true}
	}
	volume := "houston-storage-" + l.Name
	if docker.Run(ctx, []string{"volume", "inspect", volume}, dockercmd.Opts{Timeout: time.Minute}).OK {
		return dockercmd.Result{OK: true}
	}
	s := l.Settings.V
	return docker.Run(ctx, []string{"volume", "create", "--driver", "local", "--opt", "type=nfs", "--opt", "o=addr=" + s["server"] + ",rw,nfsvers=4",
		"--opt", "device=:" + s["export"], volume}, dockercmd.Opts{Timeout: time.Minute})
}

func lastLine(output string) string {
	var last string
	for _, line := range strings.Split(output, "\n") {
		if strings.TrimSpace(line) != "" {
			last = strings.TrimSpace(line)
		}
	}
	if last == "" {
		return "no output"
	}
	return last
}
