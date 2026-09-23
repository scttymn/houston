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

// version is stamped at build time with -ldflags "-X .../internal/cli.version=…".
var version = "dev"

// Exit codes. Commands that run a child process return the child's code.
const (
	exitFailure = 1 // Houston couldn't do the work (Docker missing, can't write files)
	exitUsage   = 2 // bad flags or a bad compose file
)

// Main runs the houston command line and returns the process exit code.
func Main(args []string, stdin io.Reader, stdout, stderr io.Writer, d docker.Runner) int {
	var file string
	var follow, production bool
	code := 0

	root := &cobra.Command{
		Use:           "houston",
		Short:         "Run and deploy a project described by compose.yml + x-houston",
		SilenceUsage:  true,
		SilenceErrors: true,
		// Like docker compose, -f/--file goes before the command
		// (houston -f other.yml dev), which frees -f for `logs -f`.
		TraverseChildren: true,
		Version:          version,
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
	root.CompletionOptions.DisableDefaultCmd = true

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
	dev := command("dev", "Run the project locally (dev build target, code mounted)", func() int {
		return runDev(file, production, stderr, d)
	})
	dev.Flags().BoolVar(&production, "production", false, "run the production build target instead, with the code baked into the image")
	root.AddCommand(
		dev,
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
	var asJSON bool
	inspect := command("inspect", "Show what Houston reads from the compose file (what Mission Control is told)", func() int {
		return runInspect(file, asJSON, stdout, stderr)
	})
	inspect.Flags().BoolVar(&asJSON, "json", false, "print JSON, as Mission Control reads it")
	root.AddCommand(inspect)
	var ref string
	deployCmd := command("deploy", "Deploy this checkout on a Houston server (run there, as the houston user)", func() int {
		return runDeploy(file, ref, stdout, stderr, d)
	})
	deployCmd.Flags().StringVar(&ref, "ref", "", "what's being deployed (refs/heads/<branch> or refs/tags/<tag>); default: the checked-out branch")
	root.AddCommand(deployCmd)
	var runnerName, workspace string
	runnerCmd := command("runner", "Claim and run queued deploys (in a houston-runner-N container)", func() int {
		return runRunner(runnerName, workspace, stderr, d)
	})
	runnerCmd.Flags().StringVar(&runnerName, "name", "", "this runner's name, houston-runner-N")
	runnerCmd.Flags().StringVar(&workspace, "workspace", "", "where checkouts and deploy keys live (the same path on the host)")
	runnerCmd.MarkFlagRequired("name")
	runnerCmd.MarkFlagRequired("workspace")
	root.AddCommand(runnerCmd)
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
