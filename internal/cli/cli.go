// Package cli is Houston's command line: commands, flags and exit codes.
package cli

import (
	"fmt"
	"io"

	"github.com/sevenmoons/houston/internal/docker"
	"github.com/spf13/cobra"
)

// Exit codes. Commands that run a child process return the child's code.
const (
	exitFailure = 1 // Houston couldn't do the work (Docker missing, can't write files)
	exitUsage   = 2 // bad flags or a bad compose file
)

// Main runs the houston command line and returns the process exit code.
func Main(args []string, stdout, stderr io.Writer, d docker.Runner) int {
	var file string
	code := 0

	root := &cobra.Command{
		Use:           "houston",
		Short:         "Run and deploy a project described by compose.yml + x-houston",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVarP(&file, "file", "f", "compose.yml", "the project's compose file")
	root.AddCommand(&cobra.Command{
		Use:   "dev",
		Short: "Run the project locally (dev build target, code mounted)",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			code = runDev(file, stderr, d)
			return nil
		},
	})
	root.SetArgs(args)
	root.SetOut(stdout)
	root.SetErr(stderr)

	if err := root.Execute(); err != nil {
		fmt.Fprintf(stderr, "houston: %v\nRun 'houston --help' for usage.\n", err)
		return exitUsage
	}
	return code
}
