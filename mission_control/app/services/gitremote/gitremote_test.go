package gitremote

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/scttymn/houston/mission_control/app/services/dockercmd"
)

// fake answers git's commands by their verb, and keeps what each saw.
type fake struct {
	answers map[string]dockercmd.Result
	seen    [][]string
	env     map[string]string
	keys    []string
}

func (f *fake) run(ctx context.Context, argv []string, env map[string]string) dockercmd.Result {
	f.seen = append(f.seen, argv)
	f.env = env
	for _, word := range strings.Fields(env["GIT_SSH_COMMAND"]) {
		if strings.HasSuffix(word, "deploy_key") {
			b, _ := os.ReadFile(word)
			f.keys = append(f.keys, string(b))
		}
	}
	for verb, r := range f.answers {
		if strings.Contains(strings.Join(argv, " "), verb) {
			return r
		}
	}
	return dockercmd.Result{OK: true}
}

var link = Link{RepoURL: "git@github.com:scttymn/shop.git", DeployKey: "-----KEY-----", Branch: "main"}

func git(f *fake, t *testing.T) Git {
	return Git{Run: f.run, KnownHosts: "/data/known_hosts", TempDir: t.TempDir()}
}

// Refs: every branch and tag, the key only in a file of its own, gone
// after; ssh never asks, and accepts only a host it knows or a new one.
func TestRefs(t *testing.T) {
	f := &fake{answers: map[string]dockercmd.Result{"ls-remote": {OK: true, Output: strings.Repeat("a", 40) + "\trefs/heads/main\n" +
		strings.Repeat("b", 40) + "\trefs/tags/v1\n" + strings.Repeat("c", 40) + "\trefs/tags/v1^{}\nnot a line\n"}}}
	g := git(f, t)
	refs, problem := g.Refs(context.Background(), link)
	if problem != "" || len(refs) != 3 || refs["refs/tags/v1^{}"] != strings.Repeat("c", 40) {
		t.Errorf("refs %v, %q", refs, problem)
	}
	if strings.Join(f.seen[0], " ") != "git ls-remote --heads --tags -- git@github.com:scttymn/shop.git" {
		t.Errorf("ran %v", f.seen)
	}
	if len(f.keys) != 1 || f.keys[0] != "-----KEY-----\n" {
		t.Errorf("the key file had %q", f.keys)
	}
	ssh := f.env["GIT_SSH_COMMAND"]
	if !strings.Contains(ssh, "-o StrictHostKeyChecking=accept-new -o UserKnownHostsFile=/data/known_hosts") || !strings.Contains(ssh, "BatchMode=yes") ||
		f.env["GIT_TERMINAL_PROMPT"] != "0" || f.env["GIT_ALLOW_PROTOCOL"] != "ssh:https" {
		t.Errorf("env %v", f.env)
	}
	if left, _ := os.ReadDir(g.TempDir); len(left) != 0 {
		t.Errorf("left behind: %v", left)
	}
}

// git's no, in one line, with what to do about the usual ones.
func TestExplain(t *testing.T) {
	g := Git{KnownHosts: "/data/known_hosts"}
	for out, want := range map[string]string{
		"Warning: Permanently added 'github.com'\ngit@github.com: Permission denied (publickey).\nfatal: Could not read from remote repository.\n": "git@github.com: Permission denied (publickey).. Add the deploy key above to the repo as a read-only deploy key, then check again.",
		"@@@@@\n@ WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED! @\n":                                                                            "the git host's host key changed since Houston first saw it. If that's expected, remove its line from /data/known_hosts on the server, then check again.",
		"ssh: connect to host git.example.com port 22: Connection timed out\n":                                                                     "ssh: connect to host git.example.com port 22: Connection timed out",
		"Load key \"/tmp/houston-git123/deploy_key\": invalid format\nfatal: bad\n":                                                                "fatal: bad",
		"": "git failed without saying why",
	} {
		if got := g.explain(out, "/tmp/houston-git123/deploy_key"); got != want {
			t.Errorf("%q:\n %q\nwant %q", out, got, want)
		}
	}
	if got := g.explain("error: /tmp/k/deploy_key is bad", "/tmp/k/deploy_key"); got != "error: <deploy key> is bad" {
		t.Errorf("the key's path shown: %q", got)
	}
}

// Check: can Houston read the repo, and does it have the branch?
func TestCheck(t *testing.T) {
	f := &fake{answers: map[string]dockercmd.Result{"ls-remote": {OK: true, Output: strings.Repeat("a", 40) + "\trefs/heads/main\n"}}}
	if ok, msg := git(f, t).Check(context.Background(), link); !ok || msg != "Houston can read the repo" {
		t.Errorf("= %v %q", ok, msg)
	}
	other := link
	other.Branch = "develop"
	if ok, msg := git(f, t).Check(context.Background(), other); ok || msg != "Houston can read the repo, but it has no branch develop" {
		t.Errorf("= %v %q", ok, msg)
	}
	f = &fake{answers: map[string]dockercmd.Result{"ls-remote": {Output: "fatal: repository not found\n", Code: 128}}}
	if ok, msg := git(f, t).Check(context.Background(), link); ok || msg != "fatal: repository not found" {
		t.Errorf("= %v %q", ok, msg)
	}
}

// Commit: the repo still has it, or why not.
func TestCommit(t *testing.T) {
	sha := strings.Repeat("d", 40)
	f := &fake{answers: map[string]dockercmd.Result{"rev-parse": {OK: true, Output: sha + "\n"}}}
	if problem := git(f, t).Commit(context.Background(), link, sha); problem != "" {
		t.Errorf("= %q", problem)
	}
	if !strings.Contains(strings.Join(f.seen[1], " "), "fetch --depth 1 --no-tags --filter=blob:none -- git@github.com:scttymn/shop.git "+sha) {
		t.Errorf("fetched %v", f.seen[1])
	}
	f = &fake{answers: map[string]dockercmd.Result{"fetch": {Output: "fatal: remote error: upload-pack: not our ref\n", Code: 128}}}
	if problem := git(f, t).Commit(context.Background(), link, sha); problem != "fatal: remote error: upload-pack: not our ref" {
		t.Errorf("= %q", problem)
	}
	f = &fake{answers: map[string]dockercmd.Result{"rev-parse": {OK: true, Output: strings.Repeat("e", 40)}}}
	if problem := git(f, t).Commit(context.Background(), link, sha); problem != "the repo answered with eeeeeee, not ddddddd" {
		t.Errorf("= %q", problem)
	}
}
