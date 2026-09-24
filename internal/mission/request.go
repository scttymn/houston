package mission

import (
	"sort"
	"strconv"
	"strings"

	"github.com/scttymn/houston/internal/project"
)

// RequestFor is what Mission Control is told about a project: exactly what
// houston deploy syncs and houston inspect shows Mission Control when a repo
// is linked. Only the user's variables are listed; <SERVICE>_HOST ones are
// Houston's to fill.
func RequestFor(p *project.Project) SyncRequest {
	services := make([]string, 0, len(p.Compose.Services))
	for name := range p.Compose.Services {
		services = append(services, name)
	}
	sort.Strings(services)
	vars := []Variable{}
	for _, v := range p.Variables {
		if v.Kind == project.Secret {
			vars = append(vars, Variable{Name: v.Name, Required: v.Required})
		}
	}
	return SyncRequest{
		Name: p.Name, AppService: p.AppService, Services: services, Domains: append([]string{}, p.Houston.Domains...), Variables: vars,
		Health: p.Houston.Health, Port: p.AppPort,
		DeployRule: DeployRule{On: p.Houston.Deploy.On, Branch: p.Houston.Deploy.Branch, Tags: p.Houston.Deploy.Tags},
		Volumes:    appVolumes(p), Databases: databases(p, services),
		MaintenancePage: p.Houston.MaintenancePage,
		Backups:         &Backups{Schedule: p.Houston.Backups.Schedule, KeepAuto: p.Houston.Backups.KeepAuto, KeepDeploy: p.Houston.Backups.KeepDeploy},
		Details:         details(p, services),
	}
}

func details(p *project.Project, services []string) *Details {
	d := &Details{Images: map[string]string{}}
	for _, name := range services {
		if image := p.Compose.Services[name].Image; name != p.AppService && image != "" {
			d.Images[name] = image
		}
	}
	d.CPUs, d.Memory = Limits(p)
	if c := p.Houston.Commands.Console; c != nil {
		d.Console = c.Server
	}
	return d
}

// Limits are the app service's deploy.resources.limits, as words: "1.5"
// CPUs, "2 GB". Blank when unset.
func Limits(p *project.Project) (cpus, memory string) {
	app := p.Compose.Services[p.AppService]
	if app.Deploy == nil || app.Deploy.Resources.Limits == nil {
		return "", ""
	}
	if n := app.Deploy.Resources.Limits.NanoCPUs; n > 0 {
		cpus = strconv.FormatFloat(float64(n), 'f', -1, 32)
	}
	if mem := int64(app.Deploy.Resources.Limits.MemoryBytes); mem > 0 {
		memory = humanBytes(mem)
	}
	return cpus, memory
}

func humanBytes(b int64) string {
	switch {
	case b%(1<<30) == 0:
		return strconv.FormatInt(b>>30, 10) + " GB"
	case b%(1<<20) == 0:
		return strconv.FormatInt(b>>20, 10) + " MB"
	}
	return strconv.FormatInt(b, 10) + " bytes"
}

// appVolumes are the app service's named volumes (bind mounts are dev-only).
func appVolumes(p *project.Project) []Volume {
	out := []Volume{}
	for _, v := range p.Compose.Services[p.AppService].Volumes {
		if v.Type == "volume" {
			out = append(out, Volume{Name: v.Source, Path: v.Target})
		}
	}
	return out
}

// databases are the services whose image repository ends in postgres
// (spec §9), in service order.
func databases(p *project.Project, services []string) []Database {
	out := []Database{}
	for _, name := range services {
		image := p.Compose.Services[name].Image
		if name != p.AppService && strings.HasSuffix(repository(image), "postgres") {
			out = append(out, Database{Service: name, Image: image})
		}
	}
	return out
}

// repository is an image reference without its digest or tag. A registry
// port (localhost:5000/postgres) is not a tag: only a colon after the last
// slash starts one.
func repository(image string) string {
	image, _, _ = strings.Cut(image, "@")
	if i := strings.LastIndex(image, ":"); i > strings.LastIndex(image, "/") {
		image = image[:i]
	}
	return image
}
