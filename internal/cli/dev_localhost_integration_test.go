//go:build integration

package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// houston dev at <name>.localhost against real Docker
// (docs/plans/dev-localhost.md, rows 8 and 12). These run in the `cli` dev
// container, where 127.0.0.1 is the container itself, so requests go to
// houston-dev-proxy from a container on houston-dev, with the Host header a
// browser on the laptop would send.

type devRun struct {
	cmd    *exec.Cmd
	mu     sync.Mutex
	out    bytes.Buffer
	exited chan error
}

func (r *devRun) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.out.Write(p)
}

func (r *devRun) output() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.out.String()
}

// startDev runs houston dev in dir until it says the app is up.
func startDev(t *testing.T, bin, composeFile string, want string) *devRun {
	t.Helper()
	r := &devRun{exited: make(chan error, 1)}
	r.cmd = exec.Command(bin, "-f", composeFile, "dev")
	r.cmd.Stdout, r.cmd.Stderr = r, r
	r.cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	must(t, r.cmd.Start())
	go func() { r.exited <- r.cmd.Wait() }()
	t.Cleanup(func() { r.stop(t) })
	deadline := time.Now().Add(4 * time.Minute)
	for !strings.Contains(r.output(), want) {
		select {
		case err := <-r.exited:
			t.Fatalf("houston dev exited early (%v):\n%s", err, r.output())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("never saw %q:\n%s", want, r.output())
		}
		time.Sleep(time.Second)
	}
	return r
}

func (r *devRun) stop(t *testing.T) {
	if r.cmd.ProcessState != nil {
		return
	}
	syscall.Kill(-r.cmd.Process.Pid, syscall.SIGINT)
	select {
	case <-r.exited:
	case <-time.After(90 * time.Second):
		syscall.Kill(-r.cmd.Process.Pid, syscall.SIGKILL)
		t.Errorf("houston dev didn't stop on Ctrl-C")
	}
}

// throughProxy is what a browser gets at http://<host>/<path>.
func throughProxy(host, path string) (string, error) {
	out, err := exec.Command("docker", "run", "--rm", "--network", "houston-dev", devCopyImage,
		"wget", "-qO-", "--header", "Host: "+host, "http://"+devProxy+"/"+path).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// servable makes a fixture copy answer its health path and say its name.
func servable(t *testing.T, dir, name string) {
	must(t, os.WriteFile(filepath.Join(dir, "up"), []byte("ok\n"), 0o644))
	must(t, os.WriteFile(filepath.Join(dir, "whoami"), []byte(name+"\n"), 0o644))
}

func TestDevLocalhostIntegration(t *testing.T) {
	bin := buildHouston(t)
	var runs []*devRun
	var names []string
	for i := 0; i < 2; i++ {
		dir, composeFile, name := fixtureProject(t, "")
		must(t, os.WriteFile(filepath.Join(dir, ".env"), []byte("APP_SECRET=dev-secret\n"), 0o644))
		servable(t, dir, name)
		runs = append(runs, startDev(t, bin, composeFile, "is up at http://"+name+".localhost"))
		names = append(names, name)
	}
	for _, name := range names {
		if got, err := throughProxy(name+".localhost", "whoami"); err != nil || got != name {
			t.Errorf("%s.localhost answered %q (%v)", name, got, err)
		}
		if ports, _ := exec.Command("docker", "ps", "--filter", "label=com.docker.compose.project="+name, "--format", "{{.Ports}}").Output(); strings.Contains(string(ports), "->") {
			t.Errorf("%s publishes a host port: %s", name, ports)
		}
	}
	runs[0].stop(t)
	if list, _ := exec.Command("docker", "exec", devProxy, "kamal-proxy", "list").CombinedOutput(); strings.Contains(string(list), names[0]) {
		t.Errorf("%s's route stayed after houston dev ended:\n%s", names[0], list)
	}
	if got, _ := throughProxy(names[1]+".localhost", "whoami"); got != names[1] {
		t.Errorf("stopping one stopped the other: %q", got)
	}
}

func TestDevBranchCopyIntegration(t *testing.T) {
	bin := buildHouston(t)
	mainDir, mainFile, name := fixtureProject(t, "")
	branchDir, err := os.MkdirTemp(filepath.Dir(mainDir), "devapp-branch-")
	must(t, err)
	t.Cleanup(func() { os.RemoveAll(branchDir) })
	for _, f := range []string{"Dockerfile", "compose.yml"} {
		b, err := os.ReadFile(filepath.Join(mainDir, f))
		must(t, err)
		must(t, os.WriteFile(filepath.Join(branchDir, f), b, 0o644))
	}
	for dir, branch := range map[string]string{mainDir: "main", branchDir: "feature1"} {
		must(t, os.MkdirAll(filepath.Join(dir, ".git"), 0o755))
		must(t, os.WriteFile(filepath.Join(dir, ".git", "HEAD"), []byte("ref: refs/heads/"+branch+"\n"), 0o644))
		must(t, os.WriteFile(filepath.Join(dir, ".env"), []byte("APP_SECRET=dev-secret\n"), 0o644))
		servable(t, dir, branch)
	}
	in := func(project, dir, file, script string) string {
		out, err := exec.Command("docker", "compose", "-p", project, "--project-directory", dir, "-f", file, "-f", filepath.Join(dir, ".houston", "compose.dev.yml"),
			"exec", "-T", "app", "sh", "-c", script).Output()
		if err != nil {
			t.Fatalf("%s: %s: %v", project, script, err)
		}
		return strings.TrimSpace(string(out))
	}

	startDev(t, bin, mainFile, "is up at http://"+name+".localhost")
	in(name, mainDir, mainFile, "echo main-row > /data/row")

	branchFile := filepath.Join(branchDir, "compose.yml")
	branch := startDev(t, bin, branchFile, "is up at http://feature1."+name+".localhost")
	if !strings.Contains(branch.output(), "copying "+name+"'s data") {
		t.Errorf("no copy:\n%s", branch.output())
	}
	if got := in(name+"-feature1", branchDir, branchFile, "cat /data/row"); got != "main-row" {
		t.Errorf("the branch's data = %q, want main's", got)
	}
	in(name+"-feature1", branchDir, branchFile, "echo branch-row > /data/row")
	if got := in(name, mainDir, mainFile, "cat /data/row"); got != "main-row" {
		t.Errorf("a write in the branch reached main: %q", got)
	}
	if got, _ := throughProxy("feature1."+name+".localhost", "whoami"); got != "feature1" {
		t.Errorf("feature1.%s.localhost answered %q", name, got)
	}
	if paused, _ := exec.Command("docker", "ps", "--filter", "label=com.docker.compose.project="+name, "--filter", "status=paused", "-q").Output(); len(bytes.TrimSpace(paused)) > 0 {
		t.Errorf("main is still paused")
	}
}
