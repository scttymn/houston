// Package cli is Houston's command line: commands, flags and exit codes.
package cli

import (
	"fmt"
	"io"
	"os"

	"github.com/scttymn/houston/internal/docker"
	"github.com/scttymn/houston/internal/project"
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
	var devOpts devOptions
	dev := command("dev", "Run the project locally at http://<name>.localhost (a branch: <branch>.<name>.localhost), dev build target, code mounted", func() int {
		devOpts.production = production
		return runDev(file, devOpts, stderr, d)
	})
	dev.Flags().BoolVar(&production, "production", false, "run the production build target instead, with the code baked into the image")
	dev.Flags().BoolVar(&devOpts.keepPorts, "ports", false, "also publish compose.yml's ports, as plain docker compose does")
	dev.Flags().StringVar(&devOpts.as, "as", "", "name this instance (<name>.<project>.localhost) instead of taking the git branch")
	dev.Flags().BoolVar(&devOpts.fresh, "fresh", false, "on a branch: replace its data with a new copy of main's")
	var consoleOnServer bool
	consoleCmd := command("console", "Run x-houston.commands.console in the running app (--server: on the server, over SSH on the LAN)", func() int {
		if consoleOnServer {
			return runConsoleServer(file, stderr)
		}
		return runConsole(file, stderr, d)
	})
	consoleCmd.Flags().BoolVar(&consoleOnServer, "server", false, "in the app running on the server (SSH to houston@<LAN address>)")
	root.AddCommand(
		dev,
		command("test", "Run x-houston.commands.test in a throwaway copy of the project", func() int {
			return runTest(file, stdout, stderr, d)
		}),
		consoleCmd,
	)
	root.AddCommand(command("init", "Set up this folder for Houston: adds what's missing (Dockerfile stages, compose.yml, x-houston, .env) with defaults to edit", func() int {
		return runInit(file, stdin, stdout, stderr)
	}))
	var asJSON bool
	inspect := command("inspect", "Show what Houston reads from the compose file (what Mission Control is told)", func() int {
		return runInspect(file, asJSON, stdout, stderr)
	})
	inspect.Flags().BoolVar(&asJSON, "json", false, "print JSON, as Mission Control reads it")
	root.AddCommand(inspect)
	var ref, deployProject string
	var onServer, deployFollow bool
	deployCmd := command("deploy", "Deploy this checkout on a Houston server (there, as houston); --server: have the server deploy the repo's head", func() int {
		if onServer {
			return runDeployServer(file, deployProject, deployFollow, stdout, stderr)
		}
		return runDeploy(file, ref, stdout, stderr, d)
	})
	deployCmd.Flags().StringVar(&ref, "ref", "", "what's being deployed (refs/heads/<branch> or refs/tags/<tag>); default: the checked-out branch")
	deployCmd.Flags().BoolVar(&onServer, "server", false, "from anywhere: queue a deploy of the head of what the deploy rule matches")
	deployCmd.Flags().StringVar(&deployProject, "project", "", "with --server: the project (default: the compose file's name)")
	deployCmd.Flags().BoolVar(&deployFollow, "follow", false, "with --server: follow it to its result; exit 0 on GO, 1 on NO-GO")
	root.AddCommand(deployCmd)
	var accessID, accessSecret, loginSSH string
	login := &cobra.Command{
		Use:   "login [url]",
		Short: "Save Mission Control's URL and an API token for --server commands",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			code = runLogin(args, accessID, accessSecret, loginSSH, stdin, stdout, stderr)
			return nil
		},
	}
	login.Flags().StringVar(&accessID, "access-client-id", "", "a Cloudflare Access service token's client ID, if Access guards admin.<base>")
	login.Flags().StringVar(&accessSecret, "access-client-secret", "", "its client secret")
	login.Flags().StringVar(&loginSSH, "ssh", "", "for console --server: houston@<the server's LAN address>")
	root.AddCommand(login, command("logout", "Forget the saved server and token", func() int {
		return runLogout(stdout, stderr)
	}))
	var projectFlag string
	var statusJSON, deploysJSON, showJSON, followDeploy bool
	status := command("status", "A project's status on the server (every project, outside a project)", func() int {
		return runStatus(file, projectFlag, statusJSON, stdout, stderr)
	})
	status.Flags().StringVar(&projectFlag, "project", "", "the project (default: the compose file's name)")
	status.Flags().BoolVar(&statusJSON, "json", false, "print the API's JSON")
	deploys := command("deploys", "A project's deploy history on the server", func() int {
		return runDeploys(file, projectFlag, deploysJSON, stdout, stderr)
	})
	deploys.Flags().StringVar(&projectFlag, "project", "", "the project (default: the compose file's name)")
	deploys.Flags().BoolVar(&deploysJSON, "json", false, "print the API's JSON")
	show := &cobra.Command{
		Use:   "show [number]",
		Short: "A deploy's steps and log (default: the latest); --follow until it's finished",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			number := ""
			if len(args) > 0 {
				number = args[0]
			}
			code = runDeployShow(file, projectFlag, number, followDeploy, showJSON, stdout, stderr)
			return nil
		},
	}
	show.Flags().StringVar(&projectFlag, "project", "", "the project (default: the compose file's name)")
	show.Flags().BoolVar(&followDeploy, "follow", false, "keep printing its log until it's finished; exit 0 on GO, 1 on NO-GO")
	show.Flags().BoolVar(&showJSON, "json", false, "print the API's JSON")
	deploys.AddCommand(show)
	root.AddCommand(status, deploys)

	secrets := &cobra.Command{Use: "secrets", Short: "A project's secrets on the server (write-only)"}
	secrets.PersistentFlags().StringVar(&projectFlag, "project", "", "the project (default: the compose file's name)")
	oneKey := func(use, short string, run func(key string) int) *cobra.Command {
		return &cobra.Command{Use: use, Short: short, Args: cobra.ExactArgs(1), RunE: func(_ *cobra.Command, args []string) error {
			code = run(args[0])
			return nil
		}}
	}
	secrets.AddCommand(
		command("list", "Every variable compose.yml references, and whether it has a value", func() int {
			return runSecretsList(file, projectFlag, stdout, stderr)
		}),
		oneKey("set NAME", "Set NAME from stdin (a hidden prompt on a terminal); never from the command line", func(key string) int {
			return runSecretsSet(file, projectFlag, key, stdin, stdout, stderr)
		}),
		oneKey("unset NAME", "Remove NAME's value", func(key string) int {
			return runSecretsUnset(file, projectFlag, key, stdout, stderr)
		}),
		oneKey("generate NAME", "Set NAME to a random value nobody sees (e.g. POSTGRES_PASSWORD)", func(key string) int {
			return runSecretsGenerate(file, projectFlag, key, stdout, stderr)
		}),
	)
	root.AddCommand(secrets)

	var linkBranch, linkFile string
	var linkContinue int
	var linkWait, webhookRotate bool
	link := &cobra.Command{
		Use:   "link [repo-url]",
		Short: "Link a repo to Houston (Add project): deploy key, access, compose file, save",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			url := ""
			if len(args) > 0 {
				url = args[0]
			}
			code = runLink(url, linkContinue, linkBranch, linkFile, linkWait, stdout, stderr)
			return nil
		},
	}
	link.Flags().StringVar(&linkBranch, "branch", "main", "the branch to read")
	link.Flags().StringVar(&linkFile, "file", "compose.yml", "the compose file's path in the repo")
	link.Flags().IntVar(&linkContinue, "continue", 0, "carry on with a link started earlier (after adding its deploy key)")
	link.Flags().BoolVar(&linkWait, "wait", false, "keep checking access (every 5 s, up to 10 minutes) until the deploy key is added")
	webhook := command("webhook", "A project's webhook URL (and secret, until the first push arrives)", func() int {
		return runWebhook(file, projectFlag, webhookRotate, stdout, stderr)
	})
	webhook.Flags().StringVar(&projectFlag, "project", "", "the project (default: the compose file's name)")
	webhook.Flags().BoolVar(&webhookRotate, "rotate", false, "make a new secret (then paste it into the git host)")
	root.AddCommand(link, webhook)

	// Cloudflare (docs/plans/cloudflare-settings.md): the view, a new token, repair.
	var cloudflareJSON bool
	cloudflare := command("cloudflare", "Cloudflare for this server: the tunnel and its connections, its routes, Houston's DNS records", func() int {
		return runCloudflare(file, cloudflareJSON, stdout, stderr)
	})
	cloudflare.Flags().BoolVar(&cloudflareJSON, "json", false, "print the API's JSON")
	cloudflare.AddCommand(
		&cobra.Command{
			Use:   "token",
			Short: "Replace Houston's Cloudflare API token, from stdin (a hidden prompt on a terminal); checked before it's saved",
			RunE: func(_ *cobra.Command, args []string) error {
				if len(args) > 0 {
					fmt.Fprintln(stderr, "houston cloudflare token: the token comes from stdin (or a hidden prompt), never on the command line")
					code = exitUsage
					return nil
				}
				code = runCloudflareToken(file, stdin, stdout, stderr)
				return nil
			},
		},
		command("repair", "Push the tunnel's routes again and re-point Houston's DNS records (only its own)", func() int {
			return runCloudflareRepair(file, stdout, stderr)
		}),
	)
	root.AddCommand(cloudflare)

	// Port 3000 (docs/plans/security-fixes.md, H3): Settings › Port 3000.
	var portJSON bool
	port := &cobra.Command{
		Use:   "port [open|close]",
		Short: "Whether Mission Control's port 3000 is open to the network; open or close it (Mission Control restarts for a few seconds)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			change := ""
			if len(args) == 1 {
				change = args[0]
			}
			code = runPort(file, change, portJSON, stdout, stderr)
			return nil
		},
	}
	port.Flags().BoolVar(&portJSON, "json", false, "print the API's JSON")
	root.AddCommand(port)
	root.AddCommand(&cobra.Command{
		Use:   "update [vX.Y.Z]",
		Short: "Update the Houston server to the latest release (or vX.Y.Z), from anywhere, and follow it to its result",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			version := ""
			if len(args) > 0 {
				version = args[0]
			}
			code = runUpdate(file, version, stdout, stderr)
			return nil
		},
	})

	var snapshotsJSON, backupFollow bool
	snapshots := command("snapshots", "A project's snapshots on the server (code and data together), newest first", func() int {
		return runSnapshots(file, projectFlag, snapshotsJSON, stdout, stderr)
	})
	snapshots.Flags().StringVar(&projectFlag, "project", "", "the project (default: the compose file's name)")
	snapshots.Flags().BoolVar(&snapshotsJSON, "json", false, "print the API's JSON")
	backup := command("backup", "Back a project up now, on the server", func() int {
		return runBackup(file, projectFlag, backupFollow, stdout, stderr)
	})
	backup.Flags().StringVar(&projectFlag, "project", "", "the project (default: the compose file's name)")
	backup.Flags().BoolVar(&backupFollow, "follow", false, "wait for the result; exit 0 on GO (or nothing to back up), 1 on NO-GO")
	root.AddCommand(snapshots, backup)
	var timeZone string
	settings := command("settings", "Houston's settings on the server; --time-zone sets the zone backup schedules run in", func() int {
		return runSettings(timeZone, stdout, stderr)
	})
	settings.Flags().StringVar(&timeZone, "time-zone", "", "an IANA name, like Europe/Berlin")
	root.AddCommand(settings)

	volumes := command("volumes", "A project's named volumes on the server, and where each lives", func() int {
		return runVolumes(file, projectFlag, stdout, stderr)
	})
	volumes.PersistentFlags().StringVar(&projectFlag, "project", "", "the project (default: the compose file's name)")
	var localDisk bool
	place := &cobra.Command{
		Use:   "place VOLUME [LOCATION]",
		Short: "Choose where a volume lives (a storage location, or --local-disk), until the next deploy makes it",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(_ *cobra.Command, args []string) error {
			code = runVolumePlace(file, projectFlag, args, localDisk, stdout, stderr)
			return nil
		},
	}
	place.Flags().BoolVar(&localDisk, "local-disk", false, "on the server's own disk (the default)")
	volumes.AddCommand(place)
	root.AddCommand(volumes)

	storage := command("storage", "Storage locations on the server (add one in Settings › Storage)", func() int {
		return runStorage(stdout, stderr)
	})
	var useDefault bool
	use := &cobra.Command{
		Use:   "use [LOCATION]",
		Short: "Choose where a project backs up: a location, or --default",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			code = runStorageUse(file, projectFlag, args, useDefault, stdout, stderr)
			return nil
		},
	}
	use.Flags().StringVar(&projectFlag, "project", "", "the project (default: the compose file's name)")
	use.Flags().BoolVar(&useDefault, "default", false, "the default location")
	storage.AddCommand(use, &cobra.Command{
		Use:   "default LOCATION",
		Short: "Make a location the default backup target",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			code = runStorageDefault(args[0], stdout, stderr)
			return nil
		},
	})
	root.AddCommand(storage)

	var maintenanceMessage string
	maintenance := &cobra.Command{
		Use:   "maintenance [on|off]",
		Short: "Houston's maintenance page for a project: see it, or turn it on or off (it stays until turned off)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			code = runMaintenance(file, projectFlag, args, maintenanceMessage, stdout, stderr)
			return nil
		},
	}
	maintenance.Flags().StringVar(&projectFlag, "project", "", "the project (default: the compose file's name)")
	maintenance.Flags().StringVar(&maintenanceMessage, "message", "", "with on: a message on the page")
	root.AddCommand(maintenance)

	var restoreConfirm, restoreLocation string
	var restoreFollow bool
	restore := &cobra.Command{
		Use:   "restore SNAPSHOT --confirm NAME",
		Short: "Roll a project back to a snapshot, code and data together (zero downtime; see houston snapshots)",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			code = runRestore(file, projectFlag, args[0], restoreLocation, restoreConfirm, restoreFollow, stdout, stderr)
			return nil
		},
	}
	restore.Flags().StringVar(&projectFlag, "project", "", "the project (default: the compose file's name)")
	restore.Flags().StringVar(&restoreConfirm, "confirm", "", "the project's name: the data after the snapshot is replaced")
	restore.Flags().StringVar(&restoreLocation, "location", "", "where the snapshot is (default: the project's backup location)")
	restore.Flags().BoolVar(&restoreFollow, "follow", false, "follow it to its result; exit 0 on GO, 1 on NO-GO")
	root.AddCommand(restore)
	var runnerName, workspace string
	runnerCmd := command("runner", "Claim and run queued deploys (in a houston-runner-N container)", func() int {
		return runRunner(runnerName, workspace, stderr, d)
	})
	runnerCmd.Flags().StringVar(&runnerName, "name", "", "this runner's name, houston-runner-N")
	runnerCmd.Flags().StringVar(&workspace, "workspace", "", "where checkouts and deploy keys live (the same path on the host)")
	runnerCmd.MarkFlagRequired("name")
	runnerCmd.MarkFlagRequired("workspace")
	root.AddCommand(runnerCmd)
	var logsOnServer bool
	var logsTail int
	var logsProject string
	logs := command("logs", "Show the app's logs (--server: the app running on the server)", func() int {
		if logsOnServer {
			return runLogsServer(file, logsProject, follow, logsTail, stdout, stderr)
		}
		return runLogs(file, follow, stderr, d)
	})
	logs.Flags().BoolVarP(&follow, "follow", "f", false, "keep following new output")
	logs.Flags().BoolVar(&logsOnServer, "server", false, "the app running on the server")
	logs.Flags().IntVar(&logsTail, "tail", 200, "with --server: how many lines to start with")
	logs.Flags().StringVar(&logsProject, "project", "", "with --server: the project (default: the compose file's name)")
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
