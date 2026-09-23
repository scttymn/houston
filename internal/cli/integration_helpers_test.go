//go:build integration

package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// fixtureProject copies testdata/devapp under the repo (the `cli` dev
// container mounts the repo at the host's path, so the host daemon can see
// bind mounts there, unlike /tmp) with a unique project name. It returns the
// dir, the compose file, and the name.
func fixtureProject(t *testing.T, testCommand string) (dir, composeFile, name string) {
	t.Helper()
	repo, err := filepath.Abs("../..")
	must(t, err)
	scratch := filepath.Join(repo, ".houston", "test-tmp")
	must(t, os.MkdirAll(scratch, 0o755))
	dir, err = os.MkdirTemp(scratch, "devapp-")
	must(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })

	name = "houston-it-" + randomHex(4)
	for _, f := range []string{"Dockerfile", "compose.yml"} {
		b, err := os.ReadFile(filepath.Join("testdata", "devapp", f))
		must(t, err)
		b = bytes.ReplaceAll(b, []byte("NAME_SET_BY_TEST"), []byte(name))
		if testCommand != "" && f == "compose.yml" {
			b = regexp.MustCompile(`(?m)^  commands:.*$`).ReplaceAll(b, []byte("  commands: { test: '"+testCommand+"' }"))
		}
		must(t, os.WriteFile(filepath.Join(dir, f), b, 0o644))
	}
	t.Cleanup(func() { removeProjects(name) })
	return dir, filepath.Join(dir, "compose.yml"), name
}

// buildHouston builds the real binary once per test.
func buildHouston(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "houston")
	if out, err := exec.Command("go", "build", "-o", bin, "github.com/sevenmoons/houston/cmd/houston").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

// composeProjects lists compose projects (containers, volumes, networks)
// whose name starts with prefix.
func composeProjects(prefix string) []string {
	seen := map[string]bool{}
	for _, kind := range []string{"ps -a", "volume ls", "network ls"} {
		args := append(strings.Fields(kind), "--format", `{{.Label "com.docker.compose.project"}}`)
		out, _ := exec.Command("docker", args...).Output()
		for _, p := range strings.Fields(string(out)) {
			if strings.HasPrefix(p, prefix) {
				seen[p] = true
			}
		}
	}
	var list []string
	for p := range seen {
		list = append(list, p)
	}
	return list
}

// removeProjects tears down every compose project named name or name-*.
func removeProjects(name string) {
	for _, p := range composeProjects(name) {
		exec.Command("docker", "compose", "-p", p, "down", "-v", "--rmi", "local", "--remove-orphans").Run()
	}
}
