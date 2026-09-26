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
	if slug == "production" {
		// <name>-production is the main instance's --production project.
		return "", errors.New(`"production" is houston dev --production's own name; name this instance with --as`)
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

// gitDirs finds the git checkout at dir or a folder above it: its root, its
// own git directory (a linked worktree's is <common>/worktrees/<id>, named in
// its .git file) and the repo's shared one. All "" outside git.
func gitDirs(dir string) (root, own, common string) {
	for d := dir; ; d = filepath.Dir(d) {
		gitDir := filepath.Join(d, ".git")
		if info, err := os.Stat(gitDir); err == nil {
			if !info.IsDir() {
				data, err := os.ReadFile(gitDir)
				if err != nil {
					return "", "", ""
				}
				gitDir = strings.TrimSpace(strings.TrimPrefix(string(data), "gitdir:"))
				if !filepath.IsAbs(gitDir) {
					gitDir = filepath.Join(d, gitDir)
				}
			}
			common = gitDir
			if data, err := os.ReadFile(filepath.Join(gitDir, "commondir")); err == nil {
				common = strings.TrimSpace(string(data))
				if !filepath.IsAbs(common) {
					common = filepath.Clean(filepath.Join(gitDir, common))
				}
			}
			return d, gitDir, common
		}
		if parent := filepath.Dir(d); parent == d {
			return "", "", ""
		}
	}
}

// gitBranch is the branch checked out at dir or a folder above it, read
// from HEAD (a worktree's .git is a file naming its git dir); "" when
// detached or not in git.
func gitBranch(dir string) string {
	_, own, _ := gitDirs(dir)
	if own == "" {
		return ""
	}
	return headBranch(own)
}

func headBranch(gitDir string) string {
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

// A checkout's remembered instance name (houston dev --as), kept in its own
// git directory: never in the working tree, and gone with the worktree.
const devNameFile = "houston-dev-name"

func savedDevName(own string) string {
	if own == "" {
		return ""
	}
	data, _ := os.ReadFile(filepath.Join(own, devNameFile))
	return strings.TrimSpace(string(data))
}

func saveDevName(own, name string) error {
	path := filepath.Join(own, devNameFile)
	if name == "" {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	return os.WriteFile(path, []byte(name+"\n"), 0o644)
}

// checkout is one of a repo's checkouts: the main one or a linked worktree.
type checkout struct{ dir, own string }

// checkouts lists the repo's checkouts from its shared git directory, as git
// keeps them: the main one beside it, and each worktrees/<id>/gitdir.
func checkouts(common string) []checkout {
	var all []checkout
	if filepath.Base(common) == ".git" {
		all = append(all, checkout{dir: filepath.Dir(common), own: common})
	}
	entries, _ := os.ReadDir(filepath.Join(common, "worktrees"))
	for _, e := range entries {
		own := filepath.Join(common, "worktrees", e.Name())
		data, err := os.ReadFile(filepath.Join(own, "gitdir"))
		if err != nil {
			continue
		}
		dir := filepath.Dir(strings.TrimSpace(string(data)))
		if _, err := os.Stat(dir); err != nil {
			continue // removed by hand; git prunes its entry later
		}
		all = append(all, checkout{dir: dir, own: own})
	}
	return all
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
