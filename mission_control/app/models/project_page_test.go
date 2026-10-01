package models_test

import (
	"testing"

	"github.com/scttymn/houston/mission_control/app/models"
)

func TestProjectWords(t *testing.T) {
	for url, want := range map[string]string{
		"git@github.com:scttymn/shop.git":      "GitHub · scttymn/shop",
		"https://gitlab.com/group/sub/app.git": "GitLab · group/sub/app",
		"ssh://git@codeberg.org:2222/me/wiki":  "Codeberg · me/wiki",
		"https://git.example.com/me/app/":      "git.example.com · me/app",
		"":                                     "",
		"not a url":                            "not a url",
	} {
		if got := (models.Project{RepoUrl: url}).RepoWords(); got != want {
			t.Errorf("RepoWords(%q) = %q", url, got)
		}
	}
	for _, c := range []struct{ cpus, memory, want string }{{"2", "2 GB", "2 CPUs · 2 GB"}, {"1", "", "1 CPU"}, {"", "512 MB", "512 MB"}, {"", "", ""}, {"0.5", "", "0.5 CPUs"}} {
		p := models.Project{Details: models.ProjectDetails{V: models.Details{CPUs: c.cpus, Memory: c.memory}}}
		if got := p.ResourcesWords(); got != c.want {
			t.Errorf("ResourcesWords(%q, %q) = %q", c.cpus, c.memory, got)
		}
	}
}
