package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestSecrets(t *testing.T) {
	var puts []map[string]string
	var calls []string
	remoteServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"/api/v1/projects/garage/secrets": respond(`{"secrets":[{"name":"RAILS_MASTER_KEY","required":true,"set":true},{"name":"SENTRY_DSN","required":false,"set":false}]}`),
		"/api/v1/projects/garage/secrets/SENTRY_DSN": func(w http.ResponseWriter, r *http.Request) {
			calls = append(calls, r.Method+" SENTRY_DSN")
			if r.Method == http.MethodPut {
				var body map[string]string
				json.NewDecoder(r.Body).Decode(&body)
				puts = append(puts, body)
				if strings.Contains(body["value"], `\`) {
					w.WriteHeader(http.StatusUnprocessableEntity)
					io.WriteString(w, `{"error":"Value can't contain a backslash … Base64-encode it instead."}`)
					return
				}
			}
			io.WriteString(w, `{"name":"SENTRY_DSN","set":true}`)
		},
		"/api/v1/projects/garage/secrets/SENTRY_DSN/generate": func(w http.ResponseWriter, r *http.Request) {
			calls = append(calls, r.Method+" generate")
			io.WriteString(w, `{"name":"SENTRY_DSN","set":true}`)
		},
	})
	terminal(t, false)

	code, out, errOut := run(&fakeDocker{}, "secrets", "list", "--project", "garage")
	if code != 0 || !strings.Contains(out, "RAILS_MASTER_KEY") || !strings.Contains(out, "set") || !strings.Contains(out, "SENTRY_DSN") || !strings.Contains(out, "not set") {
		t.Errorf("list: exit %d\n%s%s", code, out, errOut)
	}

	code, out, errOut = runWithInput(&fakeDocker{}, "https://sentry.example/1 with spaces\n", "secrets", "set", "SENTRY_DSN", "--project", "garage")
	if code != 0 || len(puts) != 1 || puts[0]["value"] != "https://sentry.example/1 with spaces" {
		t.Errorf("set: exit %d, sent %v\n%s", code, puts, errOut)
	}
	if strings.Contains(out+errOut, "sentry.example") {
		t.Errorf("set printed the value:\n%s%s", out, errOut)
	}

	code, _, errOut = runWithInput(&fakeDocker{}, `C:\data`+"\n", "secrets", "set", "SENTRY_DSN", "--project", "garage")
	if code != exitFailure || !strings.Contains(errOut, "Base64-encode") {
		t.Errorf("refused value: exit %d, %q", code, errOut)
	}

	if code, _, _ := run(&fakeDocker{}, "secrets", "set", "SENTRY_DSN", "a-value-on-the-command-line", "--project", "garage"); code != exitUsage {
		t.Errorf("a value in argv: exit %d, want %d (usage)", code, exitUsage)
	}

	run(&fakeDocker{}, "secrets", "unset", "SENTRY_DSN", "--project", "garage")
	run(&fakeDocker{}, "secrets", "generate", "SENTRY_DSN", "--project", "garage")
	if strings.Join(calls, ",") != "PUT SENTRY_DSN,PUT SENTRY_DSN,DELETE SENTRY_DSN,POST generate" {
		t.Errorf("calls = %v", calls)
	}
}
