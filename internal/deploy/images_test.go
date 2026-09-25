package deploy

import (
	"os"
	"regexp"
	"testing"
)

// Third-party images are pinned by digest, so a moved tag can't change what
// runs (docs/plans/security-fixes.md, M3). The installer pulls Kamal's image
// before any deploy; it must be the one deploy runs.
var pinnedByDigest = regexp.MustCompile(`^[a-z0-9./-]+:[A-Za-z0-9._-]+@sha256:[0-9a-f]{64}$`)

func TestThirdPartyImagesArePinnedByDigest(t *testing.T) {
	if !pinnedByDigest.MatchString(KamalImage) {
		t.Errorf("KamalImage isn't pinned by digest: %s", KamalImage)
	}
	install, err := os.ReadFile("../../install/install.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"CLOUDFLARED_IMAGE", "KAMAL_IMAGE", "REGISTRY_IMAGE"} {
		m := regexp.MustCompile(`(?m)^` + name + `="([^"]+)"$`).FindSubmatch(install)
		if m == nil || !pinnedByDigest.Match(m[1]) {
			t.Errorf("install.sh's %s isn't pinned by digest: %q", name, m)
			continue
		}
		if name == "KAMAL_IMAGE" && string(m[1]) != KamalImage {
			t.Errorf("install.sh pulls %s, but deploy runs %s", m[1], KamalImage)
		}
	}
}
