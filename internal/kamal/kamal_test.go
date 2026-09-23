package kamal

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/sevenmoons/houston/internal/project"
)

var target = Target{BaseDomain: "svnmns.com", Arch: "amd64"}

func load(t *testing.T, path string) *project.Project {
	t.Helper()
	p, err := project.Load(path)
	if err != nil {
		t.Fatalf("loading %s: %v", path, err)
	}
	return p
}

// parse builds a project from compose text, for the table tests.
func parse(t *testing.T, compose string) *project.Project {
	t.Helper()
	p, err := project.Parse(filepath.Join(t.TempDir(), "compose.yml"), []byte(compose))
	if err != nil {
		t.Fatalf("fixture doesn't load: %v", err)
	}
	return p
}

func config(t *testing.T, p *project.Project, tg Target) map[string]any {
	t.Helper()
	out, err := Config(p, tg)
	if err != nil {
		t.Fatalf("Config: %v", err)
	}
	var m map[string]any
	if err := yaml.Unmarshal(out, &m); err != nil {
		t.Fatalf("Config produced invalid YAML: %v\n%s", err, out)
	}
	return m
}

// assertGolden compares Config's output with a hand-written expected file as
// YAML values, so layout isn't pinned but every key and value is.
func assertGolden(t *testing.T, got map[string]any, golden string) {
	t.Helper()
	data, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	var want map[string]any
	if err := yaml.Unmarshal(data, &want); err != nil {
		t.Fatalf("%s: %v", golden, err)
	}
	if !reflect.DeepEqual(got, want) {
		gotYAML, _ := yaml.Marshal(got)
		t.Errorf("config differs from %s\n--- got\n%s--- want\n%s", golden, gotYAML, data)
	}
}

func dig(m map[string]any, keys ...string) any {
	var v any = m
	for _, k := range keys {
		mm, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = mm[k]
	}
	return v
}

func TestConfigRailsSQLite(t *testing.T) {
	assertGolden(t, config(t, load(t, "testdata/rails.compose.yml"), target), "testdata/rails.deploy.yml")
}

func TestConfigPostgresAccessory(t *testing.T) {
	assertGolden(t, config(t, load(t, "testdata/phoenix.compose.yml"), target), "testdata/phoenix.deploy.yml")
}

// The spike deploys spike.deploy.yml on a real server; this keeps it equal to
// what the generator writes.
func TestConfigSpikeFixture(t *testing.T) {
	assertGolden(t, config(t, load(t, "testdata/spike/compose.yml"), Target{BaseDomain: "houston.test", Arch: "arm64"}), "testdata/spike.deploy.yml")
	secrets, err := SecretsFile(load(t, "testdata/spike/compose.yml"))
	if err != nil {
		t.Fatal(err)
	}
	want, _ := os.ReadFile("testdata/spike.secrets")
	if string(secrets) != string(want) {
		t.Errorf("secrets file:\n%s\nwant:\n%s", secrets, want)
	}
}

func TestEnvClassification(t *testing.T) {
	p := parse(t, `name: shop
services:
  app:
    build: .
    ports: ["80:80"]
    environment:
      LITERAL: hello
      ESCAPED: "cost $$5 and $${HOME}"
      ONLY_HOSTS: redis://${CACHE_HOST:-cache}:6379/${DB_HOST:-db}
      EXACT: ${TOKEN}
      EXACT_BARE: $TOKEN
      EXACT_REQUIRED: ${TOKEN:?set TOKEN}
      RENAMED: ${API_KEY}
      WITH_DEFAULT: ${LEVEL:-info}
      NOT_A_SERVICE: ${QUEUE_HOST:-queue}
      COMPOSITE: postgres://u:${DB_PASSWORD}@${DB_HOST:-db}/shop
  db:
    image: postgres:17
  cache:
    image: redis:7
x-houston:
  health: /up
`)
	m := config(t, p, target)

	clear, _ := dig(m, "env", "clear").(map[string]any)
	wantClear := map[string]any{
		"LITERAL":    "hello",
		"ESCAPED":    "cost $5 and ${HOME}",
		"ONLY_HOSTS": "redis://shop-cache:6379/shop-db",
	}
	if !reflect.DeepEqual(clear, wantClear) {
		t.Errorf("clear env = %#v\nwant %#v", clear, wantClear)
	}

	secret, _ := dig(m, "env", "secret").([]any)
	wantSecret := []any{
		"COMPOSITE:APP__COMPOSITE",
		"EXACT:TOKEN",
		"EXACT_BARE:TOKEN",
		"EXACT_REQUIRED:TOKEN",
		"NOT_A_SERVICE:APP__NOT_A_SERVICE",
		"RENAMED:API_KEY",
		"WITH_DEFAULT:APP__WITH_DEFAULT",
	}
	if !reflect.DeepEqual(secret, wantSecret) {
		t.Errorf("secret env = %#v\nwant %#v", secret, wantSecret)
	}
}

func TestProxyHosts(t *testing.T) {
	p := parse(t, `name: shop
services:
  app:
    build: .
    ports: ["80:80"]
x-houston:
  health: /up
  domains: [shop.example.com, shop.svnmns.com, www.shop.example.com]
`)
	got := dig(config(t, p, target), "proxy", "hosts")
	want := []any{"shop.svnmns.com", "shop.example.com", "www.shop.example.com"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("proxy hosts = %v, want %v", got, want)
	}
}

func TestResourceLimits(t *testing.T) {
	for _, tc := range []struct {
		limits string
		want   any
	}{
		{`deploy: { resources: { limits: { cpus: "0.5", memory: 512m } } }`, map[string]any{"cpus": "0.5", "memory": "512m"}},
		{`deploy: { resources: { limits: { memory: 1536m } } }`, map[string]any{"memory": "1536m"}},
		{`deploy: { resources: { limits: { memory: "1000" } } }`, map[string]any{"memory": "1000"}},
		{``, nil},
	} {
		p := parse(t, `name: shop
services:
  app:
    build: .
    ports: ["80:80"]
    `+tc.limits+`
x-houston:
  health: /up
`)
		if got := dig(config(t, p, target), "servers", "web", "options"); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%q: options = %#v, want %#v", tc.limits, got, tc.want)
		}
	}
}

func TestConfigEscapesERB(t *testing.T) {
	p := parse(t, `name: shop
services:
  app:
    build: .
    ports: ["80:80"]
    command: ["sh", "-c", "echo <%= 'app' %>"]
    environment:
      GREETING: "<%= File.read('/etc/passwd') %>"
  worker:
    image: busybox
    command: echo "<%- 1 -%>"
    healthcheck:
      test: ["CMD", "echo", "<%= 2 %>"]
x-houston:
  health: /up
`)
	out, err := Config(p, target)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(bytes.ReplaceAll(out, []byte("<%%"), nil), []byte("<%")) {
		t.Errorf("unescaped <%% in the output:\n%s", out)
	}
	var m map[string]any
	if err := yaml.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	if got := dig(m, "env", "clear", "GREETING"); got != "<%%= File.read('/etc/passwd') %>" {
		t.Errorf("GREETING = %q", got)
	}
	if got := dig(m, "servers", "web", "cmd"); got != `sh -c 'echo <%%= '\''app'\'' %>'` {
		t.Errorf("app cmd = %q", got)
	}
}

func TestConfigRoundTripsHostileValues(t *testing.T) {
	hostile := []string{"key: value", "- item", "# not a comment", "{x: 1}", "'single' \"double\"", "yes", "0123", "~", "café ☕"}
	var env strings.Builder
	for i, v := range hostile {
		env.WriteString("      V" + string(rune('A'+i)) + ": " + strconv.Quote(v) + "\n")
	}
	p := parse(t, `name: shop
services:
  app:
    build: .
    ports: ["80:80"]
    environment:
`+env.String()+`x-houston:
  health: /up
`)
	clear, _ := dig(config(t, p, target), "env", "clear").(map[string]any)
	for i, v := range hostile {
		key := "V" + string(rune('A'+i))
		if clear[key] != v {
			t.Errorf("%s = %#v, want %#v", key, clear[key], v)
		}
	}
}

func problemPaths(t *testing.T, err error) []string {
	t.Helper()
	var pe *project.Errors
	if !errors.As(err, &pe) {
		t.Fatalf("error = %v (%T), want *project.Errors", err, err)
	}
	var paths []string
	for _, p := range pe.Problems {
		paths = append(paths, p.Path)
	}
	return paths
}

func TestConfigRejectsUnsafeSecretNames(t *testing.T) {
	p := parse(t, `name: shop
services:
  app:
    build: .
    ports: ["80:80"]
    environment:
      my.key: ${TOKEN}
      URL: x-${TOKEN}
      url: y-${TOKEN}
x-houston:
  health: /up
`)
	out, err := Config(p, target)
	if out != nil {
		t.Errorf("got output with an error:\n%s", out)
	}
	want := []string{"services.app.environment.URL", "services.app.environment.my.key", "services.app.environment.url"}
	if got := problemPaths(t, err); !reflect.DeepEqual(got, want) {
		t.Errorf("problem paths = %v, want %v\n%v", got, want, err)
	}
	if _, err := SecretsFile(p); err == nil {
		t.Error("SecretsFile accepted the same names")
	}
}

// A secret can only reach the server as environment: Kamal's cmd, image and
// docker options are plain text on the host. And Kamal leaves ${…} in option
// values unescaped for the host shell, so it's refused there too.
func TestConfigRejectsSecretsOutsideEnvironment(t *testing.T) {
	p := parse(t, `name: shop
services:
  app:
    build: .
    ports: ["80:80"]
    command: ["serve", "--token", "${TOKEN}"]
  cache:
    image: redis:${REDIS_VERSION:-7}
    command: redis-server --port 6379
  db:
    image: postgres:17
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U $${POSTGRES_USER}"]
x-houston:
  health: /up
`)
	out, err := Config(p, target)
	if out != nil {
		t.Errorf("got output with an error:\n%s", out)
	}
	want := []string{"services.app.command", "services.cache.image", "services.db.healthcheck.test"}
	if got := problemPaths(t, err); !reflect.DeepEqual(got, want) {
		t.Errorf("problem paths = %v, want %v\n%v", got, want, err)
	}
}

func TestConfigRejectsBadTarget(t *testing.T) {
	p := load(t, "testdata/rails.compose.yml")
	for _, tg := range []Target{
		{BaseDomain: "", Arch: "amd64"},
		{BaseDomain: "SVNMNS.com", Arch: "amd64"},
		{BaseDomain: "svnmns.com/path", Arch: "amd64"},
		{BaseDomain: "*.svnmns.com", Arch: "amd64"},
		{BaseDomain: "svnmns.com", Arch: "386"},
		{BaseDomain: "svnmns.com", Arch: ""},
	} {
		out, err := Config(p, tg)
		if err == nil || out != nil {
			t.Errorf("%+v: out=%q err=%v, want an error and no output", tg, out, err)
		}
	}
}

func TestSecretsFile(t *testing.T) {
	got, err := SecretsFile(load(t, "testdata/phoenix.compose.yml"))
	if err != nil {
		t.Fatal(err)
	}
	want, _ := os.ReadFile("testdata/phoenix.secrets")
	if string(got) != string(want) {
		t.Errorf("secrets file:\n%s\nwant:\n%s", got, want)
	}
	if bytes.Contains(got, []byte("_HOST")) {
		t.Error("service hosts aren't secrets")
	}
}

func TestConfigIsDeterministic(t *testing.T) {
	p := load(t, "testdata/phoenix.compose.yml")
	first, err := Config(p, target)
	if err != nil {
		t.Fatal(err)
	}
	for range 20 {
		again, _ := Config(p, target)
		if !bytes.Equal(first, again) {
			t.Fatalf("output changed between runs:\n%s\n---\n%s", first, again)
		}
	}
}

// A value-less environment entry has no shell on the server to take it from.
func TestConfigRejectsWhatCantReachTheServer(t *testing.T) {
	p := parse(t, `name: shop
services:
  app:
    build: .
    ports: ["80:80"]
    environment: [FROM_SHELL]
x-houston:
  health: /up
`)
	out, err := Config(p, target)
	if out != nil {
		t.Errorf("got output with an error:\n%s", out)
	}
	want := []string{"services.app.environment.FROM_SHELL"}
	if got := problemPaths(t, err); !reflect.DeepEqual(got, want) {
		t.Errorf("problem paths = %v, want %v\n%v", got, want, err)
	}
}

// Kamal writes environment through docker env files, which are read
// literally, after escaping values with Ruby's String#dump: a backslash
// arrives doubled, and a tab or line break as \t or \n. Houston refuses them
// rather than deploy a different value.
func TestConfigRejectsValuesKamalCantCarry(t *testing.T) {
	p := parse(t, `name: shop
services:
  app:
    build: .
    ports: ["80:80"]
    environment:
      BACKSLASH: 'C:\\data'
      NEWLINE: "a\nb"
      TAB: "a\tb"
x-houston:
  health: /up
`)
	out, err := Config(p, target)
	if out != nil {
		t.Errorf("got output with an error:\n%s", out)
	}
	want := []string{"services.app.environment.BACKSLASH", "services.app.environment.NEWLINE", "services.app.environment.TAB"}
	if got := problemPaths(t, err); !reflect.DeepEqual(got, want) {
		t.Errorf("problem paths = %v, want %v\n%v", got, want, err)
	}
}

// CarrierValue is how houston deploy hands a secret to the Kamal container.
// Kamal's dotenv runs command substitution on the secrets file (printenv),
// then variable substitution on the result; a backslash before $ keeps the
// $ literal, and $( is never a variable.
func TestCarrierValue(t *testing.T) {
	for _, tc := range []struct{ value, carrier string }{
		{"plain", "plain"},
		{"$HOME", `\$HOME`},
		{"${HOME}", `\${HOME}`},
		{"$$HOME", `\$\$HOME`},
		{"$(touch x)", "$(touch x)"},
		{"cost $", `cost \$`},
		{"café ☕ 'q' \"d\" `b` <%= 1 %>", "café ☕ 'q' \"d\" `b` <%= 1 %>"},
		{"", ""},
	} {
		got, err := CarrierValue(tc.value)
		if err != nil || got != tc.carrier {
			t.Errorf("CarrierValue(%q) = %q, %v; want %q", tc.value, got, err, tc.carrier)
		}
	}
	for _, bad := range []string{`back\slash`, "line\nbreak", "tab\there", "cr\r", "nul\x00"} {
		if got, err := CarrierValue(bad); err == nil {
			t.Errorf("CarrierValue(%q) = %q, want an error", bad, got)
		}
	}
}

// The spike's HOSTILE carrier is written by hand in kamal-spike-inside.sh;
// this keeps it equal to what houston deploy would pass.
func TestCarrierValueSpikeFixture(t *testing.T) {
	hostile := "$(touch /workdir/pwned-dollar) `touch /workdir/pwned-backtick` $HOME ${HOME} $$HOME 'single' \"double\" <%= 1 + 1 %> café"
	want := "$(touch /workdir/pwned-dollar) `touch /workdir/pwned-backtick` \\$HOME \\${HOME} \\$\\$HOME 'single' \"double\" <%= 1 + 1 %> café"
	if got, err := CarrierValue(hostile); err != nil || got != want {
		t.Errorf("CarrierValue(spike HOSTILE) = %q, %v\nwant %q", got, err, want)
	}
}

// Houston's registry login owns KAMAL_REGISTRY_PASSWORD; composite secret
// names only ever contain letters, digits and _.
func TestConfigSecretNamesAreSafe(t *testing.T) {
	p := parse(t, `name: shop
services:
  app:
    build: .
    ports: ["80:80"]
    environment:
      KAMAL_REGISTRY_PASSWORD: ${KAMAL_REGISTRY_PASSWORD}
  web.v2:
    image: busybox
    environment:
      URL: x-${TOKEN}
x-houston:
  health: /up
`)
	_, err := Config(p, target)
	if got := problemPaths(t, err); !reflect.DeepEqual(got, []string{"services.app.environment.KAMAL_REGISTRY_PASSWORD"}) {
		t.Errorf("problem paths = %v\n%v", got, err)
	}

	p = parse(t, `name: shop
services:
  app:
    build: .
    ports: ["80:80"]
  web.v2:
    image: busybox
    environment:
      URL: x-${TOKEN}
x-houston:
  health: /up
`)
	if got := dig(config(t, p, target), "accessories", "web.v2", "env", "secret"); !reflect.DeepEqual(got, []any{"URL:WEB_V2__URL"}) {
		t.Errorf("secret = %v, want [URL:WEB_V2__URL]", got)
	}
}

func TestResolveSecrets(t *testing.T) {
	values := map[string]string{"POSTGRES_PASSWORD": "pw $x", "SECRET_KEY_BASE": "skb"}
	lookup := func(name string) (string, bool) { v, ok := values[name]; return v, ok }

	got, err := ResolveSecrets(load(t, "testdata/phoenix.compose.yml"), lookup)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"APP__DATABASE_URL":       "postgres://postgres:pw $x@phoenixapp-db/phoenixapp",
		"POSTGRES_PASSWORD":       "pw $x",
		"SECRET_KEY_BASE":         "skb",
		"KAMAL_REGISTRY_PASSWORD": "houston",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ResolveSecrets = %#v\nwant %#v", got, want)
	}

	p := parse(t, `name: shop
services:
  app:
    build: .
    ports: ["80:80"]
    environment:
      LEVEL: ${LEVEL:-info}
      OPTIONAL: ${OPTIONAL}
      REQUIRED: ${REQUIRED:?set REQUIRED in Mission Control}
x-houston:
  health: /up
`)
	none := func(string) (string, bool) { return "", false }
	if _, err := ResolveSecrets(p, none); err == nil || !strings.Contains(err.Error(), "REQUIRED") {
		t.Errorf("unset required variable: err = %v, want one naming REQUIRED", err)
	}
	got, err = ResolveSecrets(p, func(name string) (string, bool) { return "r", name == "REQUIRED" })
	if err != nil {
		t.Fatal(err)
	}
	if got["APP__LEVEL"] != "info" || got["OPTIONAL"] != "" || got["REQUIRED"] != "r" {
		t.Errorf("ResolveSecrets = %#v", got)
	}
}

func TestAppEnv(t *testing.T) {
	p := load(t, "testdata/phoenix.compose.yml")
	secrets, err := ResolveSecrets(p, func(name string) (string, bool) { return "v-" + name, true })
	if err != nil {
		t.Fatal(err)
	}
	got, err := AppEnv(p, secrets)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"DATABASE_URL":    "postgres://postgres:v-POSTGRES_PASSWORD@phoenixapp-db/phoenixapp",
		"PHX_HOST":        "phoenixapp.com",
		"REDIS_URL":       "redis://phoenixapp-cache:6379",
		"SECRET_KEY_BASE": "v-SECRET_KEY_BASE",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("AppEnv = %#v\nwant %#v", got, want)
	}
	if _, err := AppEnv(p, map[string]string{}); err == nil {
		t.Error("AppEnv without the secrets' values: want an error")
	}
	// The release hook mounts what the app mounts.
	if got := AppVolumes(p); !reflect.DeepEqual(got, []string{"phoenixapp_media:/media"}) {
		t.Errorf("AppVolumes = %v", got)
	}
}
