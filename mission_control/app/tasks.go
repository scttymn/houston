package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/scttymn/gantry/db"

	"github.com/scttymn/houston/mission_control/app/models"
	"github.com/scttymn/houston/mission_control/app/setup"
)

// Tasks are the app's own commands: `mission-control task NAME
// [ARGS...]` runs one, and `gantry task NAME` runs it in development. Add
// one here:
//
//	{Name: "backfill-slugs", Help: "Give every post a slug", Run: backfillSlugs},
var Tasks = []Task{
	{Name: "setup-code", Help: "Print a new first-run setup code (only until the admin exists)", Run: setupCode},
	{Name: "token", Help: "Print a new personal API token named NAME (as Settings › Tokens issues one)", Run: newToken},
	{Name: "setup", Help: "Print first run's next step: admin, cloudflare, storage, or done", Run: setupState},
	{Name: "port", Help: "Print whether port 3000 is open or closed (Settings › Security), for the installer", Run: portState},
}

// setupCode is what the installer prints next to the LAN address.
func setupCode(ctx context.Context, env TaskEnv, _ []string) error {
	code, err := models.IssueSetupCode(ctx, env.DB)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(env.Out, code)
	return err
}

// Task is one of the app's commands.
type Task struct {
	Name string
	Help string // one line, for `mission-control tasks`
	Run  func(ctx context.Context, env TaskEnv, args []string) error
}

// TaskEnv is what a task works with: the migrated database, a logger, and
// where to print.
type TaskEnv struct {
	DB  *db.DB
	Log *slog.Logger
	Out io.Writer
}

// portState is the choice saved in Settings › Security: "open" (as a new
// install is) or "closed". The installer binds port 3000 by it.
func portState(ctx context.Context, env TaskEnv, _ []string) error {
	inst, err := models.New(env.DB.Read).CurrentInstallation(ctx)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	state := "open"
	if err == nil && !inst.PortOpen {
		state = "closed"
	}
	_, err = fmt.Fprintln(env.Out, state)
	return err
}

// newToken is a personal API token for someone at the server's shell (the
// CLI's, or a script's), shown once as Settings › Tokens shows it.
func newToken(ctx context.Context, env TaskEnv, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: task token NAME")
	}
	token, _, err := models.IssueToken(ctx, models.New(env.DB.Write), args[0])
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(env.Out, token)
	return err
}

// setupState is where first run stands, for the installer's report: the
// step to do next, or done.
func setupState(ctx context.Context, env TaskEnv, _ []string) error {
	state := "admin"
	switch set, err := models.New(env.DB.Read).UserExists(ctx); {
	case err != nil:
		return err
	case set:
		step, err := setup.NextStep(ctx, env.DB)
		if err != nil {
			return err
		}
		state = strings.TrimPrefix(step, "/setup/")
		if step == "" {
			state = "done"
		}
	}
	_, err := fmt.Fprintln(env.Out, state)
	return err
}
