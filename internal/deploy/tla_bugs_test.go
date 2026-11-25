package deploy

// Regression tests for TLA+ findings C1 and C2: a deploy that already
// switched traffic must end GO, even if the GO report is lost once or
// Mission Control is unreachable for a stretch after the switch.

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/scttymn/houston/internal/mission"
)

// C1 (TLA trace: Kamal -> FinishGo[lost] -> Tick x2 -> Claim abandons -> NO-GO while serving).
// One transient failure of the final GO report must not lose the GO: the runner
// has to get it through (retry), or Mission Control later marks a deploy that
// switched traffic as abandoned NO-GO.
func TestTLA_C1_LostGoReportIsRetried(t *testing.T) {
	h := newHarness(t, shopCompose)
	var failedOnce atomic.Bool
	h.mission.reportErr = func(p mission.Progress) error {
		if p.Status == "go" && failedOnce.CompareAndSwap(false, true) {
			return errors.New("Post \"https://mc/api/deploys/9\": read: connection reset by peer")
		}
		return nil
	}
	code := h.run()
	if !failedOnce.Load() {
		t.Fatal("the GO report was never sent")
	}
	if final := h.mission.final(); final.Status != "go" {
		t.Errorf("exit %d, kamal deploy succeeded, but Mission Control never got GO (last status it got: %q); "+
			"the row stays in flight, heartbeats have stopped, and the next claim abandons it as NO-GO", code, final.Status)
	}
}

// C2 (TLA trace: HandStart -> Kamal -> StopAfterKamal). Mission Control is unreachable
// for longer than FenceAfter right after kamal deploy has switched traffic (during
// post_deploy), then reachable again. The record must end GO: the new version serves.
// (Review 2 #9: the network comes back on its own, not at stop()'s lock release, so a
// fix that skips stop() is judged on the GO it sends.)
func TestTLA_C2_NoNoGoAfterTrafficSwitched(t *testing.T) {
	h := newHarness(t, shopCompose)
	h.timing = Options{HeartbeatEvery: 5 * time.Millisecond, FenceAfter: 40 * time.Millisecond, Timeout: 2 * time.Second}
	var switched atomic.Bool
	var mu sync.Mutex
	var downFrom time.Time
	const outage = 100 * time.Millisecond // longer than FenceAfter
	h.docker.onStream = func(what string, args []string) {
		switch what {
		case "kamal deploy --skip-push --version " + sha:
			switched.Store(true)
		case "post_deploy":
			mu.Lock()
			downFrom = time.Now() // the network drops after the switch
			mu.Unlock()
		}
	}
	h.docker.hold = func(what string) time.Duration {
		if what == "post_deploy" {
			return outage + 20*time.Millisecond // post_deploy outlasts the outage
		}
		return 0
	}
	h.mission.reportErr = func(p mission.Progress) error {
		mu.Lock()
		d := downFrom
		mu.Unlock()
		if !d.IsZero() && time.Since(d) < outage {
			return errors.New("can't reach Mission Control: connection refused")
		}
		return nil
	}
	h.run()
	if !switched.Load() {
		t.Fatal("kamal deploy never ran")
	}
	if final := h.mission.final(); final.Status != "go" {
		t.Errorf("final status %q (%q); want go: kamal deploy had already switched traffic to %s", final.Status, final.Error, sha[:7])
	}
}
