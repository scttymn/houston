package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// houston dev at <name>.localhost through houston-dev-proxy
// (docs/plans/dev-localhost.md, rows 3–6 and 11).

var devEnv = map[string]string{".env": "POSTGRES_PASSWORD=x\nSECRET_KEY_BASE=y\n"}

// proxyState answers `docker inspect houston-dev-proxy` with running, image
// and host port, or as missing.
func proxyState(state string) func([]string) ([]byte, error, bool) {
	return func(args []string) ([]byte, error, bool) {
		switch {
		case len(args) > 1 && args[0] == "network" && args[1] == "inspect":
			return nil, nil, true
		case args[0] == "inspect" && args[len(args)-1] == devProxy:
			if state == "" {
				return nil, errors.New("Error: No such object: houston-dev-proxy"), true
			}
			return []byte(state + "\n"), nil, true
		}
		return nil, nil, false
	}
}

func proxyRun(port string) []string {
	return []string{"run", "-d", "--name", devProxy, "--restart", "unless-stopped", "--network", "houston-dev", "-p", "127.0.0.1:" + port + ":80", devProxyImage}
}

func TestDevStartsTheProxy(t *testing.T) {
	_, path := newProject(t, phoenix, devEnv)

	missing := &fakeDocker{outputFn: func(args []string) ([]byte, error, bool) {
		if args[0] == "network" && args[1] == "inspect" {
			return nil, errors.New("Error: No such network: houston-dev"), true
		}
		if args[0] == "inspect" {
			return nil, errors.New("Error: No such object"), true
		}
		return nil, nil, false
	}}
	if code, _, stderr := run(missing, "-f", path, "dev"); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if !missing.called("network", "create", "houston-dev") || !missing.called(proxyRun("80")...) {
		t.Errorf("missing: outputs %q", missing.outputs)
	}

	running := &fakeDocker{outputFn: proxyState("true " + devProxyImage + " 80")}
	run(running, "-f", path, "dev")
	if running.called(proxyRun("80")...) || running.called("start", devProxy) {
		t.Errorf("running: started again: %q", running.outputs)
	}

	stopped := &fakeDocker{outputFn: proxyState("false " + devProxyImage + " 80")}
	run(stopped, "-f", path, "dev")
	if !stopped.called("start", devProxy) || stopped.called(proxyRun("80")...) {
		t.Errorf("stopped: %q", stopped.outputs)
	}

	old := &fakeDocker{outputFn: proxyState("true basecamp/kamal-proxy:v0.8.0 80")}
	run(old, "-f", path, "dev")
	if !old.called("rm", "-f", devProxy) || !old.called(proxyRun("80")...) {
		t.Errorf("an older proxy image: %q", old.outputs)
	}
}

func TestDevPortTaken(t *testing.T) {
	_, path := newProject(t, phoenix, devEnv)
	taken := proxyState("")
	d := &fakeDocker{outputFn: func(args []string) ([]byte, error, bool) {
		if args[0] == "run" {
			return nil, errors.New("docker: Error response from daemon: failed to set up container networking: Bind for 127.0.0.1:80 failed: port is already allocated"), true
		}
		return taken(args)
	}}
	code, _, stderr := run(d, "-f", path, "dev")
	if code != 1 || !strings.Contains(stderr, "port 80 is taken") || !strings.Contains(stderr, "HOUSTON_DEV_PORT=8080") || len(d.runs) != 0 {
		t.Errorf("exit %d, compose runs %q: %s", code, d.runs, stderr)
	}
	if !d.called("rm", "-f", devProxy) {
		t.Errorf("the half-made proxy container is left: %q", d.outputs)
	}

	t.Setenv("HOUSTON_DEV_PORT", "8080")
	d = &fakeDocker{outputFn: proxyState("")}
	_, _, stderr = run(d, "-f", path, "dev")
	if !d.called(proxyRun("8080")...) || !strings.Contains(stderr, "is up at http://phoenixapp.localhost:8080\n") {
		t.Errorf("HOUSTON_DEV_PORT: %q: %s", d.outputs, stderr)
	}
	t.Setenv("HOUSTON_DEV_PORT", "eighty")
	if code, _, stderr := run(&fakeDocker{}, "-f", path, "dev"); code != 2 || !strings.Contains(stderr, "HOUSTON_DEV_PORT") {
		t.Errorf("a bad HOUSTON_DEV_PORT: exit %d: %s", code, stderr)
	}
}

func TestDevRoutesAndCleansUp(t *testing.T) {
	dir, path := newProject(t, phoenix, devEnv)
	d := &fakeDocker{outputFn: proxyState("true " + devProxyImage + " 80")}
	code, _, stderr := run(d, "-f", path, "dev")
	want := []string{"exec", devProxy, "kamal-proxy", "deploy", "phoenixapp", "--target", "phoenixapp.localhost:4000", "--host", "phoenixapp.localhost",
		"--health-check-path", "/health", "--deploy-timeout", "10m", "--target-timeout", "5m"}
	if code != 0 || len(d.streams) != 1 || !reflect.DeepEqual(d.streams[0], want) {
		t.Fatalf("exit %d, streams %q\nwant %q\n%s", code, d.streams, want, stderr)
	}
	if !strings.Contains(stderr, "houston: phoenixapp is up at http://phoenixapp.localhost\n") {
		t.Errorf("stderr = %q", stderr)
	}
	if !d.called("exec", devProxy, "kamal-proxy", "remove", "phoenixapp") {
		t.Errorf("the route stays after houston dev ends: %q", d.outputs)
	}

	d = &fakeDocker{outputFn: proxyState("true " + devProxyImage + " 80")}
	run(d, "-f", path, "dev", "--production")
	if len(d.streams) != 1 || d.streams[0][4] != "phoenixapp-production" || d.streams[0][6] != "phoenixapp-production.localhost:4000" || d.streams[0][8] != "phoenixapp-production.localhost" {
		t.Errorf("--production: %q", d.streams)
	}

	run(&fakeDocker{outputFn: proxyState("true " + devProxyImage + " 80")}, "-f", path, "dev", "--ports")
	override, _ := os.ReadFile(filepath.Join(dir, ".houston", "compose.dev.yml"))
	if strings.Contains(string(override), "!reset") || !strings.Contains(string(override), "target: dev") {
		t.Errorf("--ports keeps compose.yml's ports:\n%s", override)
	}
}

func TestDevUnhealthyApp(t *testing.T) {
	_, path := newProject(t, phoenix, devEnv)
	d := &fakeDocker{outputFn: proxyState("true " + devProxyImage + " 80"), runExit: 0,
		streamFn: func(ctx context.Context, args []string) (int, error) { return 1, nil },
		onRun:    func() { time.Sleep(20 * time.Millisecond) }}
	code, _, stderr := run(d, "-f", path, "dev")
	if code != 0 || !strings.Contains(stderr, "phoenixapp.localhost isn't routed") || !strings.Contains(stderr, "/health") || strings.Contains(stderr, "is up at") {
		t.Errorf("exit %d: %q", code, stderr)
	}

	// compose ends before the app is healthy: houston dev returns at once,
	// the wait stops, and nothing claims the route failed.
	d = &fakeDocker{outputFn: proxyState("true " + devProxyImage + " 80"), runExit: 1,
		streamFn: func(ctx context.Context, args []string) (int, error) { <-ctx.Done(); return 1, ctx.Err() }}
	done := make(chan struct{})
	go func() { code, _, stderr = run(d, "-f", path, "dev"); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("houston dev kept waiting for the route after compose ended")
	}
	if code != 1 || strings.Contains(stderr, "isn't routed") {
		t.Errorf("exit %d: %q", code, stderr)
	}
}

func TestDevNameInUse(t *testing.T) {
	dir, path := newProject(t, phoenix, devEnv)
	d := &fakeDocker{outputFn: proxyState("true " + devProxyImage + " 80"), psOut: "/Users/someone/other-checkout\n"}
	code, _, stderr := run(d, "-f", path, "dev")
	if code != 2 || !strings.Contains(stderr, "/Users/someone/other-checkout") || !strings.Contains(stderr, "--as") || len(d.runs) != 0 {
		t.Errorf("exit %d, runs %q: %s", code, d.runs, stderr)
	}
	d = &fakeDocker{outputFn: proxyState("true " + devProxyImage + " 80"), psOut: dir + "\n"}
	if code, _, stderr := run(d, "-f", path, "dev"); code != 0 {
		t.Errorf("its own containers: exit %d: %s", code, stderr)
	}
}

// A host check that refuses the instance's name (a branch's
// feature1.equip.localhost, say) is named at once, not after the proxy's
// 10-minute wait: Houston checks the health path itself once, with the name
// the proxy uses (docs/plans/dev-localhost.md, row 6).
func TestDevSaysWhenTheAppRefusesItsName(t *testing.T) {
	defer func(d time.Duration) { devCheckNameAfter = d }(devCheckNameAfter)
	devCheckNameAfter = 10 * time.Millisecond
	_, path := newProject(t, phoenix, devEnv)
	refusing := func(args []string) ([]byte, error, bool) {
		if args[0] == "run" && contains(args, "--network") && args[len(args)-1] == "http://phoenixapp.localhost:4000/health" {
			return []byte("  HTTP/1.1 403 Forbidden\n"), errors.New("wget: server returned error: HTTP/1.1 403 Forbidden"), true
		}
		return proxyState("true " + devProxyImage + " 80")(args)
	}
	d := &fakeDocker{outputFn: refusing, onRun: func() { time.Sleep(200 * time.Millisecond) },
		streamFn: func(ctx context.Context, args []string) (int, error) { <-ctx.Done(); return 1, ctx.Err() }}
	_, _, stderr := run(d, "-f", path, "dev")
	if !strings.Contains(stderr, "phoenixapp.localhost answered 403") || !strings.Contains(stderr, "allow phoenixapp.localhost") {
		t.Errorf("stderr = %q", stderr)
	}

	answering := func(args []string) ([]byte, error, bool) {
		if args[0] == "run" && contains(args, "--network") && strings.HasSuffix(args[len(args)-1], "/health") {
			return []byte("  HTTP/1.1 200 OK\n"), nil, true
		}
		return proxyState("true " + devProxyImage + " 80")(args)
	}
	d = &fakeDocker{outputFn: answering, onRun: func() { time.Sleep(200 * time.Millisecond) },
		streamFn: func(ctx context.Context, args []string) (int, error) { <-ctx.Done(); return 1, ctx.Err() }}
	if _, _, stderr := run(d, "-f", path, "dev"); strings.Contains(stderr, "answered 403") {
		t.Errorf("a healthy app got the hint: %q", stderr)
	}
}
