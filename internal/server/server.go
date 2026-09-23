// Package server is the houston CLI's client for Mission Control's remote
// API (/api/v1), with a named personal token: houston --server commands, and
// agents. It never sees a secret's value; that stays on the runner's local
// API (internal/mission).
package server

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
	"strings"
	"time"
)

// ErrRefused: Mission Control refused the API token.
var ErrRefused = errors.New("the API token was refused (revoked?); run houston login")

// Config is where the server is and how to reach it.
type Config struct {
	URL                string `json:"url"`
	Token              string `json:"token"`
	AccessClientID     string `json:"access_client_id,omitempty"`
	AccessClientSecret string `json:"access_client_secret,omitempty"`
}

func configPath(home string) string { return filepath.Join(home, ".config", "houston", "server.json") }

// Load reads HOUSTON_SERVER and HOUSTON_API_TOKEN (agents: both, no file
// needed), else the file houston login saved. HOUSTON_ACCESS_CLIENT_ID and
// _SECRET add Cloudflare Access service-token headers either way.
func Load(getenv func(string) string, home string) (Config, error) {
	var c Config
	url, token := getenv("HOUSTON_SERVER"), getenv("HOUSTON_API_TOKEN")
	switch {
	case url != "" && token != "":
		c = Config{URL: url, Token: token}
	case url != "" || token != "":
		return c, errors.New("set both HOUSTON_SERVER and HOUSTON_API_TOKEN (or neither, and run houston login)")
	default:
		data, err := os.ReadFile(configPath(home))
		if err != nil {
			return c, errors.New("not logged in to a server: run houston login <url>, or set HOUSTON_SERVER and HOUSTON_API_TOKEN")
		}
		if err := json.Unmarshal(data, &c); err != nil {
			return c, fmt.Errorf("%s is unreadable: %v; run houston login again", configPath(home), err)
		}
	}
	if id := getenv("HOUSTON_ACCESS_CLIENT_ID"); id != "" {
		c.AccessClientID, c.AccessClientSecret = id, getenv("HOUSTON_ACCESS_CLIENT_SECRET")
	}
	c.URL = strings.TrimRight(c.URL, "/")
	return c, nil
}

// Save writes the config for the current user only (dir 0700, file 0600),
// replacing any earlier one in one step.
func Save(home string, c Config) error {
	dir := filepath.Dir(configPath(home))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".server-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), configPath(home))
}

// Remove forgets the saved server.
func Remove(home string) error {
	err := os.Remove(configPath(home))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

type Client struct {
	c    Config
	http *http.Client
}

func New(c Config) *Client { return &Client{c: c, http: &http.Client{Timeout: 30 * time.Second}} }

// Me is the token's name and the server's base domain.
type Me struct {
	Token  string `json:"token"`
	Server string `json:"server"`
}

func (cl *Client) Me(ctx context.Context) (Me, error) {
	var me Me
	return me, cl.getJSON(ctx, "/api/v1/me", &me)
}

func (cl *Client) getJSON(ctx context.Context, path string, into any) error {
	status, body, err := cl.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("%s", message(body))
	}
	return json.Unmarshal(body, into)
}

func (cl *Client) do(ctx context.Context, method, path string, payload any) (int, []byte, error) {
	var reqBody io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return 0, nil, err
		}
		reqBody = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, cl.c.URL+path, reqBody)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+cl.c.Token)
	req.Header.Set("Content-Type", "application/json")
	if cl.c.AccessClientID != "" {
		req.Header.Set("CF-Access-Client-Id", cl.c.AccessClientID)
		req.Header.Set("CF-Access-Client-Secret", cl.c.AccessClientSecret)
	}
	res, err := cl.http.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("can't reach %s: %w", cl.c.URL, err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 16<<20))
	if err != nil {
		return 0, nil, err
	}
	if res.StatusCode == http.StatusUnauthorized {
		return 0, nil, ErrRefused
	}
	return res.StatusCode, body, nil
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

// DomainState is a custom domain's DNS: DNS OK, DNS PENDING, WILDCARD, …
type DomainState struct {
	State  string `json:"state"`
	Reason string `json:"reason"`
}

type Deploy struct {
	Number     int     `json:"number"`
	Status     string  `json:"status"` // queued, in_flight, go, no_go
	SHA        string  `json:"sha"`
	Ref        string  `json:"ref"`
	Step       string  `json:"step"`
	Error      string  `json:"error"`
	Runner     string  `json:"runner"`
	StartedAt  string  `json:"started_at"`
	FinishedAt *string `json:"finished_at"`
	Duration   int     `json:"duration"`
	// With a single deploy: its steps and a piece of its log.
	Steps   []Step `json:"steps"`
	Log     string `json:"log"`
	LogSize int    `json:"log_size"`
	LogNext int    `json:"log_next"`
}

type Step struct {
	Name  string `json:"name"`
	State string `json:"state"` // done, current, failed, pending, skipped
}

type SecretSummary struct {
	Name     string `json:"name"`
	Required bool   `json:"required"`
	Set      bool   `json:"set"`
}

type Project struct {
	Name       string                 `json:"name"`
	Status     string                 `json:"status"` // queued, in_flight, go, no_go, standby
	RunningSHA string                 `json:"running_sha"`
	Host       string                 `json:"host"`
	Domains    map[string]DomainState `json:"domains"`
	LastDeploy *Deploy                `json:"last_deploy"`
	// Only for a single project:
	DeployRule struct {
		On     string `json:"on"`
		Branch string `json:"branch"`
		Tags   string `json:"tags"`
	} `json:"deploy_rule"`
	Services        []string        `json:"services"`
	RepoURL         string          `json:"repo_url"`
	Branch          string          `json:"branch"`
	WebhookVerified bool            `json:"webhook_verified"`
	Secrets         []SecretSummary `json:"secrets"`
}

func (cl *Client) Projects(ctx context.Context) ([]Project, error) {
	var body struct {
		Projects []Project `json:"projects"`
	}
	return body.Projects, cl.getJSON(ctx, "/api/v1/projects", &body)
}

func (cl *Client) Project(ctx context.Context, name string) (Project, error) {
	var p Project
	return p, cl.getJSON(ctx, "/api/v1/projects/"+url.PathEscape(name), &p)
}

func (cl *Client) Deploys(ctx context.Context, project string, page int) ([]Deploy, error) {
	var body struct {
		Deploys []Deploy `json:"deploys"`
	}
	return body.Deploys, cl.getJSON(ctx, fmt.Sprintf("/api/v1/projects/%s/deploys?page=%d", url.PathEscape(project), page), &body)
}

// Deploy is one deploy (a number, or "latest") with its log from byte from.
func (cl *Client) Deploy(ctx context.Context, project, number string, from int) (Deploy, error) {
	var d Deploy
	return d, cl.getJSON(ctx, fmt.Sprintf("/api/v1/projects/%s/deploys/%s?log_from=%d", url.PathEscape(project), url.PathEscape(number), from), &d)
}

// Raw is the API's JSON for path, for --json.
func (cl *Client) Raw(ctx context.Context, path string) ([]byte, error) {
	status, body, err := cl.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("%s", message(body))
	}
	return body, nil
}

func (cl *Client) Secrets(ctx context.Context, project string) ([]SecretSummary, error) {
	var body struct {
		Secrets []SecretSummary `json:"secrets"`
	}
	return body.Secrets, cl.getJSON(ctx, "/api/v1/projects/"+url.PathEscape(project)+"/secrets", &body)
}

// SetSecret sends a value; Mission Control never sends one back.
func (cl *Client) SetSecret(ctx context.Context, project, key, value string) error {
	return cl.send(ctx, http.MethodPut, secretPath(project, key), map[string]string{"value": value})
}

func (cl *Client) UnsetSecret(ctx context.Context, project, key string) error {
	return cl.send(ctx, http.MethodDelete, secretPath(project, key), nil)
}

// GenerateSecret stores a random value nobody sees.
func (cl *Client) GenerateSecret(ctx context.Context, project, key string) error {
	return cl.send(ctx, http.MethodPost, secretPath(project, key)+"/generate", nil)
}

func secretPath(project, key string) string {
	return "/api/v1/projects/" + url.PathEscape(project) + "/secrets/" + url.PathEscape(key)
}

func (cl *Client) send(ctx context.Context, method, path string, payload any) error {
	status, body, err := cl.do(ctx, method, path, payload)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("%s", message(body))
	}
	return nil
}
