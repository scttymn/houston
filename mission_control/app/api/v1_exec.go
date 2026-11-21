package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/scttymn/gantry/web"

	"github.com/scttymn/houston/internal/execstream"
	"github.com/scttymn/houston/mission_control/app/services/dockercmd"
)

// maxExec is the longest an exec may run.
const maxExec = time.Hour

// Exec is POST /api/v1/projects/{name}/exec {"command": [program, args...]}
// (houston exec --server, docs/plans/exec-server.md): docker exec of that
// argv in the project's running app container, no shell and no stdin. Its
// stdout and stderr stream back in frames (internal/execstream), then its
// exit code.
//
// The output is often a one-time credential (gantry's auth:setup-code), so
// it goes only to the answer: never a log, a deploy log or the database.
func (c V1) Exec(w http.ResponseWriter, r *http.Request) error {
	// On every answer, errors too: a 404 without it is a server too old to
	// have this route.
	w.Header().Set(execstream.Header, "1")
	w.Header().Set("Cache-Control", "no-store")
	p, err := c.project(r)
	if err != nil {
		return err
	}
	var body struct {
		Command []string `json:"command"`
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, v1Body+1))
	if err != nil || len(data) > v1Body || json.Unmarshal(data, &body) != nil {
		return web.Status(http.StatusBadRequest, errors.New("the request isn't JSON with command, a list of strings"))
	}
	if len(body.Command) == 0 || body.Command[0] == "" {
		return web.Status(http.StatusUnprocessableEntity, errors.New("say what to run: command, a list of the program and its arguments"))
	}
	ctx := r.Context()
	container, err := c.appContainer(ctx, p.Name)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", execstream.ContentType)
	w.WriteHeader(http.StatusOK)
	frames := execstream.NewWriter(w, func() { http.NewResponseController(w).Flush() })
	// argv as given: docker exec runs it without a shell. A client that goes
	// away cancels ctx, which stops docker exec.
	args := append([]string{"exec", container}, body.Command...)
	res := c.DockerCLI.StreamApart(ctx, args, dockercmd.Opts{Timeout: maxExec}, frames.Stdout(), frames.Stderr())
	code := res.Code
	switch {
	case res.OK:
		code = 0
	case code < 0: // docker itself didn't run
		io.WriteString(frames.Stderr(), "houston: Mission Control couldn't run docker exec\n")
		code = 1
	}
	frames.Exit(code)
	return nil
}
