package models_test

import (
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/scttymn/houston/mission-control-go/app/models"
)

// A repo URL is ssh://, https:// or user@host:path, with no credentials,
// options or local paths; a branch is one git accepts; a compose path is a
// .yml inside the repo.
func TestRepoLinkRules(t *testing.T) {
	for url, ok := range map[string]bool{
		"git@github.com:scttymn/shop.git": true, "ssh://git@git.example.com:2222/team/shop.git": true, "https://github.com/scttymn/shop.git": true,
		"git@github.com:-oProxyCommand=x": false, "git@github.com:/etc/passwd": false, "https://user:pass@github.com/x.git": false,
		"/srv/repos/shop.git": false, "file:///srv/shop.git": false, "ext::sh -c x": false, "git@github.com:a b": false,
	} {
		if got := models.RepoURLProblem(url) == ""; got != ok {
			t.Errorf("%q: %v", url, got)
		}
	}
	for branch, ok := range map[string]bool{
		"main": true, "feature/x": true, "v1.2": true,
		"-x": false, "/x": false, ".x": false, "a..b": false, "a//b": false, "a@{b": false, "x.": false, "x/": false, "x.lock": false,
		"a b": false, "a~b": false, "a^b": false, "a:b": false, "a?b": false, "a*b": false, "a[b": false, `a\b`: false, "": false,
	} {
		if got := models.ValidBranch(branch); got != ok {
			t.Errorf("branch %q: %v", branch, got)
		}
	}
	for path, ok := range map[string]bool{
		"compose.yml": true, "deploy/compose.yaml": true, "a..b.yml": true,
		"../compose.yml": false, "a/../compose.yml": false, "/compose.yml": false, "-x.yml": false, "compose.json": false, "a b.yml": false,
	} {
		if got := models.ValidComposePath(path); got != ok {
			t.Errorf("path %q: %v", path, got)
		}
	}
}

// A deploy key: an ed25519 pair ssh reads, the public half commented.
func TestNewDeployKey(t *testing.T) {
	private, public, err := models.NewDeployKey("houston@svnmns.com")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ssh.ParsePrivateKey([]byte(private)); err != nil {
		t.Errorf("private: %v", err)
	}
	key, comment, _, _, err := ssh.ParseAuthorizedKey([]byte(public))
	if err != nil || key.Type() != "ssh-ed25519" || comment != "houston@svnmns.com" || !strings.HasPrefix(private, "-----BEGIN OPENSSH PRIVATE KEY-----") {
		t.Errorf("public %q: %v", public, err)
	}
}
