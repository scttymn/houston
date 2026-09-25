package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/scttymn/houston/internal/docker"
	"github.com/scttymn/houston/internal/variant"
)

// houston-dev-proxy serves every houston dev instance by name
// (docs/plans/dev-localhost.md): kamal-proxy, as on the server, published on
// 127.0.0.1:${HOUSTON_DEV_PORT:-80} and reaching apps on variant.DevNetwork.
const devProxy = "houston-dev-proxy"

// devProxyImage is kamal-proxy pinned by digest: a moved tag can't change
// what runs.
const devProxyImage = "basecamp/kamal-proxy:v0.10.0@sha256:8278e2f5c565bb957962e32fb45eeb5ac7287c6c8033616c26e7784afaa1ebfc"

// devProxyPort is the host port the proxy listens on: HOUSTON_DEV_PORT, or 80.
func devProxyPort() (string, error) {
	port := os.Getenv("HOUSTON_DEV_PORT")
	if port == "" {
		return "80", nil
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return "", fmt.Errorf("HOUSTON_DEV_PORT must be a port number, not %q", port)
	}
	return port, nil
}

// ensureDevProxy makes sure the network exists and the proxy runs, on this
// image and port: it's created when missing, started when stopped, and made
// again when it's an older image or another port.
func ensureDevProxy(d docker.Runner, port string) error {
	if _, err := d.Output("network", "inspect", variant.DevNetwork); err != nil {
		if _, err := d.Output("network", "create", variant.DevNetwork); err != nil {
			return fmt.Errorf("couldn't create the %s network: %v", variant.DevNetwork, err)
		}
	}
	out, err := d.Output("inspect", "-f", `{{.State.Running}} {{.Config.Image}} {{(index (index .HostConfig.PortBindings "80/tcp") 0).HostPort}}`, devProxy)
	if fields := strings.Fields(string(out)); err == nil && len(fields) == 3 {
		if fields[1] == devProxyImage && fields[2] == port {
			if fields[0] == "true" {
				return nil
			}
			if _, err := d.Output("start", devProxy); err != nil {
				return proxyFailed(err, port)
			}
			return nil
		}
		d.Output("rm", "-f", devProxy)
	}
	if _, err := d.Output("run", "-d", "--name", devProxy, "--restart", "unless-stopped", "--network", variant.DevNetwork, "-p", "127.0.0.1:"+port+":80", devProxyImage); err != nil {
		d.Output("rm", "-f", devProxy) // docker run leaves a container it couldn't start
		return proxyFailed(err, port)
	}
	return nil
}

func proxyFailed(err error, port string) error {
	msg := err.Error()
	if strings.Contains(msg, "port is already allocated") || strings.Contains(msg, "address already in use") {
		other := "8080"
		if port == "8080" {
			other = "8081"
		}
		return fmt.Errorf("port %s is taken on this machine, so houston-dev-proxy can't serve *.localhost there; free it, or run with HOUSTON_DEV_PORT=%s (the app is then at <name>.localhost:%s)", port, other, other)
	}
	return fmt.Errorf("couldn't start houston-dev-proxy: %v", err)
}

// nameInUse says which other checkout's running containers hold this
// instance's name, or "".
func nameInUse(d docker.Runner, project, dir string) string {
	out, _ := d.Output("ps", "--filter", "label=com.docker.compose.project="+project, "--format", `{{.Label "com.docker.compose.project.working_dir"}}`)
	for _, line := range strings.Split(string(out), "\n") {
		if line = strings.TrimSpace(line); line != "" && line != dir {
			return line
		}
	}
	return ""
}

// devCheckNameAfter is how long the route may wait before Houston checks
// whether the app refuses its name.
var devCheckNameAfter = 30 * time.Second

// routeWhenUp has the proxy route n.Host to the app once it passes its
// health path (kamal-proxy waits for that), then says where it answers. If
// it's still waiting after devCheckNameAfter, Houston asks the health path
// once itself, with the name the proxy uses: a 403 is a host check refusing
// the name, which it says at once. The returned stop ends the wait and
// removes the route.
func routeWhenUp(d docker.Runner, name string, n devNames, containerPort int, health, hostPort string, stderr io.Writer) (stop func()) {
	ctx, cancel := context.WithCancel(context.Background())
	routed := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		select {
		case <-routed:
			return
		case <-ctx.Done():
			return
		case <-time.After(devCheckNameAfter):
		}
		out, err := d.Output("run", "--rm", "--network", variant.DevNetwork, devCopyImage,
			"wget", "-S", "-q", "-O", "/dev/null", "http://"+n.Alias+":"+strconv.Itoa(containerPort)+health)
		if strings.Contains(string(out)+fmt.Sprint(err), " 403 ") && ctx.Err() == nil {
			fmt.Fprintf(stderr, "houston: %s answered 403 Forbidden: the app refuses that name. If it checks hostnames in development, allow %s there (docs/agents.md › Things that bit us)\n", n.Host, n.Host)
		}
	}()
	go func() {
		defer wg.Done()
		defer close(routed)
		code, err := d.Stream(ctx, "", nil, io.Discard, "exec", devProxy, "kamal-proxy", "deploy", n.Project,
			"--target", n.Alias+":"+strconv.Itoa(containerPort), "--host", n.Host,
			"--health-check-path", health, "--deploy-timeout", "10m", "--target-timeout", "5m")
		url := "http://" + n.Host
		if hostPort != "80" {
			url += ":" + hostPort
		}
		if err == nil && code == 0 {
			fmt.Fprintf(stderr, "houston: %s is up at %s\n", name, url)
			return
		}
		if ctx.Err() != nil || errors.Is(err, context.Canceled) {
			return // compose ended first: nothing to say about the route
		}
		fmt.Fprintf(stderr, "houston: %s isn't routed: the app didn't answer %s within 10 minutes (it's still running; its output is above)\n", n.Host, health)
	}()
	return func() {
		cancel()
		wg.Wait()
		d.Output("exec", devProxy, "kamal-proxy", "remove", n.Project)
	}
}
