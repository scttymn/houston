package models

import (
	"encoding/json"
	"strings"

	"github.com/scttymn/gantry/db"
)

// The projects table's JSON columns.
type (
	Names          = db.JSON[[]string]
	Variables      = db.JSON[[]Variable]
	Volumes        = db.JSON[[]Volume]
	Databases      = db.JSON[[]Database]
	DomainStates   = db.JSON[map[string]DomainState]
	ProjectDetails = db.JSON[Details]
	Refs           = db.JSON[map[string]string]
	Object         = db.JSON[json.RawMessage]
	Settings       = db.JSON[map[string]string]
)

// DomainState is where a custom domain points, as the last sync or GO
// found it, and why.
type DomainState struct {
	State  string `json:"state"`
	Reason string `json:"reason"`
}

// Host is <name>.<base>, the app's default host.
func (p Project) Host(base string) string { return p.Name + "." + base }

// Accessories are its services but the app.
func (p Project) Accessories() []string {
	var out []string
	for _, s := range p.Services.V {
		if s != p.AppService {
			out = append(out, s)
		}
	}
	return out
}

// HostNames are the container-name prefixes it owns in a generation: its
// own name (Kamal's app containers) and <name>-<service> for each
// accessory.
func (p Project) HostNames(generation int64) []string {
	names := []string{p.Name}
	for _, s := range p.Accessories() {
		names = append(names, Generation{Project: p.Name, Number: generation}.Container(s))
	}
	return names
}

// Generation is a project's Docker names in one data generation, matching
// the CLI's kamal.Names: generation 1 is the names every project had before
// generations; a restore builds g+1 beside the live one.
type Generation struct {
	Project string
	Number  int64
}

// Volume is its volume name: <name>_<volume>, or <name>.g<g>_<volume>.
func (g Generation) Volume(name string) string {
	if g.Number <= 1 {
		return g.Project + "_" + name
	}
	return g.Project + ".g" + itoa(g.Number) + "_" + name
}

// Container is an accessory's container: <name>-<service>[-g<g>].
func (g Generation) Container(service string) string {
	if g.Number <= 1 {
		return g.Project + "-" + service
	}
	return g.Project + "-" + service + "-g" + itoa(g.Number)
}

// Directory is where a volume's directory lives in its storage location.
func (g Generation) Directory(name string) string {
	if g.Number <= 1 {
		return "volumes/" + g.Project + "/" + name
	}
	return "volumes/" + g.Project + ".g" + itoa(g.Number) + "/" + name
}

// MissingSecrets are the required variables of vars that values (key →
// value) has no value for, in order.
func MissingSecrets(vars []Variable, values map[string]string) []string {
	var missing []string
	for _, v := range vars {
		if v.Required && strings.TrimSpace(values[v.Name]) == "" {
			missing = append(missing, v.Name)
		}
	}
	return missing
}
