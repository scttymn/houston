package mission

import (
	"sort"

	"github.com/scttymn/houston/internal/project"
)

// Inspection is houston inspect --json: what Mission Control is told
// (sync), and what its Add project page shows (preview). Mission Control
// reads a linked repo's compose.yml with it, so compose is only ever
// interpreted by one parser.
type Inspection struct {
	Sync    SyncRequest `json:"sync"`
	Preview Preview     `json:"preview"`
}

// Preview is what the Add project page shows of a compose.yml.
type Preview struct {
	Services []PreviewService `json:"services"`
	Port     int              `json:"port"`
	Health   string           `json:"health"`
	CPUs     string           `json:"cpus,omitempty"`
	Memory   string           `json:"memory,omitempty"`
	Test     bool             `json:"test"`
	Backups  PreviewBackups   `json:"backups"`
}

// PreviewService is one of its services.
type PreviewService struct {
	Name  string `json:"name"`
	Image string `json:"image"`
	App   bool   `json:"app"`
}

// PreviewBackups is what its backups hold.
type PreviewBackups struct {
	Schedule   string   `json:"schedule"`
	KeepAuto   int      `json:"keep_auto"`
	KeepDeploy int      `json:"keep_deploy"`
	Volumes    []string `json:"volumes"`
}

// InspectionFor is p inspected.
func InspectionFor(p *project.Project) Inspection {
	return Inspection{Sync: RequestFor(p), Preview: previewOf(p)}
}

func previewOf(p *project.Project) Preview {
	pv := Preview{Port: p.AppPort, Health: p.Houston.Health, Test: p.Houston.Commands.Test != "",
		Backups: PreviewBackups{Schedule: p.Houston.Backups.Schedule, KeepAuto: p.Houston.Backups.KeepAuto, KeepDeploy: p.Houston.Backups.KeepDeploy, Volumes: []string{}}}
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
		pv.Services = append(pv.Services, PreviewService{Name: name, Image: image, App: name == p.AppService})
	}
	pv.CPUs, pv.Memory = Limits(p)
	for name := range p.Compose.Volumes {
		pv.Backups.Volumes = append(pv.Backups.Volumes, name)
	}
	sort.Strings(pv.Backups.Volumes)
	return pv
}
