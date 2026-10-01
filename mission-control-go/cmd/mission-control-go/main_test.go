package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/scttymn/gantry/crypt"
	"github.com/scttymn/gantry/db"

	"github.com/scttymn/houston/mission-control-go/config"
)

// A task reads what the server does, its encrypted columns too: the
// installer asks `task port` on a server whose Cloudflare token is saved.
func TestTaskReadsEncrypted(t *testing.T) {
	ctx := context.Background()
	key := crypt.NewKey()
	cfg := config.Load(func(k string) string {
		return map[string]string{"GANTRY_ENV": "test", "ENCRYPTION_KEYS": key, "DATABASE_URL": "sqlite://" + filepath.Join(t.TempDir(), "mc.sqlite3")}[k]
	})
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	var out, errOut bytes.Buffer
	if code := command(ctx, cfg, logger, []string{"task", "port"}, nil, &out, &errOut); code != 0 || out.String() != "open\n" {
		t.Fatalf("before setup: %d %q %q", code, out.String(), errOut.String())
	}
	keys, _ := crypt.ParseKeys(key)
	crypt.Use(keys...)
	d, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.Exec(`INSERT INTO installations (id, port_open, cloudflare_api_token) VALUES (1, FALSE, ?)`, crypt.Of("cf-token")); err != nil {
		t.Fatal(err)
	}
	d.Close()
	crypt.Use()
	out.Reset()
	if code := command(ctx, cfg, logger, []string{"task", "port"}, nil, &out, &errOut); code != 0 || out.String() != "closed\n" {
		t.Errorf("with a saved token: %d %q %q", code, out.String(), errOut.String())
	}
}
