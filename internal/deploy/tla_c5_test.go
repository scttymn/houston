package deploy

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/scttymn/houston/internal/mission"
)

// C5. Time scaled 1 s -> 1 ms: HeartbeatEvery 15, FenceAfter 60,
// HTTP timeout 30, StaleAfter 120. The last report Mission Control takes is a
// step report sent just after a heartbeat tick: it commits at T
// (heartbeat_at = T) and its answer lands 29 ms later. Then Mission Control
// is unreachable: posts fail fast while under FenceAfter since last contact,
// after that they hang for the client timeout. The runner must have stopped
// before T+StaleAfter, when Mission Control may hand the deploy to another
// runner (which releases Kamal's lock after Test and Secrets, and runs Kamal
// after Build and Snapshot).
func TestTLA_C5_FenceBeforeTakeover(t *testing.T) {
	const (
		every   = 15 * time.Millisecond
		fence   = 60 * time.Millisecond
		timeout = 30 * time.Millisecond
		stale   = 120 * time.Millisecond
	)
	h := newHarness(t, shopCompose)
	h.timing = Options{HeartbeatEvery: every, FenceAfter: fence, Timeout: 5 * time.Second}
	var (
		mu        sync.Mutex
		pushed    bool
		committed time.Time // heartbeat_at of the last report Mission Control took
		lastOK    time.Time
		stoppedAt time.Time
	)
	tick := make(chan struct{}, 1)
	h.docker.hold = func(what string) time.Duration {
		if what == "push" { // end the push just after a heartbeat tick
			for len(tick) > 0 {
				<-tick
			}
			<-tick
			mu.Lock()
			pushed = true
			mu.Unlock()
			return 500 * time.Microsecond
		}
		return 0
	}
	h.docker.onStream = func(what string, args []string) {
		if what == "kamal lock release --version "+sha {
			mu.Lock()
			if stoppedAt.IsZero() {
				stoppedAt = time.Now() // runCtx was cancelled: the fence
			}
			mu.Unlock()
		}
	}
	h.docker.block = func(what string) bool { return what == "kamal accessory boot all --version "+sha }
	h.mission.reportErr = func(p mission.Progress) error {
		mu.Lock()
		ready, c, ok := pushed, committed, lastOK
		mu.Unlock()
		if p.Step == "" && p.Status == "" {
			select {
			case tick <- struct{}{}:
			default:
			}
		}
		switch {
		case !ready:
			return nil
		case c.IsZero(): // the step report right after the push: the last that gets through
			now := time.Now()
			time.Sleep(timeout - time.Millisecond)
			mu.Lock()
			committed, lastOK = now, time.Now()
			mu.Unlock()
			return nil
		case time.Since(ok) < fence:
			return errors.New("connection refused") // fast failure, not yet past the fence
		default:
			time.Sleep(timeout) // the client's timeout
			return errors.New("context deadline exceeded (Client.Timeout exceeded while awaiting headers)")
		}
	}
	h.run()
	mu.Lock()
	defer mu.Unlock()
	if stoppedAt.IsZero() || committed.IsZero() {
		t.Fatalf("no fence (stopped %v, committed %v)", stoppedAt, committed)
	}
	// After a successful report, the stop must land within FenceAfter plus
	// the next heartbeat interval plus a hung client timeout, which is
	// under StaleAfter. The takeover threshold alone is too close to the
	// old code's stop to catch it reliably.
	bound := fence + every + timeout
	if took := stoppedAt.Sub(committed); took > bound {
		t.Errorf("fenced %v after its last heartbeat_at; want at most %v (FenceAfter + HeartbeatEvery + client timeout), under the %v takeover threshold",
			took.Round(time.Millisecond), bound, stale)
	}
}
