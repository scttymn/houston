// Package mission talks to Mission Control's local API (mission_control's
// Api:: controllers) with the runner token.
package mission

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

var (
	// ErrUnauthorized: Mission Control refused the runner token.
	ErrUnauthorized = errors.New("Mission Control refused the runner token")
	// ErrTakenOver: the deploy is no longer this process's to report on.
	ErrTakenOver = errors.New("the deploy was finished or taken over")
)

// HoldError: sync saved the project, but required variables have no value.
type HoldError struct {
	Missing []string
	Message string
}

func (e *HoldError) Error() string { return e.Message }

// BusyError: another deploy of the project is in flight.
type BusyError struct {
	Number  int
	Message string
}

func (e *BusyError) Error() string { return e.Message }

// Client is Mission Control's local API.
type Client struct {
	URL   string
	token string
	http  *http.Client
}

func New(baseURL, token string) *Client {
	return &Client{URL: strings.TrimRight(baseURL, "/"), token: token, http: &http.Client{Timeout: 30 * time.Second}}
}

// FromEnvironment reads HOUSTON_URL (default http://127.0.0.1:3000) and the
// runner token: HOUSTON_TOKEN, else ~/.config/houston/runner-token, which the
// installer writes for the houston user.
func FromEnvironment(getenv func(string) string, home string) (*Client, error) {
	base := getenv("HOUSTON_URL")
	if base == "" {
		base = "http://127.0.0.1:3000"
	}
	token := strings.TrimSpace(getenv("HOUSTON_TOKEN"))
	if token == "" {
		data, err := os.ReadFile(filepath.Join(home, ".config", "houston", "runner-token"))
		if err != nil {
			return nil, errors.New("no runner token: set HOUSTON_TOKEN, or put it in ~/.config/houston/runner-token (the installer writes it for the houston user)")
		}
		token = strings.TrimSpace(string(data))
	}
	return New(base, token), nil
}

type Variable struct {
	Name     string `json:"name"`
	Required bool   `json:"required"`
}

type DeployRule struct {
	On     string `json:"on"`
	Branch string `json:"branch"`
	Tags   string `json:"tags"`
}

// SyncRequest is what houston deploy read from compose.yml.
type SyncRequest struct {
	Name       string     `json:"name"`
	AppService string     `json:"app_service"`
	Services   []string   `json:"services"`
	Domains    []string   `json:"domains"`
	Variables  []Variable `json:"variables"`
	Health     string     `json:"health"`
	Port       int        `json:"port"`
	DeployRule DeployRule `json:"deploy_rule"`
	Volumes    []Volume   `json:"volumes,omitempty"`
	Databases  []Database `json:"databases,omitempty"`
	Backups    *Backups   `json:"backups,omitempty"`
}

// Backups are x-houston.backups: when to back up, and what to keep.
type Backups struct {
	Schedule   string `json:"schedule"`
	KeepAuto   int    `json:"keep_auto"`
	KeepDeploy int    `json:"keep_deploy"`
}

// Volume is one of the app's named volumes: what a backup copies as files.
type Volume struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// Database is a Postgres service: a backup dumps its databases.
type Database struct {
	Service string `json:"service"`
	Image   string `json:"image"`
}

type SyncResult struct {
	Project string                 `json:"project"`
	Host    string                 `json:"host"` // <name>.<base>
	DNS     string                 `json:"dns"`
	Domains map[string]DomainState `json:"domains"`
}

// DomainState is a custom domain's DNS after a sync: DNS OK, DNS PENDING,
// WILDCARD, ZONE NOT IN CLOUDFLARE YET, NO-GO, or CAN'T CHECK.
type DomainState struct {
	State  string `json:"state"`
	Reason string `json:"reason"`
}

// Sync saves the project in Mission Control and points its DNS. A HOLD is a
// *HoldError.
func (c *Client) Sync(ctx context.Context, req SyncRequest) (SyncResult, error) {
	var res SyncResult
	status, body, err := c.do(ctx, http.MethodPost, "/api/projects/sync", nil, req)
	if err != nil {
		return res, err
	}
	if status == http.StatusUnprocessableEntity {
		var hold struct {
			Error   string   `json:"error"`
			Missing []string `json:"missing"`
		}
		if json.Unmarshal(body, &hold) == nil && len(hold.Missing) > 0 {
			return res, &HoldError{Missing: hold.Missing, Message: hold.Error}
		}
	}
	if err := expect(status, body, http.StatusOK); err != nil {
		return res, err
	}
	return res, json.Unmarshal(body, &res)
}

// Secret returns a variable's value; false when it has none.
func (c *Client) Secret(ctx context.Context, project, key string) (string, bool, error) {
	status, body, err := c.do(ctx, http.MethodGet, "/api/projects/"+url.PathEscape(project)+"/secrets/"+url.PathEscape(key), nil, nil)
	if err != nil {
		return "", false, err
	}
	if status == http.StatusNotFound {
		return "", false, nil
	}
	if err := expect(status, body, http.StatusOK); err != nil {
		return "", false, err
	}
	return string(body), true, nil
}

// Deploy is a started deploy: its token is what reports on it.
type Deploy struct {
	ID       int    `json:"id"`
	Number   int    `json:"number"`
	Token    string `json:"token"`
	TookOver int    `json:"took_over"` // the silent deploy this one replaced, or 0
}

// StartDeploy starts the project's next deploy. Another in flight is a
// *BusyError.
func (c *Client) StartDeploy(ctx context.Context, project, sha, ref string) (Deploy, error) {
	var d Deploy
	status, body, err := c.do(ctx, http.MethodPost, "/api/projects/"+url.PathEscape(project)+"/deploys", nil, map[string]string{"sha": sha, "ref": ref})
	if err != nil {
		return d, err
	}
	if status == http.StatusConflict {
		var busy struct {
			Error  string `json:"error"`
			Number int    `json:"number"`
		}
		json.Unmarshal(body, &busy)
		return d, &BusyError{Number: busy.Number, Message: busy.Error}
	}
	if err := expect(status, body, http.StatusCreated); err != nil {
		return d, err
	}
	return d, json.Unmarshal(body, &d)
}

// Progress is one report: any of a step, a log chunk, a result.
type Progress struct {
	Step   string `json:"step,omitempty"`
	Log    string `json:"log,omitempty"`
	Status string `json:"status,omitempty"` // go or no_go
	Error  string `json:"error,omitempty"`
}

// Report sends progress on d. ErrTakenOver means stop.
func (c *Client) Report(ctx context.Context, d Deploy, p Progress) error {
	status, body, err := c.do(ctx, http.MethodPatch, "/api/deploys/"+strconv.Itoa(d.ID), map[string]string{"X-Houston-Deploy-Token": d.Token}, p)
	if err != nil {
		return err
	}
	if status == http.StatusConflict {
		return fmt.Errorf("%w: %s", ErrTakenOver, message(body))
	}
	return expect(status, body, http.StatusOK)
}

func (c *Client) do(ctx context.Context, method, path string, headers map[string]string, payload any) (int, []byte, error) {
	var reqBody io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return 0, nil, err
		}
		reqBody = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.URL+path, reqBody)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("can't reach Mission Control at %s: %w", c.URL, err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return 0, nil, err
	}
	if res.StatusCode == http.StatusUnauthorized {
		return 0, nil, ErrUnauthorized
	}
	return res.StatusCode, body, nil
}

func expect(status int, body []byte, want int) error {
	if status == want {
		return nil
	}
	return fmt.Errorf("Mission Control answered %d: %s", status, message(body))
}

func message(body []byte) string {
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &e) == nil && e.Error != "" {
		return e.Error
	}
	return strings.TrimSpace(string(body))
}

// Snapshot is a deploy's pre-deploy snapshot, as Mission Control runs it:
// queued, running, go, no_go, or skipped (Error says why).
type Snapshot struct {
	ID         int    `json:"id"`
	Status     string `json:"status"`
	SnapshotID string `json:"snapshot_id"`
	SHA        string `json:"sha"` // the version that was serving
	Bytes      int64  `json:"bytes"`
	Error      string `json:"error"`
}

// Snapshot asks for d's pre-deploy snapshot (a retry gets the same one).
func (c *Client) Snapshot(ctx context.Context, d Deploy) (Snapshot, error) {
	return c.snapshot(ctx, http.MethodPost, d)
}

// SnapshotStatus is d's pre-deploy snapshot as it stands.
func (c *Client) SnapshotStatus(ctx context.Context, d Deploy) (Snapshot, error) {
	return c.snapshot(ctx, http.MethodGet, d)
}

func (c *Client) snapshot(ctx context.Context, method string, d Deploy) (Snapshot, error) {
	var s Snapshot
	status, body, err := c.do(ctx, method, "/api/deploys/"+strconv.Itoa(d.ID)+"/snapshot", map[string]string{"X-Houston-Deploy-Token": d.Token}, nil)
	if err != nil {
		return s, err
	}
	if status != http.StatusOK && status != http.StatusAccepted {
		return s, errors.New(message(body))
	}
	return s, json.Unmarshal(body, &s)
}

// JobProject is what a runner needs to fetch a claimed deploy's commit.
type JobProject struct {
	Name        string `json:"name"`
	RepoURL     string `json:"repo_url"`
	Branch      string `json:"branch"`
	ComposePath string `json:"compose_path"`
	DeployKey   string `json:"deploy_key"`
}

// Job is a claimed deploy: the deploy (its token is the runner's), the
// commit and ref, the project, and the host keys Mission Control recorded
// for the repo's host.
type Job struct {
	Deploy     Deploy
	SHA        string
	Ref        string
	Project    JobProject
	KnownHosts []string
}

// Claim long-polls for the next queued deploy (up to wait seconds). false:
// nothing to do.
func (c *Client) Claim(ctx context.Context, runner string, wait int) (Job, bool, error) {
	status, body, err := c.do(ctx, http.MethodPost, "/api/runner/jobs/claim", nil, map[string]any{"runner": runner, "wait": wait})
	if err != nil {
		return Job{}, false, err
	}
	if status == http.StatusNoContent {
		return Job{}, false, nil
	}
	if err := expect(status, body, http.StatusOK); err != nil {
		return Job{}, false, err
	}
	var raw struct {
		Deploy struct {
			ID       int    `json:"id"`
			Number   int    `json:"number"`
			Token    string `json:"token"`
			SHA      string `json:"sha"`
			Ref      string `json:"ref"`
			TookOver int    `json:"took_over"`
		} `json:"deploy"`
		Project    JobProject `json:"project"`
		KnownHosts []string   `json:"known_hosts"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return Job{}, false, err
	}
	return Job{
		Deploy: Deploy{ID: raw.Deploy.ID, Number: raw.Deploy.Number, Token: raw.Deploy.Token, TookOver: raw.Deploy.TookOver},
		SHA:    raw.Deploy.SHA, Ref: raw.Deploy.Ref, Project: raw.Project, KnownHosts: raw.KnownHosts,
	}, true, nil
}
