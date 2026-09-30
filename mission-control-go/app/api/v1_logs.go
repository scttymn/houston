package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/scttymn/gantry/web"

	"github.com/scttymn/houston/mission-control-go/app/services/dockercmd"
)

// The most lines a logs request may ask for, and how long one may follow.
const (
	maxTail   = 10_000
	maxFollow = time.Hour
)

// Logs is GET /api/v1/projects/{name}/logs?tail=N&follow=1: the running
// app container's output, streamed (houston logs --server [-f]).
func (c V1) Logs(w http.ResponseWriter, r *http.Request) error {
	p, err := c.project(r)
	if err != nil {
		return err
	}
	tail := r.URL.Query().Get("tail")
	if tail == "" {
		tail = "200"
	}
	if n, err := strconv.Atoi(tail); err != nil || n < 1 || n > maxTail || strings.TrimLeft(tail, "0123456789") != "" {
		return web.Status(http.StatusUnprocessableEntity, errors.New("tail must be 1–"+strconv.Itoa(maxTail)))
	}
	ctx := r.Context()
	ps := c.DockerCLI.Run(ctx, []string{"ps", "--filter", "label=service=" + p.Name, "--filter", "label=role=web", "--format", "{{.Names}}"}, dockercmd.Opts{})
	container, _, _ := strings.Cut(ps.Output, "\n")
	container = strings.TrimSpace(container)
	if !ps.OK || container == "" {
		return web.Status(http.StatusNotFound, errors.New(p.Name+" isn't running"))
	}
	args := []string{"logs", "--timestamps", "--tail", tail}
	var o dockercmd.Opts
	if r.URL.Query().Get("follow") == "1" {
		args = append(args, "--follow")
		o.Timeout = maxFollow
	}
	args = append(args, container)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	c.DockerCLI.Stream(ctx, args, o, flushing{w})
	return nil
}

// flushing sends each write on at once.
type flushing struct{ w http.ResponseWriter }

func (f flushing) Write(p []byte) (int, error) {
	n, err := f.w.Write(p)
	if err == nil {
		http.NewResponseController(f.w).Flush()
	}
	return n, err
}
