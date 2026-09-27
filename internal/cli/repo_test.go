package cli

import (
	"net/http"
	"strings"
	"testing"
)

// houston repo: a project's repo URL, shown or changed (it moved or was
// renamed on the git host).
func TestRepo(t *testing.T) {
	var method, body string
	remoteServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"/api/v1/projects/garage": respond(garageJSON),
		"/api/v1/projects/garage/repo": func(w http.ResponseWriter, r *http.Request) {
			method, body = r.Method, readBody(r)
			if strings.Contains(body, "shop") {
				w.WriteHeader(http.StatusUnprocessableEntity)
				w.Write([]byte(`{"error":"git@github.com:scttymn/shop.git's compose.yml there names shop, not garage"}`))
				return
			}
			w.Write([]byte(`{"repo_url":"git@github.com:scttymn/garage.git"}`))
		},
	})
	t.Chdir(t.TempDir())

	code, out, errOut := run(&fakeDocker{}, "repo", "--project", "garage")
	if code != 0 || out != "git@forgejo:h/garage.git (main)\n" {
		t.Errorf("show: exit %d, %q %q", code, out, errOut)
	}
	code, out, errOut = run(&fakeDocker{}, "repo", "git@github.com:scttymn/garage.git", "--project", "garage")
	if code != 0 || method != http.MethodPut || body != `{"repo_url":"git@github.com:scttymn/garage.git"}` || !strings.Contains(out, "garage's repo is now git@github.com:scttymn/garage.git") {
		t.Errorf("set: exit %d, %s %s %q %q", code, method, body, out, errOut)
	}
	code, _, errOut = run(&fakeDocker{}, "repo", "git@github.com:scttymn/shop.git", "--project", "garage")
	if code != 1 || !strings.Contains(errOut, "names shop, not garage") {
		t.Errorf("refused: exit %d, %q", code, errOut)
	}
}
