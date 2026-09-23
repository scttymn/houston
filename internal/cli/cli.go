// Package cli is Houston's command line: commands, flags and exit codes.
package cli

import (
	"fmt"
	"io"
	"os"

	"github.com/sevenmoons/houston/internal/docker"
	"github.com/sevenmoons/houston/internal/project"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// Exit codes. Commands that run a child process return the child's code.
const (
	exitFailure = 1 // Houston couldn't do the work (Docker missing, can't write files)
	exitUsage   = 2 // bad flags or a bad compose file
)

// Main runs the houston command line and returns the process exit code.
func Main(args []string, stdin io.Reader, stdout, stderr io.Writer, d docker.Runner) int {
	var file string
	var follow bool
	code := 0

	root := &cobra.Command{
		Use:           "houston",
		Short:         "Run and deploy a project described by compose.yml + x-houston",
		SilenceUsage:  true,
		SilenceErrors: true,
		// Like docker compose, -f/--file goes before the command
		// (houston -f other.yml dev), which frees -f for `logs -f`.
		TraverseChildren: true,
		// With TraverseChildren cobra hands unknown commands to root as
		// arguments, so root rejects them itself; plain `houston` shows help.
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				return fmt.Errorf("unknown command %q for %q", args[0], cmd.CommandPath())
			}
			return cmd.Help()
		},
	}
	root.Flags().StringVarP(&file, "file", "f", "compose.yml", "the project's compose file (before the command)")

	command := func(use, short string, run func() int) *cobra.Command {
		return &cobra.Command{
			Use:   use,
			Short: short,
			Args:  cobra.NoArgs,
			RunE: func(*cobra.Command, []string) error {
				code = run()
				return nil
			},
		}
	}
	root.AddCommand(
		command("dev", "Run the project locally (dev build target, code mounted)", func() int {
			return runDev(file, stderr, d)
		}),
		command("test", "Run x-houston.commands.test in a throwaway copy of the project", func() int {
			return runTest(file, stdout, stderr, d)
		}),
		command("console", "Run x-houston.commands.console in the running app", func() int {
			return runConsole(file, stderr, d)
		}),
	)
	root.AddCommand(command("init", "Set up this Rails app for Houston (compose.yml, Dockerfile stages, .env)", func() int {
		return runInit(file, stdin, stdout, stderr)
	}))
	logs := command("logs", "Show the app's logs", func() int {
		return runLogs(file, follow, stderr, d)
	})
	logs.Flags().BoolVarP(&follow, "follow", "f", false, "keep following new output")
	root.AddCommand(logs)

	root.SetArgs(args)
	root.SetOut(stdout)
	root.SetErr(stderr)

	if err := root.Execute(); err != nil {
		fmt.Fprintf(stderr, "houston: %v\nRun 'houston --help' for usage.\n", err)
		return exitUsage
	}
	return code
}

// loadProject loads the compose file, printing every problem on failure.
func loadProject(file string, stderr io.Writer) (*project.Project, bool) {
	p, err := project.Load(file)
	if err != nil {
		fmt.Fprint(stderr, err)
		return nil, false
	}
	return p, true
}

// stdinIsTerminal reports whether Houston's stdin is a terminal. Tests
// replace it.
var stdinIsTerminal = func() bool { return isTerminal(os.Stdin) }

// isTerminal asks the OS whether f is a terminal. A character-device check
// isn't enough: /dev/null is one too.
func isTerminal(f *os.File) bool { return term.IsTerminal(int(f.Fd())) }
