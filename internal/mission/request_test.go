package mission

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/sevenmoons/houston/internal/project"
)

func TestRequestFor(t *testing.T) {
	p, err := project.Parse(filepath.Join(t.TempDir(), "compose.yml"), []byte(`name: shop
services:
  web:
    build: .
    ports: ["80:80"]
    environment:
      DATABASE_URL: postgres://u:${DB_PASSWORD}@${DB_HOST:-db}/shop
      LEVEL: ${LEVEL:-info}
  db:
    image: postgres:17
x-houston:
  health: /up
  deploy: { on: tag, tags: "release-*" }
`))
	if err != nil {
		t.Fatal(err)
	}
	want := SyncRequest{
		Name: "shop", AppService: "web", Services: []string{"db", "web"}, Domains: []string{},
		Variables: []Variable{{Name: "DB_PASSWORD", Required: true}, {Name: "LEVEL", Required: false}},
		Health:    "/up", Port: 80,
		DeployRule: DeployRule{On: "tag", Branch: p.Houston.Deploy.Branch, Tags: "release-*"},
		Volumes:    []Volume{}, Databases: []Database{{Service: "db", Image: "postgres:17"}},
	}
	if got := RequestFor(p); !reflect.DeepEqual(got, want) {
		t.Errorf("RequestFor =\n%#v\nwant\n%#v", got, want)
	}
}

// The data a backup holds: the app's named volumes, and the services whose
// image repository ends in postgres (spec §9).
func TestRequestForData(t *testing.T) {
	p, err := project.Parse(filepath.Join(t.TempDir(), "compose.yml"), []byte(`name: shop
services:
  app:
    build: .
    ports: ["80:80"]
    volumes: [".:/app", "media:/media", "storage:/rails/storage:ro"]
  pg: { image: "postgres:17" }
  pg2: { image: "docker.io/library/postgres:17-alpine" }
  pg3: { image: "ghcr.io/x/my-postgres@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" }
  cache: { image: "redis:7" }
  ts: { image: "timescale/timescaledb:latest-pg17" }
  pgadmin: { image: "dpage/pgadmin4" }
  notpg: { image: "postgres-exporter:1" }
  local: { image: "localhost:5000/postgres" }
volumes: { media: {}, storage: {} }
x-houston:
  health: /up
`))
	if err != nil {
		t.Fatal(err)
	}
	got := RequestFor(p)
	if want := []Volume{{Name: "media", Path: "/media"}, {Name: "storage", Path: "/rails/storage"}}; !reflect.DeepEqual(got.Volumes, want) {
		t.Errorf("Volumes = %#v, want %#v", got.Volumes, want)
	}
	want := []Database{
		{Service: "local", Image: "localhost:5000/postgres"},
		{Service: "pg", Image: "postgres:17"},
		{Service: "pg2", Image: "docker.io/library/postgres:17-alpine"},
		{Service: "pg3", Image: "ghcr.io/x/my-postgres@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"},
	}
	if !reflect.DeepEqual(got.Databases, want) {
		t.Errorf("Databases = %#v, want %#v", got.Databases, want)
	}
}
