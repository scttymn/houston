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
	"github.com/scttymn/houston/mission-control-go/app/services/appstats"
	"github.com/scttymn/houston/mission-control-go/app/services/backup"
	"github.com/scttymn/houston/mission-control-go/app/services/cfsettings"
	"github.com/scttymn/houston/mission-control-go/app/services/dns"
	"github.com/scttymn/houston/mission-control-go/app/services/dockercmd/dockercmdtest"
	"github.com/scttymn/houston/mission-control-go/app/services/owncontainer"
	"github.com/scttymn/houston/mission-control-go/app/services/port"
	"github.com/scttymn/houston/mission-control-go/app/services/serverupdate"
	"github.com/scttymn/houston/mission-control-go/app/services/systemstatus"
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
	q, err := jobs.New(t.Context(), d, jobs.Options{Queues: app.Queues})
	if err != nil {
		t.Fatal(err)
	}
	signer := sign.Signer{Key: []byte("test-key")}
	fake := &dockercmdtest.Fake{}
	a := &app.App{DB: d, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Signer: signer, Jobs: q,
		Live: live.New(signer, live.Options{}), Identity: app.Identity("test-secret-key-base"), RunnerToken: RunnerToken, Version: "dev",
		DockerCLI: fake, Snapshots: &backup.Snapshots{Docker: fake}, Tools: "houston/mission-control:test", ToolsBin: "/app",
		Services: dns.Services{MissionControl: "http://mission-control:8080", Apps: "http://kamal-proxy:80"}}
	own := owncontainer.Own{Docker: fake, Hostname: "mc"}
	a.Updater = serverupdate.Updater{DB: d, Docker: fake, Own: own, Version: a.Version, Repo: "scttymn/houston", Log: a.Log}
	a.Port = &port.Port{DB: d, Own: own}
	a.CloudflareSettings = &cfsettings.Settings{DB: d, Services: a.Services}
	a.SystemStatus = &systemstatus.Status{Registry: "http://registry.invalid"}
	a.Stats = &appstats.Stats{Docker: fake, Log: a.Log, DiskSize: func() int64 { return 100 << 30 }}
	if err := a.DefineJobs(); err != nil {
		t.Fatal(err)
	}
	return a
}
