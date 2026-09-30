package models

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"regexp"
	"strings"

	"golang.org/x/crypto/ssh"
)

// A repo link's rules (the Rails app's RepoLink), as code where Ruby's
// patterns look around, which Go's don't.
var (
	repoHost  = `[A-Za-z0-9][A-Za-z0-9.-]*`
	repoPath  = `[A-Za-z0-9._~/-]+`
	httpsRepo = regexp.MustCompile(`^https://` + repoHost + `(:\d+)?/` + repoPath + `$`)
	sshRepo   = regexp.MustCompile(`^ssh://([A-Za-z0-9._-]+@)?` + repoHost + `(:\d+)?/` + repoPath + `$`)
	scpRepo   = regexp.MustCompile(`^[A-Za-z0-9._-]+@` + repoHost + `:(` + repoPath + `)$`)
	composeOK = regexp.MustCompile(`^[A-Za-z0-9._/-]+\.ya?ml$`)
)

// RepoURLProblem is "must be ..." for a URL that isn't an ssh://,
// https:// or user@host:path repo URL (no credentials, options or local
// paths), else "".
func RepoURLProblem(url string) string {
	ok := httpsRepo.MatchString(url) || sshRepo.MatchString(url)
	if m := scpRepo.FindStringSubmatch(url); m != nil && !strings.HasPrefix(m[1], "-") && !strings.HasPrefix(m[1], "/") {
		ok = true
	}
	if ok {
		return ""
	}
	return "must be an ssh://, https:// or user@host:path repo URL (no credentials, options or local paths)"
}

// ValidBranch is whether git accepts name as a branch (git
// check-ref-format --branch, closely enough to refuse anything unsafe).
func ValidBranch(name string) bool {
	if name == "" || strings.HasPrefix(name, "-") || strings.HasPrefix(name, "/") || strings.HasPrefix(name, ".") ||
		strings.Contains(name, "..") || strings.Contains(name, "//") || strings.Contains(name, "@{") ||
		strings.HasSuffix(name, ".") || strings.HasSuffix(name, "/") || strings.HasSuffix(name, ".lock") {
		return false
	}
	for _, r := range name {
		if r <= 0x20 || r == 0x7f || strings.ContainsRune("~^:?*[\\", r) {
			return false
		}
	}
	return true
}

// ValidComposePath is whether p is a .yml or .yaml path inside the repo.
func ValidComposePath(p string) bool {
	if !composeOK.MatchString(p) || strings.HasPrefix(p, "-") || strings.HasPrefix(p, "/") {
		return false
	}
	for _, part := range strings.Split(p, "/") {
		if part == ".." {
			return false
		}
	}
	return true
}

// NewDeployKey is an ed25519 key pair for a repo's read-only deploy key:
// the private key in OpenSSH's format, the public one as a line for the
// git host, commented houston@<base>.
func NewDeployKey(comment string) (private, public string, err error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	block, err := ssh.MarshalPrivateKey(priv, comment)
	if err != nil {
		return "", "", err
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return "", "", err
	}
	return string(pem.EncodeToMemory(block)), strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub))) + " " + comment, nil
}
