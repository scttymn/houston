package api_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/scttymn/houston/internal/execstream"
	"github.com/scttymn/houston/internal/server"
	"github.com/scttymn/houston/mission_control/app"
	"github.com/scttymn/houston/mission_control/app/services/dockercmd/dockercmdtest"
)

// execApp is the personal API with every log Mission Control writes going
// to the returned buffer, at every level.
func execApp(t *testing.T) (*app.App, http.Handler, *dockercmdtest.Fake, *bytes.Buffer) {
	t.Helper()
	a, _ := remote(t)
	logs := &bytes.Buffer{}
	logger := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	a.Log = logger
	was := slog.Default()
	slog.SetDefault(logger)
	t.Cleanup(func() { slog.SetDefault(was) })
	return a, a.Handler(), a.DockerCLI.(*dockercmdtest.Fake), logs
}

var shopWeb = []string{"ps", "--filter", "label=service=shop", "--filter", "label=role=web", "--format", "{{.Names}}"}

// The command's argv goes to docker exec as an array, its stdout and
// stderr come back apart in frames, then its exit code.
func TestV1Exec(t *testing.T) {
	_, h, fake, _ := execApp(t)
	fake.On(dockercmdtest.OK("shop-web-abc123\nshop-web-old\n"), shopWeb...)
	fake.OnApart("CODE-4f9a\n", "a warning\n", 3, "exec")
	command := []string{"/app", "task", "auth:setup-code; touch /pwned", "$(id)", "a b", ""}

	w := v1(h, "POST", "/projects/shop/exec", personal, `{"command":["/app","task","auth:setup-code; touch /pwned","$(id)","a b",""]}`, nil)

	if w.Code != 200 || w.Header().Get("Content-Type") != execstream.ContentType || w.Header().Get(execstream.Header) != "1" {
		t.Fatalf("= %d %v %q", w.Code, w.Header(), w.Body.String())
	}
	if !strings.Contains(w.Header().Get("Cache-Control"), "no-store") {
		t.Errorf("Cache-Control = %q, want no-store", w.Header().Get("Cache-Control"))
	}
	var out, errOut bytes.Buffer
	code, err := execstream.Read(w.Body, &out, &errOut)
	if err != nil || code != 3 || out.String() != "CODE-4f9a\n" || errOut.String() != "a warning\n" {
		t.Errorf("code %d, err %v, stdout %q, stderr %q", code, err, out.String(), errOut.String())
	}
	calls := fake.Calls()
	last := calls[len(calls)-1]
	if want := append([]string{"exec", "shop-web-abc123"}, command...); !slices.Equal(last.Args, want) {
		t.Errorf("ran %q\nwant %q", last.Args, want)
	}
	if last.Timeout != time.Hour {
		t.Errorf("timeout %v, want an hour", last.Timeout)
	}
}

func TestV1Exec_Succeeds(t *testing.T) {
	_, h, fake, _ := execApp(t)
	fake.On(dockercmdtest.OK("shop-web-abc123\n"), shopWeb...)
	fake.OnApart("ok\n", "", 0, "exec")
	w := v1(h, "POST", "/projects/shop/exec", personal, `{"command":["true"]}`, nil)
	var out, errOut bytes.Buffer
	if code, err := execstream.Read(w.Body, &out, &errOut); err != nil || code != 0 || out.String() != "ok\n" {
		t.Errorf("code %d, err %v, stdout %q", code, err, out.String())
	}
}

// The CLI's client against this route, over real HTTP: argv there and the
// output and code back. And against a router without the route (a server
// too old for it, as v0.5.9 is): ErrNoExec, not "no project".
func TestV1Exec_WithTheCLIClient(t *testing.T) {
	_, h, fake, _ := execApp(t)
	fake.On(dockercmdtest.OK("shop-web-abc123\n"), shopWeb...)
	fake.OnApart("CODE-4f9a\n", "a warning\n", 5, "exec")
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	var out, errOut bytes.Buffer
	code, err := server.New(server.Config{URL: srv.URL, Token: personal}).Exec(context.Background(), "shop", []string{"/app", "task", "a b"}, &out, &errOut)
	if err != nil || code != 5 || out.String() != "CODE-4f9a\n" || errOut.String() != "a warning\n" {
		t.Errorf("code %d, err %v, stdout %q, stderr %q", code, err, out.String(), errOut.String())
	}
	if ran := fake.Ran(); ran[len(ran)-1] != "exec shop-web-abc123 /app task a b" {
		t.Errorf("ran %q", ran[len(ran)-1])
	}

	_, err = server.New(server.Config{URL: srv.URL, Token: personal}).Exec(context.Background(), "nope", []string{"true"}, &out, &errOut)
	if err == nil || err.Error() != "no project nope" {
		t.Errorf("unknown project: %v", err)
	}
	old := server.New(server.Config{URL: srv.URL + "/an-older-houston", Token: personal})
	if _, err := old.Exec(context.Background(), "shop", []string{"true"}, &out, &errOut); !errors.Is(err, server.ErrNoExec) {
		t.Errorf("no such route: %v, want ErrNoExec", err)
	}
}

// What's refused, before any docker exec runs; every answer of the route
// says it's the exec route, so the CLI can tell an old server's 404.
func TestV1Exec_Refusals(t *testing.T) {
	_, h, fake, _ := execApp(t)
	for name, c := range map[string]struct {
		project, token, body string
		code                 int
		want                 string
	}{
		"no token":          {"shop", "", `{"command":["true"]}`, 401, `{"error":"the API token is missing, wrong or revoked"}`},
		"wrong token":       {"shop", "hou_wrong", `{"command":["true"]}`, 401, `{"error":"the API token is missing, wrong or revoked"}`},
		"no project":        {"nope", personal, `{"command":["true"]}`, 404, `{"error":"no project nope"}`},
		"not running":       {"shop", personal, `{"command":["true"]}`, 404, `{"error":"shop isn't running"}`},
		"no command":        {"shop", personal, `{"command":[]}`, 422, `{"error":"say what to run: command, a list of the program and its arguments"}`},
		"empty program":     {"shop", personal, `{"command":[""]}`, 422, `{"error":"say what to run: command, a list of the program and its arguments"}`},
		"command as string": {"shop", personal, `{"command":"rm -rf /"}`, 400, `{"error":"the request isn't JSON with command, a list of strings"}`},
		"not JSON":          {"shop", personal, `nope`, 400, `{"error":"the request isn't JSON with command, a list of strings"}`},
	} {
		t.Run(name, func(t *testing.T) {
			w := v1(h, "POST", "/projects/"+c.project+"/exec", c.token, c.body, nil)
			is(t, w, c.code, c.want)
			if c.code != 401 && w.Header().Get(execstream.Header) != "1" {
				t.Errorf("no %s header on a %d", execstream.Header, w.Code)
			}
		})
	}
	for _, ran := range fake.Ran() {
		if strings.HasPrefix(ran, "exec") {
			t.Errorf("docker exec ran: %q", ran)
		}
	}
}

// The output (a one-time code, say) is never logged or stored: not in any
// log Mission Control writes, at any level, and not in the database.
func TestV1Exec_NeverLogsOrStoresTheOutput(t *testing.T) {
	a, h, fake, logs := execApp(t)
	const code, warning = "SETUP-CODE-7c1e2b", "STDERR-MARK-93d0"
	fake.On(dockercmdtest.OK("shop-web-abc123\n"), shopWeb...)
	fake.OnApart(code+"\n", warning+"\n", 0, "exec")

	w := v1(h, "POST", "/projects/shop/exec", personal, `{"command":["/app","task","auth:setup-code"]}`, nil)
	if !strings.Contains(w.Body.String(), code) {
		t.Fatalf("the code didn't reach the answer: %q", w.Body.String())
	}

	for _, mark := range []string{code, warning} {
		if strings.Contains(logs.String(), mark) {
			t.Errorf("%s is in the log:\n%s", mark, logs.String())
		}
		if where := stored(t, a, mark); where != "" {
			t.Errorf("%s is stored, in %s", mark, where)
		}
	}
}

// stored is the table holding s anywhere in any column, or "".
func stored(t *testing.T, a *app.App, s string) string {
	t.Helper()
	rows, err := a.DB.Read.Query(`SELECT name FROM sqlite_master WHERE type = 'table'`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var name string
		rows.Scan(&name)
		tables = append(tables, name)
	}
	rows.Close()
	for _, table := range tables {
		rows, err := a.DB.Read.Query(`SELECT * FROM "` + table + `"`)
		if err != nil {
			t.Fatal(err)
		}
		cols, _ := rows.Columns()
		for rows.Next() {
			values := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range values {
				ptrs[i] = &values[i]
			}
			rows.Scan(ptrs...)
			for _, v := range values {
				if strings.Contains(fmt.Sprintf("%s", v), s) {
					rows.Close()
					return table
				}
			}
		}
		rows.Close()
	}
	return ""
}
