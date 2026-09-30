// Package port opens Mission Control's port 3000 to the network, or closes
// it to 127.0.0.1 (the Rails app's PortSwitch and PortExposure): for
// first-run setup, or on purpose. The choice is saved, and the installer
// reads it on every run. It's applied by having Mission Control recreated
// from its own compose.yml, which binds "${HOUSTON_BIND:-127.0.0.1}:3000:80":
// a one-off container from the runner image (it has docker compose) waits a
// moment, then runs compose with HOUSTON_BIND set, which recreates this one.
package port

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/scttymn/gantry/db"

	"github.com/scttymn/houston/mission-control-go/app/models"
	"github.com/scttymn/houston/mission-control-go/app/services/dockercmd"
	"github.com/scttymn/houston/mission-control-go/app/services/owncontainer"
)

// Helper is the one-off container's name.
const Helper = "houston-port-3000"

// Refused is a switch that can't be made now, in words.
type Refused string

func (r Refused) Error() string { return string(r) }

// Port is the switch, and what the port is bound to, read at most hourly:
// the bindings can't change without recreating the container, and an
// unknown answer isn't kept.
type Port struct {
	DB          *db.DB
	Own         owncontainer.Own
	RunnerImage string // HOUSTON_RUNNER_IMAGE

	mu      sync.Mutex
	address string
	readAt  time.Time
}

// Address is what port 3000 is bound to ("0.0.0.0", "127.0.0.1", ...), or
// "" when unknown.
func (p *Port) Address(ctx context.Context) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.address != "" && time.Since(p.readAt) < time.Hour {
		return p.address
	}
	p.address, p.readAt = p.Own.PortAddress(ctx), time.Now()
	return p.address
}

// Set saves the choice and starts the helper; Refused, with nothing saved,
// when it can't be applied.
func (p *Port) Set(ctx context.Context, open bool) error {
	file, dir := p.Own.ComposeFile(ctx)
	if file == "" || dir == "" {
		return Refused("can't change it from here: this Mission Control wasn't started by Houston's installer")
	}
	if p.RunnerImage == "" {
		return Refused("can't change it from here yet: run the installer once more (it tells Mission Control the runner image to do it with)")
	}
	bind := "127.0.0.1"
	if open {
		bind = "0.0.0.0"
	}
	ran := p.Own.Docker.Run(ctx, []string{"run", "-d", "--rm", "--name", Helper, "--user", "0", "-e", "HOUSTON_BIND=" + bind,
		"-v", "/var/run/docker.sock:/var/run/docker.sock", "-v", dir + ":" + dir + ":ro",
		"--entrypoint", "sh", p.RunnerImage, "-c", "sleep 5 && exec docker compose -f " + file + " up -d --no-deps mission-control"},
		dockercmd.Opts{Timeout: 30 * time.Second})
	if !ran.OK {
		return Refused("can't change it right now: " + lastLine(ran.Output))
	}
	return models.New(p.DB.Write).SetPortOpen(ctx, models.SetPortOpenParams{PortOpen: open, UpdatedAt: time.Now()})
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}
