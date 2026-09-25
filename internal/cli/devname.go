package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/scttymn/houston/internal/project"
)

// devInstance names this checkout's houston dev instance
// (docs/plans/dev-localhost.md): "" for the main one (the deploy rule's
// branch, main or master, a detached HEAD, or no git at all), else the
// branch, or --as, as a DNS label.
func devInstance(dir string, p *project.Project, as string) (string, error) {
	name := as
	if name == "" {
		branch := gitBranch(dir)
		if branch == "" || branch == p.Houston.Deploy.Branch || branch == "main" || branch == "master" {
			return "", nil
		}
		name = branch
	}
	slug := dnsLabel(name)
	if slug == "" {
		return "", errors.New("can't make a name for this instance from " + `"` + name + `"` + "; name it with --as")
	}
	return slug, nil
}

// dnsLabel lowercases s, turns everything but letters and digits into single
// dashes, trims them, and cuts it to 63 characters, a DNS label's most.
func dnsLabel(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	label := strings.TrimRight(b.String(), "-")
	if len(label) > 63 {
		label = strings.TrimRight(label[:63], "-")
	}
	return label
}

// gitBranch is the branch checked out at dir or a folder above it, read
// from HEAD (a worktree's .git is a file naming its git dir); "" when
// detached or not in git.
func gitBranch(dir string) string {
	for d := dir; ; d = filepath.Dir(d) {
		gitDir := filepath.Join(d, ".git")
		info, err := os.Stat(gitDir)
		if err == nil {
			if !info.IsDir() {
				data, err := os.ReadFile(gitDir)
				if err != nil {
					return ""
				}
				gitDir = strings.TrimSpace(strings.TrimPrefix(string(data), "gitdir:"))
				if !filepath.IsAbs(gitDir) {
					gitDir = filepath.Join(d, gitDir)
				}
			}
			head, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
			if err != nil {
				return ""
			}
			ref, ok := strings.CutPrefix(strings.TrimSpace(string(head)), "ref: refs/heads/")
			if !ok {
				return ""
			}
			return ref
		}
		if parent := filepath.Dir(d); parent == d {
			return ""
		}
	}
}

// devNames are one instance's names: where it's served, its Compose
// project (also its route's name in the proxy), its alias on houston-dev,
// and the main instance's project, whose data a branch starts from. The
// alias is the host itself: kamal-proxy's health check sends the target's
// name as Host, and a framework's development host check (Rails's) allows
// .localhost, not a made-up name.
type devNames struct {
	Host, Project, Alias, Main string
}

func devNaming(name, instance string, production bool) devNames {
	suffix := ""
	if production {
		suffix = "-production"
	}
	n := devNames{Host: name + suffix + ".localhost", Project: name + suffix, Main: name + suffix}
	if instance != "" {
		n.Host = instance + "." + n.Host
		n.Project = name + "-" + instance + suffix
	}
	n.Alias = n.Host
	return n
}
