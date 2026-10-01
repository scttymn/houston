package assets

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Every name a script imports from a vendored package is one the package
// exports: a browser refuses a module that imports a missing name, and a
// controller that doesn't load is never registered, silently.
func TestVendorImports(t *testing.T) {
	importFrom := regexp.MustCompile(`import\s*\{([^}]*)\}\s*from\s*"([^"]+)"`)
	exportList := regexp.MustCompile(`export\s*\{([^}]*)\}`)
	scripts, _ := filepath.Glob("js/controllers/*.js")
	scripts = append(scripts, "js/application.js")
	for _, script := range scripts {
		src, err := os.ReadFile(script)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range importFrom.FindAllStringSubmatch(string(src), -1) {
			vendor := filepath.Join("js", "vendor", strings.ReplaceAll(m[2], "/", "--")+".js")
			pkg, err := os.ReadFile(vendor)
			if err != nil {
				continue // not a vendored package
			}
			exported := map[string]bool{}
			for _, list := range exportList.FindAllStringSubmatch(string(pkg), -1) {
				for _, e := range strings.Split(list[1], ",") {
					f := strings.Fields(e)
					if len(f) > 0 {
						exported[f[len(f)-1]] = true
					}
				}
			}
			for _, name := range strings.Split(m[1], ",") {
				if f := strings.Fields(name); len(f) > 0 && !exported[f[0]] {
					t.Errorf("%s imports %s from %q, which doesn't export it", script, f[0], m[2])
				}
			}
		}
	}
}
