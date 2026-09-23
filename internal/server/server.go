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
		return fmt.Errorf("Mission Control answered %d: %s", status, message(body))
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
