package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/scttymn/houston/internal/project"
)

// The instance comes from the git branch: the main one is <name>.localhost,
// any other <branch>.<name>.localhost, each its own Compose project
// (docs/plans/dev-localhost.md, row 9).
func TestDevInstanceName(t *testing.T) {
	repo := func(head string) string {
		dir := t.TempDir()
		must(t, os.MkdirAll(filepath.Join(dir, ".git"), 0o755))
		must(t, os.WriteFile(filepath.Join(dir, ".git", "HEAD"), []byte(head), 0o644))
		return dir
	}
	p := &project.Project{Name: "equip", Houston: project.Houston{Deploy: project.Deploy{On: "commit", Branch: "main"}}}
	for _, tc := range []struct {
		name, head, as, want string
	}{
		{"main", "ref: refs/heads/main\n", "", ""},
		{"master counts as main", "ref: refs/heads/master\n", "", ""},
		{"a branch", "ref: refs/heads/feature1\n", "", "feature1"},
		{"slashes, case and underscores", "ref: refs/heads/Feature/Login_Fix\n", "", "feature-login-fix"},
		{"a detached HEAD is main", "4be21c0c0ffee4be21c0c0ffee4be21c0c0ffee0\n", "", ""},
		{"--as wins", "ref: refs/heads/feature1\n", "Demo Day", "demo-day"},
		{"too long for a DNS label", "ref: refs/heads/" + strings.Repeat("a", 80) + "\n", "", strings.Repeat("a", 63)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := devInstance(repo(tc.head), p, tc.as)
			if err != nil || got != tc.want {
				t.Errorf("devInstance = %q, %v; want %q", got, err, tc.want)
			}
		})
	}

	t.Run("the deploy rule's branch is main", func(t *testing.T) {
		q := *p
		q.Houston.Deploy.Branch = "develop"
		if got, _ := devInstance(repo("ref: refs/heads/develop\n"), &q, ""); got != "" {
			t.Errorf("develop = %q, want main", got)
		}
	})
	t.Run("not a git checkout is main", func(t *testing.T) {
		if got, err := devInstance(t.TempDir(), p, ""); got != "" || err != nil {
			t.Errorf("= %q, %v", got, err)
		}
	})
	t.Run("compose.yml in a subfolder, and a worktree", func(t *testing.T) {
		main := repo("ref: refs/heads/main\n")
		wt := filepath.Join(main, ".git", "worktrees", "feat")
		must(t, os.MkdirAll(wt, 0o755))
		must(t, os.WriteFile(filepath.Join(wt, "HEAD"), []byte("ref: refs/heads/feat-x\n"), 0o644))
		checkout := t.TempDir()
		must(t, os.WriteFile(filepath.Join(checkout, ".git"), []byte("gitdir: "+wt+"\n"), 0o644))
		sub := filepath.Join(checkout, "deploy")
		must(t, os.MkdirAll(sub, 0o755))
		if got, _ := devInstance(sub, p, ""); got != "feat-x" {
			t.Errorf("= %q, want feat-x", got)
		}
	})
	t.Run("a name that slugs to nothing", func(t *testing.T) {
		if _, err := devInstance(repo("ref: refs/heads/main\n"), p, "///"); err == nil || !strings.Contains(err.Error(), "--as") {
			t.Errorf("err = %v", err)
		}
	})
}

func TestDevNames(t *testing.T) {
	for _, tc := range []struct {
		instance   string
		production bool
		host, proj string
		main       string
	}{
		{"", false, "equip.localhost", "equip", "equip"},
		{"feature1", false, "feature1.equip.localhost", "equip-feature1", "equip"},
		{"", true, "equip-production.localhost", "equip-production", "equip-production"},
		{"feature1", true, "feature1.equip-production.localhost", "equip-feature1-production", "equip-production"},
	} {
		n := devNaming("equip", tc.instance, tc.production)
		if n.Host != tc.host || n.Project != tc.proj || n.Main != tc.main || n.Alias != tc.host {
			t.Errorf("%q production=%v: %+v", tc.instance, tc.production, n)
		}
	}
}
