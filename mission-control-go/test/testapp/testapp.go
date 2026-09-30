// Package testapp is the app on a test database, for tests of its routes.
package testapp

import (
	"io"
	"log/slog"
	"testing"

	"github.com/scttymn/gantry/crypt"
	"github.com/scttymn/gantry/jobs"
	"github.com/scttymn/gantry/live"
	"github.com/scttymn/gantry/sign"

	"github.com/scttymn/houston/mission-control-go/app"
	"github.com/scttymn/houston/mission-control-go/test"
)

// RunnerToken is the test app's HOUSTON_RUNNER_TOKEN.
const RunnerToken = "runner-token-0123456789abcdef0123456789"

// New is the app on a test database, its jobs defined (they run when the
// test calls a.Jobs.Drain), with encryption keys of its own.
func New(t testing.TB) *app.App {
	t.Helper()
	keys, _ := crypt.ParseKeys(crypt.NewKey())
	if err := crypt.Use(keys...); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { crypt.Use() })
	d := test.DB(t)
	q, err := jobs.New(t.Context(), d, jobs.Options{})
	if err != nil {
		t.Fatal(err)
	}
	signer := sign.Signer{Key: []byte("test-key")}
	a := &app.App{DB: d, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Signer: signer, Jobs: q,
		Live: live.New(signer, live.Options{}), Identity: app.Identity("test-secret-key-base"), RunnerToken: RunnerToken}
	if err := a.DefineJobs(); err != nil {
		t.Fatal(err)
	}
	return a
}
