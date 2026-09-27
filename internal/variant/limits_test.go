package variant

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/scttymn/houston/internal/project"
)

// Production's limits leave dev and test only for the app, whose container
// there builds and runs tests (docs/plans/dev-test-limits.md, row 3): an app
// without limits gets no reset, and an accessory keeps its own, since it runs
// the same image as in production.
func TestOverridesLeaveOtherLimits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "compose.yml")
	compose := "name: demo\nservices:\n  app:\n    build: .\n    expose: [\"8080\"]\n  db:\n    image: postgres:17\n    deploy:\n      resources: { limits: { memory: 256M } }\nx-houston:\n  health: /up\n"
	if err := os.WriteFile(path, []byte(compose), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := project.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	for name, got := range map[string][]byte{
		"DevOverride":        DevOverride(p, Route{Alias: "demo.localhost"}),
		"DevOverride, ports": DevOverride(p, Route{KeepPorts: true}),
		"TestOverride":       TestOverride(p),
	} {
		if strings.Contains(string(got), "deploy") {
			t.Errorf("%s touches limits it should leave:\n%s", name, got)
		}
	}
}
