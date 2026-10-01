package models

import (
	"context"
	"regexp"
	"strings"
)

var repoHosts = map[string]string{"github.com": "GitHub", "gitlab.com": "GitLab", "codeberg.org": "Codeberg", "bitbucket.org": "Bitbucket"}

var repoWordsParts = regexp.MustCompile(`^(?:[a-z+]+://)?(?:[^@/]+@)?([^:/]+)(?::\d+)?[:/](.+?)(?:\.git)?/?$`)

// RepoWords are the repo in words: "GitHub · scttymn/shop"; "" unlinked.
func (p Project) RepoWords() string {
	if p.RepoUrl == "" {
		return ""
	}
	m := repoWordsParts.FindStringSubmatch(p.RepoUrl)
	if m == nil {
		return p.RepoUrl
	}
	host := m[1]
	if name, ok := repoHosts[host]; ok {
		host = name
	}
	return host + " · " + m[2]
}

// ResourcesWords are the app's limits from compose.yml: "2 CPUs · 2 GB";
// "" without.
func (p Project) ResourcesWords() string {
	d := p.Details.V
	var parts []string
	if d.CPUs != "" {
		unit := "CPUs"
		if d.CPUs == "1" {
			unit = "CPU"
		}
		parts = append(parts, d.CPUs+" "+unit)
	}
	if d.Memory != "" {
		parts = append(parts, d.Memory)
	}
	return strings.Join(parts, " · ")
}

// NameRefusal is why name can't be a new project's, or "".
func NameRefusal(ctx context.Context, q *Queries, name string) (string, error) {
	if !ValidName(name) {
		return name + " can't be a project's name", nil
	}
	taken, err := q.ProjectExists(ctx, name)
	if err != nil || !taken {
		return "", err
	}
	return name + " is another project", nil
}

// HasVariable is whether compose.yml references key.
func (p Project) HasVariable(key string) bool {
	for _, v := range p.Variables.V {
		if v.Name == key {
			return true
		}
	}
	return false
}
