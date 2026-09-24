package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/scttymn/houston/internal/mission"
	"github.com/scttymn/houston/internal/project"
)

// inspection is houston inspect --json: what Mission Control is told
// (sync), and what its Add project page shows (preview). Mission Control
// runs this binary to read a linked repo's compose.yml, so compose is only
// ever interpreted by one parser.
type inspection struct {
	Sync    mission.SyncRequest `json:"sync"`
	Preview preview             `json:"preview"`
}

type preview struct {
	Services []previewService `json:"services"`
	Port     int              `json:"port"`
	Health   string           `json:"health"`
	CPUs     string           `json:"cpus,omitempty"`
	Memory   string           `json:"memory,omitempty"`
	Test     bool             `json:"test"`
	Backups  previewBackups   `json:"backups"`
}

type previewService struct {
	Name  string `json:"name"`
	Image string `json:"image"`
	App   bool   `json:"app"`
}

type previewBackups struct {
	Schedule   string   `json:"schedule"`
	KeepAuto   int      `json:"keep_auto"`
	KeepDeploy int      `json:"keep_deploy"`
	Volumes    []string `json:"volumes"`
}

func runInspect(file string, asJSON bool, stdout, stderr io.Writer) int {
	p, ok := loadProject(file, stderr)
	if !ok {
		return exitUsage
	}
	in := inspection{Sync: mission.RequestFor(p), Preview: previewOf(p)}
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

func previewOf(p *project.Project) preview {
	pv := preview{Port: p.AppPort, Health: p.Houston.Health, Test: p.Houston.Commands.Test != "",
		Backups: previewBackups{Schedule: p.Houston.Backups.Schedule, KeepAuto: p.Houston.Backups.KeepAuto, KeepDeploy: p.Houston.Backups.KeepDeploy, Volumes: []string{}}}
	names := make([]string, 0, len(p.Compose.Services))
	for name := range p.Compose.Services {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		svc := p.Compose.Services[name]
		image := svc.Image
		if name == p.AppService {
			image = ""
		}
		pv.Services = append(pv.Services, previewService{Name: name, Image: image, App: name == p.AppService})
	}
	pv.CPUs, pv.Memory = mission.Limits(p)
	for name := range p.Compose.Volumes {
		pv.Backups.Volumes = append(pv.Backups.Volumes, name)
	}
	sort.Strings(pv.Backups.Volumes)
	return pv
}

func printInspection(w io.Writer, p *project.Project, in inspection) {
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
