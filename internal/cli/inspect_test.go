package cli

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/sevenmoons/houston/internal/mission"
	"github.com/sevenmoons/houston/internal/project"
)

const inspectable = `name: phoenixapp
services:
  app:
    build: .
    ports: ["4000:4000"]
    environment:
      DATABASE_URL: postgres://postgres:${POSTGRES_PASSWORD}@${DB_HOST:-db}/phoenixapp
      SECRET_KEY_BASE: ${SECRET_KEY_BASE}
    volumes:
      - media:/media
    deploy: { resources: { limits: { cpus: "2", memory: 2g } } }
  db:
    image: postgres:17
    environment:
      POSTGRES_PASSWORD: ${POSTGRES_PASSWORD}
    volumes:
      - pgdata:/var/lib/postgresql/data
  cache:
    image: redis:7
volumes:
  media:
  pgdata:
x-houston:
  health: /health
  domains: [phoenixapp.com]
  commands: { test: mix test }
  backups: { schedule: "daily 04:30", keep: { auto: 7, deploy: 5 } }
`

func TestInspect(t *testing.T) {
	_, path := newProject(t, inspectable, nil)
	d := &fakeDocker{}

	code, stdout, stderr := run(d, "-f", path, "inspect", "--json")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, stderr)
	}
	var got struct {
		Sync    mission.SyncRequest `json:"sync"`
		Preview map[string]any      `json:"preview"`
	}
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, stdout)
	}
	p, err := project.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := mission.RequestFor(p); !reflect.DeepEqual(got.Sync, want) {
		t.Errorf("sync =\n%#v\nwant exactly what houston deploy sends:\n%#v", got.Sync, want)
	}
	wantPreview := map[string]any{
		"services": []any{
			map[string]any{"name": "app", "image": "", "app": true},
			map[string]any{"name": "cache", "image": "redis:7", "app": false},
			map[string]any{"name": "db", "image": "postgres:17", "app": false},
		},
		"port": float64(4000), "health": "/health", "cpus": "2", "memory": "2 GB", "test": true,
		"backups": map[string]any{"schedule": "daily 04:30", "keep_auto": float64(7), "keep_deploy": float64(5), "volumes": []any{"media", "pgdata"}},
	}
	if !reflect.DeepEqual(got.Preview, wantPreview) {
		t.Errorf("preview =\n%#v\nwant\n%#v", got.Preview, wantPreview)
	}

	code, stdout, _ = run(d, "-f", path, "inspect")
	if code != 0 {
		t.Fatalf("human output: exit %d", code)
	}
	for _, want := range []string{"phoenixapp", "port 4000", "health /health", "2 CPUs", "2 GB", "cache (redis:7)", "db (postgres:17)",
		"phoenixapp.com", "every commit to main", "tests run first", "POSTGRES_PASSWORD (required)", "daily 04:30", "media, pgdata"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("human output lacks %q:\n%s", want, stdout)
		}
	}

	_, broken := newProject(t, strings.Replace(inspectable, "health: /health", "health: health", 1), nil)
	code, stdout, stderr = run(d, "-f", broken, "inspect", "--json")
	if code != exitUsage || stdout != "" || !strings.Contains(stderr, "x-houston.health") {
		t.Errorf("broken file: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	code, _, stderr = run(d, "-f", path+".missing", "inspect", "--json")
	if code != exitUsage || !strings.Contains(stderr, "not found") {
		t.Errorf("missing file: exit %d, stderr %q", code, stderr)
	}
	if d.calls() != 0 {
		t.Errorf("inspect ran docker %d times", d.calls())
	}
}
