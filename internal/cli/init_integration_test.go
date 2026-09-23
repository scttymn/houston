//go:build integration

package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestInit_DefaultsRun (docs/plans/init-generic.md, row 11): in a folder with
// only index.html, `houston init` then `houston dev`, `houston test` and
// `houston dev --production` work with the defaults as written.
func TestInit_DefaultsRun(t *testing.T) {
	repo, err := filepath.Abs("../..")
	must(t, err)
	scratch := filepath.Join(repo, ".houston", "test-tmp")
	must(t, os.MkdirAll(scratch, 0o755))
	name := "houston-it-" + randomHex(4)
	dir := filepath.Join(scratch, name)
	must(t, os.MkdirAll(dir, 0o755))
	t.Cleanup(func() { os.RemoveAll(dir) })
	t.Cleanup(func() { removeProjects(name) })
	index, err := os.ReadFile(filepath.Join("testdata", "static", "index.html"))
	must(t, err)
	must(t, os.WriteFile(filepath.Join(dir, "index.html"), index, 0o644))
	// What a real folder has beside the site: none of it may be served.
	must(t, os.WriteFile(filepath.Join(dir, ".env"), []byte("SECRET=do-not-serve\n"), 0o644))
	must(t, os.MkdirAll(filepath.Join(dir, ".git"), 0o755))
	must(t, os.WriteFile(filepath.Join(dir, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o644))
	bin := buildHouston(t)
	composeFile := filepath.Join(dir, "compose.yml")

	if out, err := exec.Command(bin, "-f", composeFile, "init").CombinedOutput(); err != nil {
		t.Fatalf("houston init: %v\n%s", err, out)
	}
	// The host side of the published port only: 8080 on this machine may be
	// taken by something else. The app still listens on 8080 in its container.
	written, err := os.ReadFile(composeFile)
	must(t, err)
	if !bytes.Contains(written, []byte(`ports: ["127.0.0.1:8080:8080"]`)) {
		t.Fatalf("init didn't write the default ports:\n%s", written)
	}
	must(t, os.WriteFile(composeFile, bytes.Replace(written, []byte(`ports: ["127.0.0.1:8080:8080"]`), []byte(`ports: ["127.0.0.1::8080"]`), 1), 0o644))

	// Each variant serves index.html on 8080 inside the app container.
	serves := func(overrides ...string) {
		t.Helper()
		args := []string{"compose", "-p", name, "--project-directory", dir, "-f", composeFile}
		for _, o := range overrides {
			args = append(args, "-f", o)
		}
		args = append(args, "exec", "-T", "app", "wget", "-qO-", "http://127.0.0.1:8080/")
		var body []byte
		for deadline := time.Now().Add(time.Minute); time.Now().Before(deadline); time.Sleep(time.Second) {
			if body, err = exec.Command("docker", args...).Output(); err == nil && bytes.Contains(body, []byte("hello from a static site")) {
				return
			}
		}
		t.Errorf("GET / didn't serve index.html: %v %q", err, body)
	}
	dev := func(args ...string) func() {
		t.Helper()
		var out bytes.Buffer
		cmd := exec.Command(bin, append([]string{"-f", composeFile, "dev"}, args...)...)
		cmd.Stdout, cmd.Stderr = &out, &out
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		must(t, cmd.Start())
		exited := make(chan error, 1)
		go func() { exited <- cmd.Wait() }()
		for deadline := time.Now().Add(3 * time.Minute); !serviceRunning(name, "app"); time.Sleep(time.Second) {
			select {
			case err := <-exited:
				t.Fatalf("houston dev %v exited early (%v):\n%s", args, err, out.String())
			default:
			}
			if time.Now().After(deadline) {
				t.Fatalf("the app never started:\n%s", out.String())
			}
		}
		return func() {
			syscall.Kill(-cmd.Process.Pid, syscall.SIGINT)
			select {
			case <-exited:
			case <-time.After(30 * time.Second):
				syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
				t.Errorf("houston dev %v didn't stop:\n%s", args, out.String())
			}
		}
	}

	stop := dev()
	serves()
	stop()

	// No commands.test: houston test passes without running anything.
	if out, err := exec.Command(bin, "-f", composeFile, "test").CombinedOutput(); err != nil {
		t.Errorf("houston test: %v\n%s", err, out)
	}

	stop = dev("--production")
	production := filepath.Join(dir, ".houston", "compose.production.yml")
	serves(production)
	for _, path := range []string{"/.env", "/.git/HEAD"} {
		out, err := exec.Command("docker", "compose", "-p", name, "--project-directory", dir, "-f", composeFile, "-f", production,
			"exec", "-T", "app", "wget", "-qO-", "http://127.0.0.1:8080"+path).CombinedOutput()
		if err == nil || !bytes.Contains(out, []byte("404")) {
			t.Errorf("GET %s from the production image: %v %q (want 404)", path, err, out)
		}
	}
	stop()
}
