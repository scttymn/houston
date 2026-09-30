// Package config is the app's settings, from the environment: each has a
// default, so a blank one falls back.
package config

import "os"

// Config is what the environment sets.
type Config struct {
	Env         string // GANTRY_ENV: development, test or production (the default)
	Addr        string // ADDR, default :8080
	DataDir     string // DATA_DIR: the database and uploads; default /data
	DatabaseURL string // DATABASE_URL; default sqlite://DATA_DIR/mission-control-go.sqlite3
	SecretKey   string // SECRET_KEY, else SECRET_KEY_BASE: signs cookies and forms; blank: a key kept in the database
	// SecretKeyBase is the Rails app's SECRET_KEY_BASE, from the server's
	// .env: /ping's identity is derived from it.
	SecretKeyBase string
	// RunnerToken is HOUSTON_RUNNER_TOKEN: the runner API's bearer token.
	RunnerToken string
	// MissionControlURL and AppsURL are where the tunnel sends Mission
	// Control's hosts and everything else (HOUSTON_MISSION_CONTROL_URL,
	// default http://mission-control:8080; HOUSTON_APPS_URL, default
	// http://kamal-proxy:80).
	MissionControlURL, AppsURL string
	// KnownHosts is the git hosts' keys Mission Control records
	// (HOUSTON_KNOWN_HOSTS, default DATA_DIR/known_hosts).
	KnownHosts string
	// ToolsImage makes volumes' directories (HOUSTON_TOOLS_IMAGE).
	ToolsImage string
	// TunnelHost is cloudflared's name on the Docker network
	// (HOUSTON_TUNNEL_HOST, default cloudflared).
	TunnelHost string
	// EncryptionKeys are gantry's crypt keys (ENCRYPTION_KEYS, crypt.NewKey
	// makes one; the newest first). Development and tests have their own.
	EncryptionKeys string
	// RailsKeys are the Rails app's Active Record encryption keys
	// (AR_ENCRYPTION_PRIMARY_KEY, AR_ENCRYPTION_KEY_DERIVATION_SALT), for
	// the one-time move from its database.
	RailsPrimaryKey, RailsKeySalt string
	// JobsInServer runs the background jobs in the server's process (Rails 8's
	// Solid Queue in Puma). JOBS_IN_SERVER=false leaves them to a process of
	// their own, `mission-control-go jobs`.
	JobsInServer bool
}

// Development is true where the code is mounted and changes as you work:
// db migrate rewrites db/schema.sql there, and db reset is allowed.
func (c Config) Development() bool { return c.Env == "development" }

// Load reads the environment through getenv (os.Getenv, or a test's).
func Load(getenv func(string) string) Config {
	or := func(name, fallback string) string {
		if v := getenv(name); v != "" {
			return v
		}
		return fallback
	}
	c := Config{
		Env:               or("GANTRY_ENV", "production"),
		Addr:              or("ADDR", ":8080"),
		DataDir:           or("DATA_DIR", "/data"),
		SecretKey:         or("SECRET_KEY", getenv("SECRET_KEY_BASE")),
		SecretKeyBase:     getenv("SECRET_KEY_BASE"),
		RunnerToken:       getenv("HOUSTON_RUNNER_TOKEN"),
		TunnelHost:        or("HOUSTON_TUNNEL_HOST", "cloudflared"),
		ToolsImage:        or("HOUSTON_TOOLS_IMAGE", "houston/mission-control:local"),
		MissionControlURL: or("HOUSTON_MISSION_CONTROL_URL", "http://mission-control:8080"),
		AppsURL:           or("HOUSTON_APPS_URL", "http://kamal-proxy:80"),
		JobsInServer:      getenv("JOBS_IN_SERVER") != "false",
	}
	c.DatabaseURL = or("DATABASE_URL", "sqlite://"+c.DataDir+"/mission-control-go.sqlite3")
	c.KnownHosts = or("HOUSTON_KNOWN_HOSTS", c.DataDir+"/known_hosts")
	c.EncryptionKeys = getenv("ENCRYPTION_KEYS")
	if c.EncryptionKeys == "" && c.Env != "production" {
		c.EncryptionKeys = devEncryptionKey
	}
	c.RailsPrimaryKey, c.RailsKeySalt = getenv("AR_ENCRYPTION_PRIMARY_KEY"), getenv("AR_ENCRYPTION_KEY_DERIVATION_SALT")
	return c
}

// devEncryptionKey encrypts development's and tests' secrets (as the Rails
// app's development keys do); production's come from ENCRYPTION_KEYS.
const devEncryptionKey = "ZGV2LW9ubHkta2V5LW5vdC1mb3ItcHJvZHVjdGlvbiE="

// FromEnv is the process's settings.
func FromEnv() Config { return Load(os.Getenv) }
