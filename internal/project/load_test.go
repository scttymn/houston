package project

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// doc builds a compose file around a minimal valid project. Fields are appended
// verbatim, so callers indent them: app keys by 4 spaces, services by 2, x-houston
// keys by 2, top-level keys by 0.
type doc struct {
	name     string // "" → "demo"; "-" → omit the key
	app      string // extra keys under services.app
	appBase  string // "" → build + one port; replaces them when set
	services string // extra services
	top      string // extra top-level keys
	xh       string // "" → "  health: /up"; "-" → omit the block
}

func (d doc) String() string {
	var b strings.Builder
	switch d.name {
	case "":
		b.WriteString("name: demo\n")
	case "-":
	default:
		b.WriteString("name: " + d.name + "\n")
	}
	b.WriteString("services:\n  app:\n")
	if d.appBase == "" {
		b.WriteString("    build: .\n    ports: [\"3000:3000\"]\n")
	} else {
		b.WriteString(d.appBase)
	}
	b.WriteString(d.app)
	b.WriteString(d.services)
	b.WriteString(d.top)
	switch d.xh {
	case "":
		b.WriteString("x-houston:\n  health: /up\n")
	case "-":
	default:
		b.WriteString("x-houston:\n" + d.xh)
	}
	return b.String()
}

func writeCompose(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "compose.yml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func mustLoad(t *testing.T, path string) *Project {
	t.Helper()
	p, err := Load(path)
	if err != nil {
		t.Fatalf("Load(%s) failed:\n%v", path, err)
	}
	if p == nil {
		t.Fatal("Load returned nil Project and nil error")
	}
	return p
}

func loadProblems(t *testing.T, path string) []Problem {
	t.Helper()
	p, err := Load(path)
	if err == nil {
		t.Fatalf("Load succeeded, want problems")
	}
	if p != nil {
		t.Errorf("Load returned a Project alongside an error")
	}
	var errs *Errors
	if !errors.As(err, &errs) {
		t.Fatalf("error is %T, want *Errors: %v", err, err)
	}
	return errs.Problems
}

func assertProblem(t *testing.T, problems []Problem, path, msgPart string) {
	t.Helper()
	for _, p := range problems {
		if p.Path == path && strings.Contains(p.Msg, msgPart) {
			return
		}
	}
	t.Errorf("no problem at %q containing %q; got %+v", path, msgPart, problems)
}

func TestLoad_PhoenixScenario(t *testing.T) {
	p := mustLoad(t, "testdata/phoenixapp.compose.yml")

	if p.Name != "phoenixapp" || p.AppService != "app" || p.AppPort != 4000 {
		t.Errorf("name/app/port = %q/%q/%d", p.Name, p.AppService, p.AppPort)
	}
	want := Houston{
		Health:  "/health",
		Domains: []string{"phoenixapp.com", "www.phoenixapp.com"},
		Deploy:  Deploy{On: "commit", Branch: "main", Tags: "v*"},
		Commands: Commands{
			Console: &Console{Dev: "iex -S mix", Server: "bin/phoenixapp remote"},
			Test:    "mix test",
		},
		Hooks:   Hooks{Release: `bin/phoenixapp eval "PhoenixApp.Release.migrate"`},
		Backups: Backups{Schedule: "daily 03:00", KeepAuto: 14, KeepDeploy: 10},
	}
	if !reflect.DeepEqual(p.Houston, want) {
		t.Errorf("Houston =\n%+v\nwant\n%+v", p.Houston, want)
	}
	wantVars := []Variable{
		{Name: "CACHE_HOST", Required: false, Kind: ServiceHost},
		{Name: "DB_HOST", Required: false, Kind: ServiceHost},
		{Name: "POSTGRES_PASSWORD", Required: true, Kind: Secret},
		{Name: "SECRET_KEY_BASE", Required: true, Kind: Secret},
	}
	if !reflect.DeepEqual(p.Variables, wantVars) {
		t.Errorf("Variables =\n%+v\nwant\n%+v", p.Variables, wantVars)
	}
	var services []string
	for name := range p.Compose.Services {
		services = append(services, name)
	}
	sort.Strings(services)
	if !reflect.DeepEqual(services, []string{"app", "cache", "db"}) {
		t.Errorf("compose services = %v", services)
	}
}

func TestLoad_RailsScenarioDefaults(t *testing.T) {
	p := mustLoad(t, "testdata/railsapp.compose.yml")

	if p.Name != "railsapp" || p.AppPort != 3000 {
		t.Errorf("name/port = %q/%d", p.Name, p.AppPort)
	}
	if want := (Deploy{On: "commit", Branch: "main", Tags: "v*"}); p.Houston.Deploy != want {
		t.Errorf("Deploy = %+v, want %+v", p.Houston.Deploy, want)
	}
	if want := (Backups{Schedule: "daily 03:00", KeepAuto: 14, KeepDeploy: 10}); p.Houston.Backups != want {
		t.Errorf("Backups = %+v, want %+v", p.Houston.Backups, want)
	}
	if want := (&Console{Dev: "bin/rails console", Server: "bin/rails console"}); !reflect.DeepEqual(p.Houston.Commands.Console, want) {
		t.Errorf("Console = %+v, want %+v", p.Houston.Commands.Console, want)
	}
	if want := []Variable{{Name: "RAILS_MASTER_KEY", Required: true, Kind: Secret}}; !reflect.DeepEqual(p.Variables, want) {
		t.Errorf("Variables = %+v, want %+v", p.Variables, want)
	}
	if len(p.Houston.Domains) != 0 {
		t.Errorf("Domains = %v, want none", p.Houston.Domains)
	}
}

func TestLoad_IgnoresEnvironmentAndDotEnv(t *testing.T) {
	src, err := os.ReadFile("testdata/phoenixapp.compose.yml")
	if err != nil {
		t.Fatal(err)
	}
	path := writeCompose(t, string(src))
	before := mustLoad(t, path)

	t.Setenv("POSTGRES_PASSWORD", "leak-from-env")
	t.Setenv("DB_HOST", "leak-host")
	t.Setenv("COMPOSE_PROJECT_NAME", "other")
	dotenv := "SECRET_KEY_BASE=leak-from-dotenv\nDB_HOST=leak-host\nCOMPOSE_PROJECT_NAME=other\n"
	if err := os.WriteFile(filepath.Join(filepath.Dir(path), ".env"), []byte(dotenv), 0o644); err != nil {
		t.Fatal(err)
	}
	after := mustLoad(t, path)

	if after.Name != before.Name || !reflect.DeepEqual(after.Variables, before.Variables) || !reflect.DeepEqual(after.Houston, before.Houston) {
		t.Errorf("environment changed the result:\nbefore %+v\nafter  %+v", before, after)
	}
	url := after.Compose.Services["app"].Environment["DATABASE_URL"]
	if url == nil || *url != "postgres://postgres:${POSTGRES_PASSWORD}@${DB_HOST:-db}/phoenixapp" {
		t.Errorf("DATABASE_URL was interpolated: %v", url)
	}
	for _, leak := range []string{"leak-from-env", "leak-host", "leak-from-dotenv"} {
		for name, svc := range after.Compose.Services {
			for k, v := range svc.Environment {
				if v != nil && strings.Contains(*v, leak) {
					t.Errorf("services.%s.environment.%s contains %q", name, k, leak)
				}
			}
		}
		for k, v := range after.Compose.Environment {
			if strings.Contains(v, leak) {
				t.Errorf("project environment %s contains %q", k, leak)
			}
		}
	}
}

func TestLoad_FileProblems(t *testing.T) {
	valid := doc{}.String()
	cases := []struct {
		name    string
		content *string // nil → file doesn't exist
		msg     string
	}{
		{"missing", nil, "houston init"},
		{"empty", ptr(""), "empty"},
		{"comments only", ptr("# nothing yet\n"), "empty"},
		{"list root", ptr("- a\n- b\n"), "mapping"},
		{"scalar root", ptr("hello\n"), "mapping"},
		{"malformed", ptr("name: [\n"), "not valid YAML"},
		{"two documents", ptr(valid + "---\nname: other\n"), "one YAML document"},
		{"over 1 MiB", ptr(valid + "# " + strings.Repeat("x", 1<<20) + "\n"), "larger than 1 MiB"},
		{"compose schema error", ptr(doc{app: "    enviroment: { A: b }\n"}.String()), "enviroment"},
		{"no x-houston", ptr(doc{xh: "-"}.String()), "x-houston"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "compose.yml")
			if tc.content != nil {
				path = writeCompose(t, *tc.content)
			}
			problems := loadProblems(t, path)
			if len(problems) != 1 {
				t.Fatalf("want exactly 1 problem, got %+v", problems)
			}
			if !strings.Contains(problems[0].Msg, tc.msg) {
				t.Errorf("problem %q doesn't mention %q", problems[0].Msg, tc.msg)
			}
			if abs, _ := filepath.Abs(path); strings.Contains(problems[0].Msg, abs) {
				t.Errorf("problem repeats the file path: %q", problems[0].Msg)
			}
		})
	}
}

func TestLoad_NameRules(t *testing.T) {
	for _, name := range []string{"equip", "a", "my-app", strings.Repeat("a", 63)} {
		t.Run("valid "+name[:min(len(name), 12)], func(t *testing.T) {
			if p := mustLoad(t, writeCompose(t, doc{name: name}.String())); p.Name != name {
				t.Errorf("Name = %q", p.Name)
			}
		})
	}
	invalid := map[string]string{
		"missing":    "-",
		"uppercase":  "Equip",
		"underscore": "my_app",
		"leading -":  "-app",
		"trailing -": "app-",
		"64 chars":   strings.Repeat("a", 64),
		"admin":      "admin",
		"hooks":      "hooks",
	}
	for label, name := range invalid {
		t.Run("invalid "+label, func(t *testing.T) {
			assertProblem(t, loadProblems(t, writeCompose(t, doc{name: name}.String())), "name", "")
		})
	}
}

func TestLoad_AppServiceAndPort(t *testing.T) {
	t.Run("no built service", func(t *testing.T) {
		d := doc{appBase: "    image: nginx\n    ports: [\"80:80\"]\n"}
		assertProblem(t, loadProblems(t, writeCompose(t, d.String())), "services", "no service has `build:`")
	})
	t.Run("two built services", func(t *testing.T) {
		d := doc{services: "  worker:\n    build: .\n"}
		assertProblem(t, loadProblems(t, writeCompose(t, d.String())), "services", "only one built service")
	})
	t.Run("two ports without x-houston.app_port", func(t *testing.T) {
		d := doc{appBase: "    build: .\n    ports: [\"3000:3000\", \"3001:3001\"]\n"}
		assertProblem(t, loadProblems(t, writeCompose(t, d.String())), "services.app.ports", "x-houston.app_port")
	})
	t.Run("no ports without x-houston.app_port", func(t *testing.T) {
		d := doc{appBase: "    build: .\n"}
		assertProblem(t, loadProblems(t, writeCompose(t, d.String())), "services.app.ports", "x-houston.app_port")
	})
	t.Run("x-houston.app_port overrides ports", func(t *testing.T) {
		d := doc{appBase: "    build: .\n    ports: [\"3000:3000\", \"3001:3001\"]\n", xh: "  health: /up\n  app_port: 3001\n"}
		if p := mustLoad(t, writeCompose(t, d.String())); p.AppPort != 3001 {
			t.Errorf("AppPort = %d, want 3001", p.AppPort)
		}
	})
	t.Run("x-houston.app_port without ports", func(t *testing.T) {
		d := doc{appBase: "    build: .\n", xh: "  health: /up\n  app_port: 8080\n"}
		if p := mustLoad(t, writeCompose(t, d.String())); p.AppPort != 8080 {
			t.Errorf("AppPort = %d, want 8080", p.AppPort)
		}
	})
	t.Run("container side of the mapping", func(t *testing.T) {
		d := doc{appBase: "    build: .\n    ports: [\"8080:3000\"]\n"}
		if p := mustLoad(t, writeCompose(t, d.String())); p.AppPort != 3000 {
			t.Errorf("AppPort = %d, want 3000", p.AppPort)
		}
	})
	for _, port := range []string{"0", "70000", "\"3000\""} {
		t.Run("x-houston.app_port "+port, func(t *testing.T) {
			d := doc{xh: "  health: /up\n  app_port: " + port + "\n"}
			assertProblem(t, loadProblems(t, writeCompose(t, d.String())), "x-houston.app_port", "")
		})
	}
}

// x-houston.port was renamed app_port (docs/plans/init-generic.md): there's
// no alias, and the old key says what it's called now.
func TestAppPortRenamed(t *testing.T) {
	d := doc{appBase: "    build: .\n", xh: "  health: /up\n  port: 8080\n"}
	assertProblem(t, loadProblems(t, writeCompose(t, d.String())), "x-houston.port", "`port` is now `app_port`")
}

func TestLoad_UnsupportedCompose(t *testing.T) {
	db := "  db:\n    image: postgres:17\n"
	cases := []struct {
		name string
		d    doc
		path string
		msg  string
	}{
		{"network_mode", doc{app: "    network_mode: host\n"}, "services.app.network_mode", "can't run"},
		{"privileged", doc{app: "    privileged: true\n"}, "services.app.privileged", "can't run"},
		{"profiles", doc{services: db + "    profiles: [debug]\n"}, "services.db.profiles", "can't run"},
		{"extends", doc{services: db + "  db2:\n    extends: { service: db }\n"}, "services.db2.extends", "can't run"},
		{"deploy.replicas", doc{app: "    deploy: { replicas: 2 }\n"}, "services.app.deploy.replicas", "can't run"},
		{"env_file", doc{app: "    env_file: .env\n"}, "services.app.env_file", "NAME: ${NAME}"},
		{"top-level secrets", doc{top: "secrets:\n  s: { file: ./s.txt }\n"}, "secrets", "can't run"},
		{"top-level networks", doc{top: "networks:\n  back: {}\n"}, "networks", "can't run"},
		{"top-level configs", doc{top: "configs:\n  c: { file: ./c.txt }\n"}, "configs", "can't run"},
		{"include", doc{top: "include: [other.yml]\n"}, "include", "can't run"},
		{"other extension", doc{top: "x-other: {}\n"}, "x-other", "can't run"},
		{"bind mount on a service", doc{services: db + "    volumes: [\"./init.sql:/docker-entrypoint-initdb.d/init.sql\"]\n"}, "services.db.volumes", "disappear on the server"},
		{"anonymous volume", doc{app: "    volumes: [\"/data\"]\n"}, "services.app.volumes", "name"},
		{"volume driver_opts", doc{app: "    volumes: [\"media:/media\"]\n", top: "volumes:\n  media:\n    driver_opts: { type: nfs }\n"}, "volumes.media", "Mission Control"},
		{"undeclared volume", doc{app: "    volumes: [\"media:/media\"]\n"}, "", "media"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertProblem(t, loadProblems(t, writeCompose(t, tc.d.String())), tc.path, tc.msg)
		})
	}
	t.Run("allowed keys pass", func(t *testing.T) {
		d := doc{
			app: "    environment: { A: b }\n    command: [bin/start]\n    restart: unless-stopped\n" +
				"    healthcheck: { test: [CMD, \"true\"] }\n    depends_on: [db]\n" +
				"    deploy: { resources: { limits: { cpus: \"1\", memory: 1g } } }\n" +
				"    volumes: [\".:/app\", \"media:/media\"]\n",
			services: db + "    volumes: [\"pgdata:/var/lib/postgresql/data\"]\n",
			top:      "volumes:\n  media:\n  pgdata: {}\n",
		}
		mustLoad(t, writeCompose(t, d.String()))
	})
}

func TestLoad_XHoustonRules(t *testing.T) {
	invalid := []struct {
		name string
		xh   string
		path string
		msg  string
	}{
		{"not a mapping", "  - health\n", "x-houston", "mapping"},
		{"health missing", "  domains: []\n", "x-houston.health", "required"},
		{"health without slash", "  health: up\n", "x-houston.health", "/"},
		{"health not a string", "  health: 200\n", "x-houston.health", "must be a path"},
		{"release not a string", "  health: /up\n  hooks: { release: [a, b] }\n", "x-houston.hooks.release", "must be a command string"},
		{"health with space", "  health: /u p\n", "x-houston.health", "whitespace"},
		{"unknown key", "  health: /up\n  helth: /up\n", "x-houston.helth", "unknown"},
		{"wildcard domain", "  health: /up\n  domains: [\"*.x.com\"]\n", "x-houston.domains[0]", ""},
		{"domain with scheme", "  health: /up\n  domains: [\"https://x.com\"]\n", "x-houston.domains[0]", ""},
		{"single-label domain", "  health: /up\n  domains: [localhost]\n", "x-houston.domains[0]", ""},
		{"uppercase domain", "  health: /up\n  domains: [X.com]\n", "x-houston.domains[0]", ""},
		{"duplicate domain", "  health: /up\n  domains: [x.com, x.com]\n", "x-houston.domains[1]", "duplicate"},
		{"deploy.on", "  health: /up\n  deploy: { on: push }\n", "x-houston.deploy.on", "commit"},
		{"branch leading dash", "  health: /up\n  deploy: { branch: -x }\n", "x-houston.deploy.branch", ""},
		{"branch with ..", "  health: /up\n  deploy: { branch: a..b }\n", "x-houston.deploy.branch", ""},
		{"empty tags", "  health: /up\n  deploy: { tags: \"\" }\n", "x-houston.deploy.tags", ""},
		{"console dev only", "  health: /up\n  commands: { console: { dev: iex } }\n", "x-houston.commands.console", "server"},
		{"test as map", "  health: /up\n  commands: { test: { dev: x } }\n", "x-houston.commands.test", "string"},
		{"unknown command", "  health: /up\n  commands: { migrate: x }\n", "x-houston.commands.migrate", "hooks.release"},
		{"empty console", "  health: /up\n  commands: { console: \"\" }\n", "x-houston.commands.console", "empty"},
		{"unknown hook", "  health: /up\n  hooks: { pre_deploy: x }\n", "x-houston.hooks.pre_deploy", "unknown"},
		{"empty release", "  health: /up\n  hooks: { release: \"\" }\n", "x-houston.hooks.release", "empty"},
		{"hourly schedule", "  health: /up\n  backups: { schedule: hourly }\n", "x-houston.backups.schedule", "daily HH:MM"},
		{"schedule 24:00", "  health: /up\n  backups: { schedule: \"daily 24:00\" }\n", "x-houston.backups.schedule", "daily HH:MM"},
		{"keep.auto 0", "  health: /up\n  backups: { keep: { auto: 0 } }\n", "x-houston.backups.keep.auto", "1"},
		{"keep.deploy 1001", "  health: /up\n  backups: { keep: { deploy: 1001 } }\n", "x-houston.backups.keep.deploy", "1000"},
		{"backups.storage", "  health: /up\n  backups: { storage: unas-nfs }\n", "x-houston.backups.storage", "project page"},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			assertProblem(t, loadProblems(t, writeCompose(t, doc{xh: tc.xh}.String())), tc.path, tc.msg)
		})
	}

	t.Run("valid edges", func(t *testing.T) {
		xh := "  health: /up\n  domains: [a.io, www.a-b.co.uk]\n  deploy: { on: tag, tags: \"release-*\" }\n" +
			"  commands: { console: bin/rails console }\n  backups: { schedule: \"daily 23:59\", keep: { auto: 1, deploy: 1000 } }\n"
		p := mustLoad(t, writeCompose(t, doc{xh: xh}.String()))
		if want := (Deploy{On: "tag", Branch: "main", Tags: "release-*"}); p.Houston.Deploy != want {
			t.Errorf("Deploy = %+v, want %+v", p.Houston.Deploy, want)
		}
		if want := (Backups{Schedule: "daily 23:59", KeepAuto: 1, KeepDeploy: 1000}); p.Houston.Backups != want {
			t.Errorf("Backups = %+v, want %+v", p.Houston.Backups, want)
		}
	})
}

func TestLoad_ReportsAllProblemsSortedByPath(t *testing.T) {
	d := doc{name: "Bad_Name", app: "    privileged: true\n", xh: "  health: up\n  deploy: { on: push }\n"}
	path := writeCompose(t, d.String())
	_, err := Load(path)
	var errs *Errors
	if !errors.As(err, &errs) {
		t.Fatalf("want *Errors, got %v", err)
	}
	var paths []string
	for _, p := range errs.Problems {
		paths = append(paths, p.Path)
	}
	want := []string{"name", "services.app.privileged", "x-houston.deploy.on", "x-houston.health"}
	if !reflect.DeepEqual(paths, want) {
		t.Errorf("problem paths = %v, want %v", paths, want)
	}
	lines := strings.Split(strings.TrimRight(err.Error(), "\n"), "\n")
	if len(lines) != 4 {
		t.Fatalf("Error() has %d lines, want 4:\n%s", len(lines), err)
	}
	for i, line := range lines {
		if !strings.HasPrefix(line, path+": "+want[i]+": ") {
			t.Errorf("line %d = %q, want prefix %q", i, line, path+": "+want[i]+": ")
		}
	}
}

func TestLoad_VariableKinds(t *testing.T) {
	db := "  db:\n    image: postgres:17\n"
	valid := []struct {
		name     string
		env      string // lines under services.app.environment, indented 6
		services string
		xh       string
		want     []Variable
	}{
		{"braced", "      A: ${V}\n", "", "", []Variable{{"V", true, Secret, false}}},
		{"unbraced", "      A: $V\n", "", "", []Variable{{"V", true, Secret, false}}},
		{"required with message", "      A: ${V:?set it}\n", "", "", []Variable{{"V", true, Secret, false}}},
		{"required unset only", "      A: ${V?x}\n", "", "", []Variable{{"V", true, Secret, false}}},
		{"default", "      A: ${V:-d}\n", "", "", []Variable{{"V", false, Secret, false}}},
		{"empty default", "      A: ${V:-}\n", "", "", []Variable{{"V", false, Secret, true}}},
		{"required and empty default", "      A: ${V}\n      B: ${V:-}\n", "", "", []Variable{{"V", true, Secret, false}}},
		{"default if unset", "      A: ${V-d}\n", "", "", []Variable{{"V", false, Secret, false}}},
		{"presence value", "      A: ${V:+x}\n", "", "", []Variable{{"V", false, Secret, false}}},
		{"escaped", "      A: $$V\n", "", "", nil},
		{"nested default", "      A: ${A:-${B}}\n", "", "", []Variable{{"A", false, Secret, false}, {"B", true, Secret, false}}},
		{"message isn't scanned", "      A: ${V:?set $OTHER}\n", "", "", []Variable{{"V", true, Secret, false}}},
		{"required wins over optional", "      A: ${V}\n      B: ${V:-d}\n", "", "", []Variable{{"V", true, Secret, false}}},
		{"x-houston ignored", "", "", "  health: /up\n  hooks: { release: echo $$HOME }\n", nil},
		{"service host", "      U: ${DB_HOST:-db}\n", db, "", []Variable{{"DB_HOST", false, ServiceHost, false}}},
		{"host var without service", "      U: ${DB_HOST:-db}\n", "", "", []Variable{{"DB_HOST", false, Secret, false}}},
		{"dashed service", "      U: ${MY_DB_HOST:-my-db}\n", "  my-db:\n    image: postgres:17\n", "", []Variable{{"MY_DB_HOST", false, ServiceHost, false}}},
	}
	for _, tc := range valid {
		t.Run(tc.name, func(t *testing.T) {
			d := doc{services: tc.services, xh: tc.xh}
			if tc.env != "" {
				d.app = "    environment:\n" + tc.env
			}
			p := mustLoad(t, writeCompose(t, d.String()))
			if !reflect.DeepEqual(p.Variables, tc.want) {
				t.Errorf("Variables = %+v, want %+v", p.Variables, tc.want)
			}
		})
	}

	invalid := []struct {
		name     string
		env      string
		services string
		path     string
		msg      string
	}{
		{"service host without default", "      U: ${DB_HOST}\n", db, "services.app.environment.U", "${DB_HOST:-db}"},
		{"service host wrong default", "      U: ${DB_HOST:-database}\n", db, "services.app.environment.U", "${DB_HOST:-db}"},
		{"colliding host names", "", db + "  my-db:\n    image: redis:7\n  my_db:\n    image: redis:7\n", "services", "MY_DB_HOST"},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			d := doc{services: tc.services}
			if tc.env != "" {
				d.app = "    environment:\n" + tc.env
			}
			assertProblem(t, loadProblems(t, writeCompose(t, d.String())), tc.path, tc.msg)
		})
	}
}

func ptr(s string) *string { return &s }

func TestLoad_XHoustonDollarEscaping(t *testing.T) {
	t.Run("$$ becomes $, $( stays", func(t *testing.T) {
		xh := "  health: /up\n" +
			"  commands:\n" +
			"    test: 'test \"$$(cat /stage)\" = test && wget -qO- $$DB_URL && echo $(date)'\n" +
			"    console: { dev: 'iex --sname $$NODE', server: bin/app remote }\n" +
			"  hooks: { release: 'echo $${HOME}', post_deploy: bin/notify }\n"
		p := mustLoad(t, writeCompose(t, doc{xh: xh}.String()))
		if want := `test "$(cat /stage)" = test && wget -qO- $DB_URL && echo $(date)`; p.Houston.Commands.Test != want {
			t.Errorf("Test = %q, want %q", p.Houston.Commands.Test, want)
		}
		if want := "iex --sname $NODE"; p.Houston.Commands.Console.Dev != want {
			t.Errorf("Console.Dev = %q, want %q", p.Houston.Commands.Console.Dev, want)
		}
		if want := "echo ${HOME}"; p.Houston.Hooks.Release != want {
			t.Errorf("Release = %q, want %q", p.Houston.Hooks.Release, want)
		}
		if p.Variables != nil {
			t.Errorf("x-houston commands produced variables: %+v", p.Variables)
		}
	})
	invalid := []struct {
		name string
		xh   string
		path string
		msg  string
	}{
		{"bare $NAME in test", "  health: /up\n  commands: { test: 'wget -qO- $DB_URL' }\n", "x-houston.commands.test", "$$DB_URL"},
		{"braced in release", "  health: /up\n  hooks: { release: 'echo ${HOME}' }\n", "x-houston.hooks.release", "$$HOME"},
		{"bare in console dev", "  health: /up\n  commands: { console: { dev: 'x $A', server: y } }\n", "x-houston.commands.console.dev", "$$A"},
		{"bare in console string", "  health: /up\n  commands: { console: 'x $A' }\n", "x-houston.commands.console", "$$A"},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			assertProblem(t, loadProblems(t, writeCompose(t, doc{xh: tc.xh}.String())), tc.path, tc.msg)
		})
	}
}

func TestLoad_VariableLedBindMounts(t *testing.T) {
	d := doc{
		app: "    volumes:\n" +
			"      - ${HOME}/.ssh:/root/.ssh:ro\n" +
			"      - ${PWD}:/app\n" +
			"      - ${DATA_DIR:-./data}:/data\n" +
			"      - media:/media\n",
		top: "volumes:\n  media:\n",
	}
	p := mustLoad(t, writeCompose(t, d.String()))

	type mount struct {
		Type, Source, Target string
		ReadOnly             bool
	}
	var got []mount
	for _, v := range p.Compose.Services["app"].Volumes {
		got = append(got, mount{v.Type, v.Source, v.Target, v.ReadOnly})
	}
	want := []mount{
		{"bind", "${HOME}/.ssh", "/root/.ssh", true},
		{"bind", "${PWD}", "/app", false},
		{"bind", "${DATA_DIR:-./data}", "/data", false},
		{"volume", "media", "/media", false},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("volumes =\n%+v\nwant\n%+v", got, want)
	}
	wantVars := []Variable{{"DATA_DIR", false, Secret, false}, {"HOME", true, Secret, false}, {"PWD", true, Secret, false}}
	if !reflect.DeepEqual(p.Variables, wantVars) {
		t.Errorf("Variables = %+v, want %+v", p.Variables, wantVars)
	}

	t.Run("still a bind mount on a service", func(t *testing.T) {
		d := doc{services: "  db:\n    image: postgres:17\n    volumes: [\"${HOME}/init.sql:/docker-entrypoint-initdb.d/init.sql\"]\n"}
		assertProblem(t, loadProblems(t, writeCompose(t, d.String())), "services.db.volumes", "disappear on the server")
	})
}

// Every compose file in the repo outside testdata loads (review R6): a stale
// key, like x-houston.port after its rename, can't hide in one.
func TestRepoComposeFilesLoad(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	err = filepath.WalkDir(root, func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.IsDir() && (e.Name() == "testdata" || e.Name() == "node_modules" || strings.HasPrefix(e.Name(), ".")) && path != root {
			return filepath.SkipDir
		}
		if e.Name() != "compose.yml" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil || !strings.Contains(string(data), "x-houston:") {
			return err
		}
		found++
		if _, err := Load(path); err != nil {
			t.Errorf("%s doesn't load:\n%v", path, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if found == 0 {
		t.Errorf("found no compose.yml with x-houston; the walk is wrong")
	}
}

// A build stays inside the repo (security fixes, batch 1): only a relative
// context and dockerfile, a target and args with values; nothing that reaches
// the runner's files, network or SSH agent, and no build arg left for Compose
// to fill in from the runner's environment.
func TestBuildStaysInTheRepo(t *testing.T) {
	compose := func(build string) string {
		return "name: shop\nservices:\n  app:\n    build: " + build + "\n    ports: [\"127.0.0.1:3000:3000\"]\nx-houston:\n  health: /up\n"
	}
	for _, build := range []string{`.`, `{ context: ., target: production }`, `{ context: app, dockerfile: docker/Dockerfile.prod, args: { RUBY: "3.4" } }`, `{ context: ., args: [ "NODE_ENV=production" ] }`} {
		mustLoad(t, writeCompose(t, compose(build)))
	}
	for _, tc := range []struct{ build, path, want string }{
		{`/home/houston/.ssh`, "services.app.build.context", "inside the repo"},
		{`{ context: /home/houston/.ssh }`, "services.app.build.context", "inside the repo"},
		{`{ context: ../other }`, "services.app.build.context", "inside the repo"},
		{`{ context: ., dockerfile: ../../Dockerfile }`, "services.app.build.dockerfile", "inside the repo"},
		{`{ context: ., dockerfile: /etc/Dockerfile }`, "services.app.build.dockerfile", "inside the repo"},
		{`{ context: ., additional_contexts: { k: /home/houston/.ssh } }`, "services.app.build.additional_contexts", "can't"},
		{`{ context: ., ssh: [ default ] }`, "services.app.build.ssh", "can't"},
		{`{ context: ., network: host }`, "services.app.build.network", "can't"},
		{`{ context: ., dockerfile_inline: "FROM scratch" }`, "services.app.build.dockerfile_inline", "can't"},
		{`{ context: ., args: [ HOUSTON_TOKEN ] }`, "services.app.build.args", "a value"},
		{`{ context: ., args: { HOUSTON_TOKEN: } }`, "services.app.build.args", "a value"},
	} {
		problems := loadProblems(t, writeCompose(t, compose(tc.build)))
		found := false
		for _, p := range problems {
			if p.Path == tc.path && strings.Contains(p.Msg, tc.want) {
				found = true
			}
		}
		if !found {
			t.Errorf("build: %s — want a problem at %s mentioning %q, got %+v", tc.build, tc.path, tc.want, problems)
		}
	}
}

// The context and Dockerfile resolve (symlinks too) inside the checkout, or
// nothing is built: a repo's symlink can't point the build at the runner.
func TestBuildPathsStayInsideTheCheckout(t *testing.T) {
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "id_ed25519"), []byte("secret"), 0o600)
	os.WriteFile(filepath.Join(outside, "Dockerfile"), []byte("FROM scratch\n"), 0o644)

	repo := func(build string, links map[string]string) (*Project, string) {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\n"), 0o644)
		for name, target := range links {
			if err := os.Symlink(target, filepath.Join(dir, name)); err != nil {
				t.Fatal(err)
			}
		}
		path := filepath.Join(dir, "compose.yml")
		os.WriteFile(path, []byte("name: shop\nservices:\n  app:\n    build: "+build+"\n    ports: [\"127.0.0.1:3000:3000\"]\nx-houston:\n  health: /up\n"), 0o644)
		return mustLoad(t, path), dir
	}

	p, dir := repo(`.`, nil)
	context, dockerfile, err := p.BuildPaths(dir)
	real, _ := filepath.EvalSymlinks(dir)
	if err != nil || context != real || dockerfile != filepath.Join(real, "Dockerfile") {
		t.Errorf("a plain build: %q %q %v", context, dockerfile, err)
	}
	for name, tc := range map[string]struct {
		build string
		links map[string]string
	}{
		"a context symlinked out":    {`{ context: ctx }`, map[string]string{"ctx": outside}},
		"a Dockerfile symlinked out": {`{ context: ., dockerfile: Dockerfile.link }`, map[string]string{"Dockerfile.link": filepath.Join(outside, "Dockerfile")}},
		"a missing context":          {`{ context: nope }`, nil},
		"a dangling Dockerfile link": {`{ context: ., dockerfile: Dockerfile.link }`, map[string]string{"Dockerfile.link": filepath.Join(outside, "nope")}},
	} {
		p, dir := repo(tc.build, tc.links)
		if _, _, err := p.BuildPaths(dir); err == nil {
			t.Errorf("%s: built anyway", name)
		}
	}
}
