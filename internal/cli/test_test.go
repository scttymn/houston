package cli

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

var phoenixWithTest = strings.Replace(phoenix, "  health: /health\n", "  health: /health\n  commands: { test: mix test }\n", 1)

var throwawayName = regexp.MustCompile(`^phoenixapp-test-[0-9a-f]{8}$`)

func testArgs(project, dir, path string, tail ...string) []string {
	base := []string{"compose", "-p", project, "--project-directory", dir, "--env-file", "/dev/null",
		"-f", path, "-f", filepath.Join(dir, ".houston", "compose.test.yml")}
	return append(base, tail...)
}

// byCommand makes the fake return different results for `run` and `down`.
func byCommand(run, down func() (int, error)) func([]string) (int, error) {
	return func(args []string) (int, error) {
		for _, a := range args {
			switch a {
			case "run":
				return run()
			case "down":
				return down()
			}
		}
		return 0, nil
	}
}

func ok(code int) func() (int, error) { return func() (int, error) { return code, nil } }

func envMap(env []string) map[string]string {
	m := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		m[k] = v
	}
	return m
}

func TestTest_RunsInThrowawayProject(t *testing.T) {
	dir, path := newProject(t, phoenixWithTest, nil)
	d := &fakeDocker{runResult: byCommand(ok(3), ok(0))}

	code, _, stderr := run(d, "-f", path, "test")

	if code != 3 {
		t.Errorf("exit = %d, want the test's 3 (stderr: %s)", code, stderr)
	}
	if len(d.runs) != 2 {
		t.Fatalf("want run then down, got %q", d.runs)
	}
	name := d.runs[0][2]
	if !throwawayName.MatchString(name) {
		t.Fatalf("project name %q doesn't match %s", name, throwawayName)
	}
	if want := testArgs(name, dir, path, "run", "--rm", "--build", "app", "sh", "-c", "mix test"); !reflect.DeepEqual(d.runs[0], want) {
		t.Errorf("run =\n%q\nwant\n%q", d.runs[0], want)
	}
	if want := testArgs(name, dir, path, "down", "-v", "--rmi", "local", "--remove-orphans"); !reflect.DeepEqual(d.runs[1], want) {
		t.Errorf("down =\n%q\nwant\n%q", d.runs[1], want)
	}
	if d.runDirs[0] != dir || d.runDirs[1] != dir {
		t.Errorf("run dirs = %q, want %q", d.runDirs, dir)
	}

	again := &fakeDocker{}
	run(again, "-f", path, "test")
	if again.runs[0][2] == name {
		t.Errorf("two runs used the same project name %q", name)
	}
}

func TestTest_EnvironmentIsThrowaway(t *testing.T) {
	t.Setenv("SECRET_KEY_BASE", "from-shell")
	t.Setenv("DB_HOST", "elsewhere")
	t.Setenv("LOG_LEVEL", "debug")
	_, path := newProject(t, phoenixWithTest, map[string]string{".env": "POSTGRES_PASSWORD=from-dotenv\n"})

	d := &fakeDocker{}
	code, _, stderr := run(d, "-f", path, "test")
	if code != 0 || len(d.runEnvs) != 2 {
		t.Fatalf("exit = %d, runs = %d (stderr: %s)", code, len(d.runEnvs), stderr)
	}
	if stderr != "" {
		t.Errorf("unexpected stderr (test mode shouldn't read .env): %s", stderr)
	}
	env := envMap(d.runEnvs[0])
	for _, name := range []string{"POSTGRES_PASSWORD", "SECRET_KEY_BASE"} {
		switch v := env[name]; v {
		case "", "from-shell", "from-dotenv":
			t.Errorf("%s = %q, want a random throwaway value", name, v)
		}
	}
	for _, name := range []string{"DB_HOST", "LOG_LEVEL"} {
		if v, set := env[name]; set {
			t.Errorf("%s = %q is passed; service hosts and optional variables must fall back to their defaults", name, v)
		}
	}
	if env["PATH"] != os.Getenv("PATH") {
		t.Errorf("PATH wasn't passed through")
	}
	if !reflect.DeepEqual(d.runEnvs[0], d.runEnvs[1]) {
		t.Errorf("down got a different environment than run")
	}

	second := &fakeDocker{}
	run(second, "-f", path, "test")
	if envMap(second.runEnvs[0])["SECRET_KEY_BASE"] == env["SECRET_KEY_BASE"] {
		t.Errorf("two runs got the same secret value")
	}
}

func TestTest_NoTestCommand(t *testing.T) {
	dir, path := newProject(t, phoenix, nil)
	d := &fakeDocker{}

	code, stdout, stderr := run(d, "-f", path, "test")

	if code != 0 {
		t.Errorf("exit = %d, want 0", code)
	}
	if !strings.Contains(stdout+stderr, "nothing to run") {
		t.Errorf("output doesn't say there's nothing to run: %q %q", stdout, stderr)
	}
	if d.calls() != 0 {
		t.Errorf("docker was called: %q %q", d.outputs, d.runs)
	}
	if _, err := os.Stat(filepath.Join(dir, ".houston")); !os.IsNotExist(err) {
		t.Errorf(".houston was created")
	}
}

func TestTest_InvalidConfigAndPreflight(t *testing.T) {
	t.Run("invalid compose file", func(t *testing.T) {
		_, path := newProject(t, strings.Replace(phoenixWithTest, "health: /health", "health: health", 1), nil)
		d := &fakeDocker{}
		code, _, stderr := run(d, "-f", path, "test")
		if code != 2 || d.calls() != 0 || !strings.Contains(stderr, "x-houston.health") {
			t.Errorf("exit = %d, docker calls = %d, stderr = %s", code, d.calls(), stderr)
		}
	})
	boom := errors.New("boom")
	for name, d := range map[string]*fakeDocker{
		"not installed": {lookErr: boom},
		"not running":   {versionErr: boom},
		"no compose v2": {composeErr: boom},
	} {
		t.Run(name, func(t *testing.T) {
			_, path := newProject(t, phoenixWithTest, nil)
			code, _, _ := run(d, "-f", path, "test")
			if code != 1 || len(d.runs) != 0 {
				t.Errorf("exit = %d, runs = %q; want 1 and none", code, d.runs)
			}
		})
	}
}

func TestTest_TeardownAlwaysRuns(t *testing.T) {
	t.Run("docker can't start the run", func(t *testing.T) {
		_, path := newProject(t, phoenixWithTest, nil)
		d := &fakeDocker{runResult: byCommand(
			func() (int, error) { return 1, errors.New("exec: docker: boom") },
			ok(0),
		)}
		code, _, stderr := run(d, "-f", path, "test")
		if code != 1 {
			t.Errorf("exit = %d, want 1 (stderr: %s)", code, stderr)
		}
		if len(d.runs) != 2 || !contains(d.runs[1], "down") {
			t.Errorf("teardown didn't run: %q", d.runs)
		}
	})
	t.Run("teardown fails", func(t *testing.T) {
		_, path := newProject(t, phoenixWithTest, nil)
		d := &fakeDocker{runResult: byCommand(ok(5), ok(1))}
		code, _, stderr := run(d, "-f", path, "test")
		if code != 5 {
			t.Errorf("exit = %d, want the test's 5", code)
		}
		name := d.runs[0][2]
		if want := "docker compose -p " + name + " down -v"; !strings.Contains(stderr, want) {
			t.Errorf("stderr doesn't give the cleanup command %q: %s", want, stderr)
		}
	})
}

func TestTest_GeneratedFile(t *testing.T) {
	t.Run("written next to .gitignore", func(t *testing.T) {
		dir, path := newProject(t, phoenixWithTest, nil)
		var atRun string
		d := &fakeDocker{onRun: func() {
			if atRun == "" {
				b, _ := os.ReadFile(filepath.Join(dir, ".houston", "compose.test.yml"))
				atRun = string(b)
			}
		}}
		run(d, "-f", path, "test")
		if !strings.Contains(atRun, "target: test") {
			t.Errorf("override when the run started =\n%s", atRun)
		}
		if ignore, _ := os.ReadFile(filepath.Join(dir, ".houston", ".gitignore")); string(ignore) != "*\n" {
			t.Errorf(".houston/.gitignore = %q", ignore)
		}
	})
	t.Run(".houston is a file", func(t *testing.T) {
		_, path := newProject(t, phoenixWithTest, map[string]string{".houston": "not a dir\n"})
		d := &fakeDocker{}
		code, _, stderr := run(d, "-f", path, "test")
		if code != 1 || len(d.runs) != 0 {
			t.Errorf("exit = %d, runs = %q; want 1 and none", code, d.runs)
		}
		if !strings.Contains(stderr, "can't write .houston/compose.test.yml") {
			t.Errorf("stderr = %s", stderr)
		}
	})
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func TestTest_KeepsDockersOwnEnvironment(t *testing.T) {
	// A dev-only bind mount like ${HOME}/.ssh must not make houston test hand
	// docker a random HOME (it would lose ~/.docker and its context).
	withHome := strings.Replace(phoenixWithTest, "    depends_on: [db]\n", "    volumes: [\"${HOME}/.ssh:/root/.ssh:ro\"]\n    depends_on: [db]\n", 1)
	withHome = strings.Replace(withHome, "      LOG_LEVEL: ${LOG_LEVEL:-info}\n", "      LOG_LEVEL: ${LOG_LEVEL:-info}\n      DOCKER_CTX: ${DOCKER_CONTEXT}\n", 1)
	t.Setenv("HOME", "/home/tester")
	t.Setenv("DOCKER_CONTEXT", "orbstack")
	_, path := newProject(t, withHome, nil)
	d := &fakeDocker{}

	code, _, stderr := run(d, "-f", path, "test")

	if code != 0 {
		t.Fatalf("exit = %d (stderr: %s)", code, stderr)
	}
	env := envMap(d.runEnvs[0])
	if env["HOME"] != "/home/tester" || env["DOCKER_CONTEXT"] != "orbstack" {
		t.Errorf("HOME = %q, DOCKER_CONTEXT = %q; docker's own variables must pass through untouched", env["HOME"], env["DOCKER_CONTEXT"])
	}
	if env["SECRET_KEY_BASE"] == "" {
		t.Errorf("app secrets must still get throwaway values")
	}
}

// houston test runs the repo's build with only what Docker needs from this
// environment: a runner's HOUSTON_TOKEN (or anything else) never reaches a
// build arg or a service (security fixes, batch 1).
func TestTestEnvKeepsOnlyWhatDockerNeeds(t *testing.T) {
	env := envMap(testEnv([]string{"PATH=/usr/bin", "HOME=/home/houston", "DOCKER_HOST=unix:///var/run/docker.sock",
		"DOCKER_CONFIG=/var/lib/houston/runners/houston-runner-1/.docker", "HOUSTON_TOKEN=runner-secret", "HOUSTON_URL=http://mission-control:80",
		"AWS_SECRET_ACCESS_KEY=x", "SECRET_KEY_BASE=from-the-runner"}, nil))
	for _, name := range []string{"PATH", "HOME", "DOCKER_HOST", "DOCKER_CONFIG"} {
		if _, ok := env[name]; !ok {
			t.Errorf("%s dropped; Docker needs it", name)
		}
	}
	for _, name := range []string{"HOUSTON_TOKEN", "HOUSTON_URL", "AWS_SECRET_ACCESS_KEY", "SECRET_KEY_BASE"} {
		if _, ok := env[name]; ok {
			t.Errorf("%s reached the test build", name)
		}
	}
}
