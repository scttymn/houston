package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/scttymn/houston/internal/mission"
	"github.com/scttymn/houston/internal/project"
)

func runInspect(file string, asJSON bool, stdout, stderr io.Writer) int {
	p, ok := loadProject(file, stderr)
	if !ok {
		return exitUsage
	}
	in := mission.InspectionFor(p)
	if asJSON {
		out, err := json.MarshalIndent(in, "", "  ")
		if err != nil {
			fmt.Fprintf(stderr, "houston: %v\n", err)
			return exitFailure
		}
		fmt.Fprintln(stdout, string(out))
		return 0
	}
	printInspection(stdout, p, in)
	return 0
}

func printInspection(w io.Writer, p *project.Project, in mission.Inspection) {
	pv := in.Preview
	app := fmt.Sprintf("%s · port %d · health %s", p.AppService, pv.Port, pv.Health)
	if pv.CPUs != "" {
		app += " · " + pv.CPUs + " CPUs"
	}
	if pv.Memory != "" {
		app += " · " + pv.Memory
	}
	var services, secrets []string
	for _, s := range pv.Services {
		if !s.App {
			services = append(services, fmt.Sprintf("%s (%s)", s.Name, s.Image))
		}
	}
	for _, v := range in.Sync.Variables {
		secrets = append(secrets, fmt.Sprintf("%s (%s)", v.Name, map[bool]string{true: "required", false: "optional"}[v.Required]))
	}
	deploys := "every commit to " + orDash(p.Houston.Deploy.Branch)
	if p.Houston.Deploy.On == "tag" {
		deploys = "tags matching " + orDash(p.Houston.Deploy.Tags)
	}
	if pv.Test {
		deploys += " · tests run first"
	}
	backups := fmt.Sprintf("%s · keep %d auto, %d deploy", pv.Backups.Schedule, pv.Backups.KeepAuto, pv.Backups.KeepDeploy)
	if len(pv.Backups.Volumes) > 0 {
		backups += " · volumes " + strings.Join(pv.Backups.Volumes, ", ")
	}
	for _, row := range [][2]string{
		{"name", p.Name}, {"app", app}, {"services", orDash(strings.Join(services, ", "))},
		{"domains", orDash(strings.Join(in.Sync.Domains, ", "))}, {"deploys", deploys},
		{"secrets", orDash(strings.Join(secrets, ", "))}, {"backups", backups},
	} {
		fmt.Fprintf(w, "%-10s %s\n", row[0], row[1])
	}
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
