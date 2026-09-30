package registry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/scttymn/houston/mission-control-go/app/services/dockercmd"
	"github.com/scttymn/houston/mission-control-go/app/services/dockercmd/dockercmdtest"
)

// A repository's manifests: each tag's digest once, deleted.
func TestDeleteRepository(t *testing.T) {
	var deleted []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/v2/shop/tags/list":
			w.Write([]byte(`{"name":"shop","tags":["a1","a2","gone"]}`))
		case r.Method == "GET":
			http.NotFound(w, r)
		case r.Method == "HEAD" && strings.HasSuffix(r.URL.Path, "/gone"):
			w.WriteHeader(404)
		case r.Method == "HEAD":
			if !strings.Contains(r.Header.Get("Accept"), "application/vnd.oci.image.index.v1+json") {
				w.WriteHeader(400)
				return
			}
			w.Header().Set("Docker-Content-Digest", "sha256:same")
		case r.Method == "DELETE":
			deleted = append(deleted, r.URL.Path)
			w.WriteHeader(202)
		}
	}))
	defer srv.Close()
	r := Registry{URL: srv.URL}
	n, err := r.DeleteRepository(context.Background(), "shop")
	if err != nil || n != 1 || strings.Join(deleted, " ") != "/v2/shop/manifests/sha256:same" {
		t.Errorf("= %d %v, deleted %v", n, err, deleted)
	}
	if n, err := r.DeleteRepository(context.Background(), "never"); n != 0 || err != nil {
		t.Errorf("none: %d %v", n, err)
	}
}

// Its container, whether deletes are on, and its garbage collection.
func TestRegistryContainer(t *testing.T) {
	docker := &dockercmdtest.Fake{}
	docker.On(dockercmdtest.OK("abc123\n"), "ps")
	docker.On(dockercmdtest.OK(`["PATH=/bin","REGISTRY_STORAGE_DELETE_ENABLED=true"]`), "inspect")
	docker.On(dockercmdtest.OK("marking...\n12 blobs and 3 manifests eligible for deletion\n"), "exec")
	r := Registry{ComposeProject: "houston", Docker: docker}
	ctx := context.Background()
	if !r.DeletesEnabled(ctx) {
		t.Error("deletes off")
	}
	if freed, err := r.Collect(ctx); err != nil || freed != "12 blobs and 3 manifests eligible for deletion" {
		t.Errorf("collect %q %v", freed, err)
	}
	if got := strings.Join(docker.Ran(), "\n"); !strings.Contains(got, "ps -q --filter label=com.docker.compose.project=houston --filter label=com.docker.compose.service=registry") ||
		!strings.Contains(got, "exec abc123 registry garbage-collect /etc/distribution/config.yml") {
		t.Errorf("ran %s", got)
	}
	none := &dockercmdtest.Fake{}
	none.On(dockercmd.Result{OK: true}, "ps")
	if _, err := (Registry{ComposeProject: "houston", Docker: none}).Collect(ctx); err == nil || err.Error() != "no registry container (the houston project's registry service) is running" {
		t.Errorf("no container: %v", err)
	}
}
