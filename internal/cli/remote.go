package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/sevenmoons/houston/internal/server"
)

// followEvery is how often deploys show --follow asks for more. Tests shorten it.
var followEvery = 2 * time.Second

// remote is a --server command's client and project: --project, else the
// compose file's name. needProject: whether "no project" is an error.
func remote(file, projectFlag string, needProject bool, stderr io.Writer) (*server.Client, string, int) {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(stderr, "houston: %v\n", err)
		return nil, "", exitFailure
	}
	cfg, err := server.Load(os.Getenv, home)
	if err != nil {
		fmt.Fprintf(stderr, "houston: %v\n", err)
		return nil, "", exitFailure
	}
	name := projectFlag
	if name == "" {
		if _, statErr := os.Stat(file); statErr == nil {
			p, ok := loadProject(file, stderr)
			if !ok {
				return nil, "", exitUsage
			}
			name = p.Name
		} else if !errors.Is(statErr, fs.ErrNotExist) {
			fmt.Fprintf(stderr, "houston: %v\n", statErr)
			return nil, "", exitUsage
		}
	}
	if name == "" && needProject {
		fmt.Fprintf(stderr, "houston: say which project: --project <name> (or run it where %s is)\n", file)
		return nil, "", exitUsage
	}
	return server.New(cfg), name, 0
}

var statusWords = map[string]string{"queued": "QUEUED", "in_flight": "IN FLIGHT", "go": "GO", "no_go": "NO-GO", "standby": "STANDBY"}

func runStatus(file, projectFlag string, asJSON bool, stdout, stderr io.Writer) int {
	client, name, code := remote(file, projectFlag, false, stderr)
	if code != 0 {
		return code
	}
	ctx := context.Background()
	if asJSON {
		path := "/api/v1/projects"
		if name != "" {
			path += "/" + name
		}
		return printRaw(ctx, client, path, stdout, stderr)
	}
	if name == "" {
		projects, err := client.Projects(ctx)
		if err != nil {
			return remoteFailed(err, stderr)
		}
		for _, p := range projects {
			last := "never deployed"
			if d := p.LastDeploy; d != nil {
				last = fmt.Sprintf("#%d %s", d.Number, statusWords[d.Status])
			}
			fmt.Fprintf(stdout, "%-10s %-20s %-8s %-30s %s\n", statusWords[p.Status], p.Name, short(p.RunningSHA), p.Host, last)
		}
		return 0
	}
	p, err := client.Project(ctx, name)
	if err != nil {
		return remoteFailed(err, stderr)
	}
	fmt.Fprintf(stdout, "%s  %s\n", p.Name, statusWords[p.Status])
	fmt.Fprintf(stdout, "running   %s\n", orDash(short(p.RunningSHA)))
	fmt.Fprintf(stdout, "host      %s\n", p.Host)
	for d, st := range p.Domains {
		line := d + ": " + st.State
		if st.Reason != "" {
			line += " (" + st.Reason + ")"
		}
		fmt.Fprintf(stdout, "domain    %s\n", line)
	}
	rule := "Every commit to " + p.DeployRule.Branch
	if p.DeployRule.On == "tag" {
		rule = "Tags matching " + p.DeployRule.Tags
	}
	fmt.Fprintf(stdout, "deploys   %s\n", rule)
	if p.RepoURL != "" {
		fmt.Fprintf(stdout, "repo      %s (%s)\n", p.RepoURL, p.Branch)
	}
	if d := p.LastDeploy; d != nil {
		line := fmt.Sprintf("#%d %s %s", d.Number, statusWords[d.Status], short(d.SHA))
		if d.Error != "" {
			line += ": " + d.Error
		}
		fmt.Fprintf(stdout, "last      %s\n", line)
	}
	var missing []string
	for _, s := range p.Secrets {
		if s.Required && !s.Set {
			missing = append(missing, s.Name)
		}
	}
	if len(missing) > 0 {
		fmt.Fprintf(stdout, "HOLD      %s %s no value\n", strings.Join(missing, ", "), map[bool]string{true: "has", false: "have"}[len(missing) == 1])
	}
	return 0
}

func runDeploys(file, projectFlag string, asJSON bool, stdout, stderr io.Writer) int {
	client, name, code := remote(file, projectFlag, true, stderr)
	if code != 0 {
		return code
	}
	ctx := context.Background()
	if asJSON {
		return printRaw(ctx, client, "/api/v1/projects/"+name+"/deploys", stdout, stderr)
	}
	deploys, err := client.Deploys(ctx, name, 1)
	if err != nil {
		return remoteFailed(err, stderr)
	}
	for _, d := range deploys {
		line := fmt.Sprintf("#%-5d %-10s %s  %s", d.Number, statusWords[d.Status], short(d.SHA), strings.TrimPrefix(strings.TrimPrefix(d.Ref, "refs/heads/"), "refs/tags/"))
		if d.Error != "" {
			line += "  " + d.Error
		}
		fmt.Fprintln(stdout, line)
	}
	return 0
}

// runDeployShow prints a deploy's steps and log; with follow, keeps printing
// new log until it's finished. The exit code is the result: 0 GO, 1 NO-GO.
func runDeployShow(file, projectFlag, number string, follow, asJSON bool, stdout, stderr io.Writer) int {
	client, name, code := remote(file, projectFlag, true, stderr)
	if code != 0 {
		return code
	}
	ctx := context.Background()
	if number == "" {
		number = "latest"
	}
	if asJSON && !follow {
		return printRaw(ctx, client, "/api/v1/projects/"+name+"/deploys/"+number, stdout, stderr)
	}
	from, waiting := 0, false
	for {
		d, err := client.Deploy(ctx, name, number, from)
		if err != nil {
			return remoteFailed(err, stderr)
		}
		number = strconv.Itoa(d.Number)
		io.WriteString(stdout, d.Log)
		from = d.LogNext
		switch d.Status {
		case "go":
			fmt.Fprintf(stdout, "GO: %s #%d serving %s\n", name, d.Number, short(d.SHA))
			return 0
		case "no_go":
			fmt.Fprintf(stdout, "NO-GO: %s #%d: %s\n", name, d.Number, d.Error)
			return exitFailure
		case "queued":
			if !waiting && follow {
				fmt.Fprintf(stderr, "#%d is queued, waiting for a runner…\n", d.Number)
				waiting = true
			}
		}
		if !follow {
			fmt.Fprintf(stdout, "%s: %s #%d at %s\n", statusWords[d.Status], name, d.Number, orDash(d.Step))
			return 0
		}
		time.Sleep(followEvery)
	}
}

func printRaw(ctx context.Context, client *server.Client, path string, stdout, stderr io.Writer) int {
	body, err := client.Raw(ctx, path)
	if err != nil {
		return remoteFailed(err, stderr)
	}
	stdout.Write(body)
	fmt.Fprintln(stdout)
	return 0
}

func remoteFailed(err error, stderr io.Writer) int {
	fmt.Fprintf(stderr, "houston: %v\n", err)
	return exitFailure
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
