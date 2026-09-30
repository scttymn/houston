package app

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	"github.com/scttymn/gantry/db"

	"github.com/scttymn/houston/mission-control-go/app/models"
)

// Tasks are the app's own commands, Rails' rake tasks: `mission-control-go task NAME
// [ARGS...]` runs one, and `gantry task NAME` runs it in development. Add
// one here:
//
//	{Name: "backfill-slugs", Help: "Give every post a slug", Run: backfillSlugs},
var Tasks = []Task{
	{Name: "setup-code", Help: "Print a new first-run setup code (only until the admin exists)", Run: setupCode},
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
	Help string // one line, for `mission-control-go tasks`
	Run  func(ctx context.Context, env TaskEnv, args []string) error
}

// TaskEnv is what a task works with: the migrated database, a logger, and
// where to print.
type TaskEnv struct {
	DB  *db.DB
	Log *slog.Logger
	Out io.Writer
}
