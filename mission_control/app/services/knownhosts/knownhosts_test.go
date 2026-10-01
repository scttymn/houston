package knownhosts_test

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/scttymn/houston/mission_control/app/services/knownhosts"
)

// hashed is host as ssh writes it with HashKnownHosts: |1|salt|hmac.
func hashed(host string) string {
	salt := []byte("0123456789abcdefghij")
	mac := hmac.New(sha1.New, salt)
	mac.Write([]byte(host))
	return "|1|" + base64.StdEncoding.EncodeToString(salt) + "|" + base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// The lines recorded for a repo's SSH host, as they are in the file:
// plain or hashed, [host]:port for another port. None for HTTPS, an unseen
// host, or no file yet (as ssh-keygen -F answers).
func TestFor(t *testing.T) {
	file := filepath.Join(t.TempDir(), "known_hosts")
	lines := []string{
		"# a comment",
		"github.com,140.82.112.3 ssh-ed25519 AAAAgithub",
		"github.com ecdsa-sha2-nistp256 AAAAgithub2",
		hashed("[git.example.com]:2222") + " ssh-ed25519 AAAAforgejo",
		hashed("gitlab.com") + " ssh-ed25519 AAAAgitlab",
		"*.corp.example ssh-rsa AAAAcorp",
		"[git.example.com]:2222 ssh-rsa AAAAplain",
		"",
	}
	os.WriteFile(file, []byte(joinLines(lines)), 0o600)
	for repo, want := range map[string][]string{
		"git@github.com:scttymn/shop.git":                 {lines[1], lines[2]},
		"ssh://git@git.example.com:2222/scttymn/shop.git": {lines[3], lines[6]},
		"ssh://gitlab.com/group/shop.git":                 {lines[4]},
		"git@git.corp.example:team/app.git":               {lines[5]},
		"https://github.com/scttymn/shop.git":             nil,
		"git@bitbucket.org:x/y.git":                       nil,
		"ssh://git@git.example.com/scttymn/shop.git":      nil, // port 22: not [host]:2222
	} {
		if got := knownhosts.For(file, repo); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: %q, want %q", repo, got, want)
		}
	}
	if got := knownhosts.For(filepath.Join(t.TempDir(), "none"), "git@github.com:x/y.git"); got != nil {
		t.Errorf("no file: %q", got)
	}
}

func joinLines(lines []string) string {
	out := ""
	for _, l := range lines {
		out += l + "\n"
	}
	return out
}
