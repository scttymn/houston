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
	}
	if got := RequestFor(p); !reflect.DeepEqual(got, want) {
		t.Errorf("RequestFor =\n%#v\nwant\n%#v", got, want)
	}
}
