package deploy

import (
	"strings"
	"testing"
)

// kamal deploy pulls the image first, before its deploy lock or any
// container. When Docker's containerd store fails to unpack a layer (as it
// did when three deploys pulled the same layers at once), the deploy tries
// once more.

// kamalPullFailed is kamal deploy's output when its docker pull failed,
// as equip's deploy #7 logged it.
const kamalPullFailed = "Pull app image...\n" +
	"  INFO [58e0f178] Running docker login 127.0.0.1:5000 -u [REDACTED] -p [REDACTED] on 127.0.0.1\n" +
	"  INFO [58e0f178] Finished in 0.145 seconds with exit status 0 (successful).\n" +
	"  INFO [5d3dd15e] Running docker image rm --force 127.0.0.1:5000/shop:" + sha + " on 127.0.0.1\n" +
	"  INFO [5d3dd15e] Finished in 0.331 seconds with exit status 0 (successful).\n" +
	"  INFO [994388f1] Running docker pull 127.0.0.1:5000/shop:" + sha + " on 127.0.0.1\n" +
	"  Finished all in 2.0 seconds\n" +
	"  \x1b[31mERROR (SSHKit::Command::Failed): Exception while executing on host 127.0.0.1: docker exit status: 1\n" +
	"docker stdout: " + sha + ": Pulling from shop\n" +
	"9dd751957cb9: Pulling fs layer\n" +
	"docker stderr: failed to extract layer (application/vnd.oci.image.layer.v1.tar+gzip sha256:0adf) to overlayfs as \"extract-138465018-fjR8\": " +
	"failed call to UtimesNanoAt for /var/lib/containerd/io.containerd.snapshotter.v1.overlayfs/snapshots/1588/fs/usr/local/bundle/ruby/3.4.0/build_info: no such file or directory\n" +
	"\x1b[0m\n"

// kamalValidateFailed: the pull worked, and the image's check didn't.
const kamalValidateFailed = "Pull app image...\n" +
	"  INFO [994388f1] Running docker pull 127.0.0.1:5000/shop:" + sha + " on 127.0.0.1\n" +
	"  INFO [994388f1] Finished in 3.1 seconds with exit status 0 (successful).\n" +
	"  INFO [aa11bb22] Running docker inspect -f '{{ .Config.Labels.service }}' 127.0.0.1:5000/shop:" + sha + " on 127.0.0.1\n" +
	"  \x1b[31mERROR (SSHKit::Command::Failed): Exception while executing on host 127.0.0.1: docker exit status: 1\n"

// kamalBootFailed: past the pull, Kamal had started changing things.
const kamalBootFailed = "Pull app image...\n" +
	"  INFO [994388f1] Running docker pull 127.0.0.1:5000/shop:" + sha + " on 127.0.0.1\n" +
	"  INFO [994388f1] Finished in 3.1 seconds with exit status 0 (successful).\n" +
	"Ensure kamal-proxy is running...\n" +
	"  INFO [cc33dd44] Running docker pull basecamp/kamal-proxy:v0.9.2 on 127.0.0.1\n" +
	"  \x1b[31mERROR (SSHKit::Command::Failed): Exception while executing on host 127.0.0.1: docker exit status: 1\n"

// kamalDeploys makes kamal deploy write each of outputs in turn, exiting 1
// for each ("" exits 0).
func kamalDeploys(h *harness, outputs ...string) {
	n := 0
	h.docker.emit = func(what string) string {
		if !strings.HasPrefix(what, "kamal deploy") || n >= len(outputs) {
			return ""
		}
		return outputs[n]
	}
	h.docker.exit = func(what string) int {
		if !strings.HasPrefix(what, "kamal deploy") {
			return 0
		}
		n++
		if n <= len(outputs) && outputs[n-1] != "" {
			return 1
		}
		return 0
	}
}

func kamalDeployCount(h *harness) int {
	n := 0
	for _, what := range h.docker.whats() {
		if strings.HasPrefix(what, "kamal deploy") {
			n++
		}
	}
	return n
}

func TestDeployRetriesAFailedPullOnce(t *testing.T) {
	h := newHarness(t, shopCompose)
	kamalDeploys(h, kamalPullFailed, "")
	if code := h.run(); code != 0 || kamalDeployCount(h) != 2 || h.mission.final().Status != "go" {
		t.Fatalf("exit %d, %d kamal deploys, %+v", code, kamalDeployCount(h), h.mission.final())
	}
	if !strings.Contains(h.mission.log(), "The image pull failed, before Kamal changed anything; pulling once more.") {
		t.Errorf("the retry isn't said:\n%s", h.mission.log())
	}

	h = newHarness(t, shopCompose)
	kamalDeploys(h, kamalPullFailed, kamalPullFailed, "")
	if code := h.run(); code != 1 || kamalDeployCount(h) != 2 || !strings.Contains(h.mission.final().Error, "old version keeps serving") {
		t.Errorf("twice: exit %d, %d kamal deploys, %+v", code, kamalDeployCount(h), h.mission.final())
	}
}

func TestDeployDoesntRetryOtherFailures(t *testing.T) {
	for name, output := range map[string]string{
		"the image's check":                       kamalValidateFailed,
		"after the pull":                          kamalBootFailed,
		"no pull at all":                          "output\n",
		"a docker pull outside Kamal's pull step": strings.Replace(kamalPullFailed, "Pull app image...\n", "", 1),
		"a docker pull before it": "  INFO [0a0b0c0d] Running docker pull 127.0.0.1:5000/shop:" + sha + " on 127.0.0.1\nPull app image...\n" +
			"  \x1b[31mERROR (SSHKit::Command::Failed): Exception while executing on host 127.0.0.1: docker exit status: 1\n",
		"the proxy's pull, later": kamalBootFailed + "Pull app image...\n",
	} {
		h := newHarness(t, shopCompose)
		kamalDeploys(h, output, "")
		if code := h.run(); code != 1 || kamalDeployCount(h) != 1 {
			t.Errorf("%s: exit %d, %d kamal deploys", name, code, kamalDeployCount(h))
		}
	}
}

func TestRestoreRetriesAFailedPullOnce(t *testing.T) {
	h := restoreHarness(t)
	h.switchesProxy()
	kamalDeploys(h, kamalPullFailed, "")
	if code := h.run(); code != 0 || kamalDeployCount(h) != 2 || !h.finishedWith("go", "") {
		t.Errorf("exit %d, %d kamal deploys\n%s", code, kamalDeployCount(h), h.mission.log())
	}
}

// Stream output arrives in pieces that split lines anywhere.
func TestPullWatchReadsSplitLines(t *testing.T) {
	var out strings.Builder
	w := &pullWatch{w: &out}
	for i := 0; i < len(kamalPullFailed); i += 7 {
		w.Write([]byte(kamalPullFailed[i:min(i+7, len(kamalPullFailed))]))
	}
	if !w.pullFailed() || out.String() != kamalPullFailed {
		t.Errorf("pullFailed %v; passed through intact: %v", w.pullFailed(), out.String() == kamalPullFailed)
	}
}
