package cli

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/sevenmoons/houston/internal/docker"
	"github.com/sevenmoons/houston/internal/project"
	"github.com/sevenmoons/houston/internal/variant"
)

// runTest implements `houston test`: commands.test in a throwaway copy of the
// project (its own name, volumes and random secrets), torn down every time.
// Laptops and server runners get the same environment: .env and the shell's
// values for the file's variables never reach it.
func runTest(file string, stdout, stderr io.Writer, d docker.Runner) int {
	abs, err := filepath.Abs(file)
	if err != nil {
		fmt.Fprintf(stderr, "houston: %v\n", err)
		return exitUsage
	}
	p, ok := loadProject(file, stderr)
	if !ok {
		return exitUsage
	}
	if p.Houston.Commands.Test == "" {
		fmt.Fprintln(stdout, "houston: no x-houston.commands.test; nothing to run")
		return 0
	}
	if !preflight(d, stderr) {
		return exitFailure
	}
	dir := filepath.Dir(abs)
	override := filepath.Join(dir, ".houston", "compose.test.yml")
	if err := writeGenerated(override, variant.TestOverride(p)); err != nil {
		fmt.Fprintf(stderr, "houston: can't write .houston/compose.test.yml: %v\n", err)
		return exitFailure
	}

	name := p.Name + "-test-" + randomHex(4)
	env := testEnv(os.Environ(), p.Variables)
	base := []string{"compose", "-p", name, "--project-directory", dir, "--env-file", "/dev/null", "-f", abs, "-f", override}

	code, runErr := d.Run(dir, env, slices.Concat(base, []string{"run", "--rm", "--build", p.AppService, "sh", "-c", p.Houston.Commands.Test})...)
	// Teardown always runs and never changes the result: leftovers are only
	// resources, and the next run uses a new project name anyway.
	if downCode, downErr := d.Run(dir, env, slices.Concat(base, []string{"down", "-v", "--rmi", "local", "--remove-orphans"})...); downErr != nil || downCode != 0 {
		fmt.Fprintf(stderr, "warning: couldn't remove the test project; clean up with: docker compose -p %s down -v\n", name)
	}
	if runErr != nil {
		fmt.Fprintf(stderr, "houston: %v\n", runErr)
		return exitFailure
	}
	return code
}

// dockersOwn are variables the docker CLI itself runs on. They describe this
// machine, not the app, so houston test never strips or randomizes them even
// when the compose file references one (e.g. a dev-only ${HOME}/.ssh mount).
var dockersOwn = map[string]bool{
	"HOME": true, "PATH": true, "TMPDIR": true, "DOCKER_HOST": true, "DOCKER_CONTEXT": true,
	"DOCKER_CONFIG": true, "DOCKER_CERT_PATH": true, "DOCKER_TLS_VERIFY": true,
}

// testEnv is Houston's environment minus every variable the compose file
// references, plus a random value for each required secret. Service hosts and
// optional variables stay unset so their defaults apply. Docker's own
// variables always pass through.
func testEnv(environ []string, vars []project.Variable) []string {
	referenced := map[string]bool{}
	for _, v := range vars {
		if !dockersOwn[v.Name] {
			referenced[v.Name] = true
		}
	}
	var env []string
	for _, kv := range environ {
		if name, _, _ := strings.Cut(kv, "="); !referenced[name] {
			env = append(env, kv)
		}
	}
	for _, v := range vars {
		if v.Required && v.Kind == project.Secret && !dockersOwn[v.Name] {
			env = append(env, v.Name+"="+randomHex(16))
		}
	}
	return env
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand doesn't fail on supported platforms
	}
	return hex.EncodeToString(b)
}
