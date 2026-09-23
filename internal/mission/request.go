package mission

import (
	"sort"

	"github.com/sevenmoons/houston/internal/project"
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
	}
}
