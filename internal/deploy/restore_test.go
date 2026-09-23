package deploy

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/scttymn/houston/internal/kamal"
	"github.com/scttymn/houston/internal/mission"
	"github.com/scttymn/houston/internal/project"
)

// A claimed restore (docs/plans/restore.md, Batch 6): generation 2 is built
// beside the serving generation 1, then traffic switches, then 1 goes.
func restoreHarness(t *testing.T) *harness {
	t.Helper()
	return restoreHarnessFor(t, shopCompose)
}

func restoreHarnessFor(t *testing.T, compose string) *harness {
	t.Helper()
	h := newHarness(t, compose)
	h.snapshotEvery = time.Millisecond
	// The serving generation, as Mission Control knows it: its accessories
	// (cache isn't in this commit's compose.yml), and what a snapshot holds.
	h.claimed = &mission.Deploy{ID: 9, Number: 4, Token: "t", Kind: "restore", Generation: 2, PreviousGeneration: 1,
		PreviousAccessories: []string{"shop-db", "shop-cache"}, PreviousVolumes: []string{"shop_storage"}, PreviousDatabases: []string{"shop-db"}}
	// Both generations' volumes; one no backup holds (a cache's); another
	// project's; one that only contains the prefix.
	h.docker.volumes = []string{"shop_storage", "shop_pgdata", "shop_objects", "shop.g2_pgdata", "shop.g2_storage", "shopfront_data", "old.shop_x"}
	h.docker.mounts = map[string]string{"shop-db": "shop_pgdata"}
	// kamal-proxy routes to c1, generation 1 (no label); c2 is generation 2.
	h.docker.proxy = proxyList("shop-web", "c1")
	h.mission.restoreData = mission.Snapshot{ID: 31, Status: "queued"}
	h.mission.restoreDataPoll = []mission.Snapshot{{ID: 31, Status: "running"}, {ID: 31, Status: "go"}}
	h.mission.snapshot = mission.Snapshot{ID: 32, Status: "queued"}
	h.mission.snapshotPoll = []mission.Snapshot{{ID: 32, Status: "go", SnapshotID: "5c5edd4c" + strings.Repeat("0", 56), SHA: sha, Bytes: 5}}
	// The restore's ref: any past commit, not the deploy rule's branch.
	h.ref = "refs/restore/5c5edd4c"
	h.git.refs[h.ref] = sha
	p, err := project.Load(filepath.Join(h.dir, "compose.yml"))
	if err != nil {
		t.Fatal(err)
	}
	h.docker.labels = map[string]string{"c1": "", "c2": "2"}
	for name, label := range kamal.AccessoryLabels(p, 2) {
		h.docker.labels[p.Name+"-"+name] = label
	}
	return h
}

// switchesProxy makes kamal deploy switch kamal-proxy to generation 2's c2.
func (h *harness) switchesProxy() {
	h.docker.onStream = func(what string, args []string) {
		if strings.HasPrefix(what, "kamal deploy") {
			h.docker.mu.Lock()
			h.docker.proxy = proxyList("shop-web", "c2")
			h.docker.mu.Unlock()
		}
	}
}

// removed: what was removed (containers, volumes), in order.
func (h *harness) removed() []string {
	var out []string
	for _, args := range h.docker.outputs {
		if args[0] == "rm" || args[0] == "volume" && args[1] == "rm" {
			out = append(out, strings.Join(args, " "))
		}
	}
	return out
}

// emptied: the volumes a helper container emptied.
func (h *harness) emptied() []string {
	var out []string
	for _, args := range h.docker.outputs {
		if args[0] == "run" && slices.Contains(args, "find /v -mindepth 1 -delete") {
			out = append(out, strings.TrimSuffix(args[slices.Index(args, "-v")+1], ":/v"))
		}
	}
	return out
}

func failKamalDeploy(what string) int {
	if strings.HasPrefix(what, "kamal deploy") {
		return 1
	}
	return 0
}

func TestRestore(t *testing.T) {
	h := restoreHarness(t)
	h.switchesProxy()
	h.docker.held = map[string]string{"shop_storage": "old1\n"} // a stopped old app version
	if code := h.run(); code != 0 {
		t.Fatalf("exit %d\n%s", code, h.stderr.String())
	}
	// One sync, the restore's check (its id and token): Mission Control
	// applies the compose.yml it keeps at the switch; the runner doesn't.
	if want := []string{"sync", "restore data", "restore data?", "restore data?", "snapshot", "snapshot?"}; !slices.Equal(h.mission.calls, want) {
		t.Errorf("mission calls = %v, want %v", h.mission.calls, want)
	}
	if s := h.mission.syncs[0]; s.RestoreDeploy != 9 || s.DeployToken != "t" || s.ServingGeneration != 1 {
		t.Errorf("sync: %+v", s)
	}
	want := []string{"pull 127.0.0.1:5000/shop:" + sha, "kamal accessory boot all --version " + sha, "kamal deploy --skip-push --version " + sha}
	if got := h.docker.whats(); !slices.Equal(got, want) {
		t.Errorf("docker calls:\n%v\nwant\n%v", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	config, _ := os.ReadFile(filepath.Join(h.dir, ".houston", "kamal", "config", "deploy.yml"))
	if !strings.Contains(string(config), "db-g2:") || !strings.Contains(string(config), "shop.g2_storage:") {
		t.Errorf("deploy.yml isn't generation 2's:\n%s", config)
	}
	if !h.finishedWith("go", "") {
		t.Errorf("reports: %+v", h.mission.reports)
	}
	// Generation 1 goes after the switch, only what a snapshot holds: the
	// serving config's accessories, the old app version holding its volume,
	// its app volume and its Postgres's. shop_objects is kept, and said so.
	wantRemoved := []string{"rm -f shop-db shop-cache", "rm -f old1", "volume rm shop_storage shop_pgdata"}
	if got := h.removed(); !slices.Equal(got, wantRemoved) {
		t.Errorf("removed %v, want %v", got, wantRemoved)
	}
	if got := h.emptied(); !slices.Equal(got, []string{"shop_storage", "shop_pgdata"}) {
		t.Errorf("emptied %v", got)
	}
	mounts := slices.IndexFunc(h.docker.outputs, func(a []string) bool { return a[0] == "inspect" && slices.Contains(a, "shop-db") })
	rm := slices.IndexFunc(h.docker.outputs, func(a []string) bool { return a[0] == "rm" && slices.Contains(a, "shop-db") })
	if mounts < 0 || mounts > rm {
		t.Errorf("shop-db's volumes weren't read before it was removed")
	}
	if !strings.Contains(h.stdout.String(), "kept shop_objects: no backup holds it") {
		t.Errorf("log:\n%s", h.stdout.String())
	}
	for _, step := range []string{"Prepare", "Image", "Accessories", "Restore data", "Safety snapshot", "Switch", "Clean up"} {
		if !h.reported(step) {
			t.Errorf("no %s step reported", step)
		}
	}
	if len(h.exec.calls) != 0 {
		t.Errorf("a restore ran tests: %v", h.exec.calls)
	}
	if want := "Restore #4 of shop: " + sha[:7] + " (refs/restore/5c5edd4c), into data generation 2 (deadline 4h0m0s)"; !strings.Contains(h.stdout.String(), want) {
		t.Errorf("log lacks %q:\n%s", want, h.stdout.String())
	}
}

func TestRestoreRebuildsAPrunedImage(t *testing.T) {
	h := restoreHarness(t)
	h.switchesProxy()
	h.docker.exit = func(what string) int {
		if strings.HasPrefix(what, "pull ") {
			return 1
		}
		return 0
	}
	if code := h.run(); code != 0 {
		t.Fatalf("exit %d\n%s", code, h.stderr.String())
	}
	whats := h.docker.whats()
	if indexOf(whats, "build") < 0 || indexOf(whats, "push") < 0 || indexOf(whats, "push") > indexOf(whats, "kamal accessory boot") {
		t.Errorf("the pruned image wasn't rebuilt and pushed first: %v", whats)
	}
}

func TestRestoreFailures(t *testing.T) {
	gen2 := []string{"rm -f shop-db-g2", "volume rm shop.g2_pgdata shop.g2_storage"}
	for _, c := range []struct {
		name     string
		setup    func(*harness)
		error    string
		switched bool
	}{
		{"restore data NO-GO", func(h *harness) {
			h.mission.restoreDataPoll = []mission.Snapshot{{ID: 31, Status: "no_go", Error: "pg_restore of db's database \"shop\" failed"}}
		}, "restore failed at its data: pg_restore of db's database \"shop\" failed; the old version keeps serving", false},
		{"safety snapshot NO-GO", func(h *harness) {
			h.mission.snapshotPoll = []mission.Snapshot{{ID: 32, Status: "no_go", Error: "restic backup failed"}}
		}, "restore failed at its safety snapshot: restic backup failed; the old version keeps serving", false},
		{"kamal deploy fails", func(h *harness) { h.docker.exit = failKamalDeploy },
			"kamal deploy failed (exit 1); the old version keeps serving", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := restoreHarness(t)
			c.setup(h)
			if code := h.run(); code != 1 {
				t.Errorf("exit %d, want 1", code)
			}
			if !h.finishedWith("no_go", c.error) {
				t.Errorf("reports: %+v", h.mission.reports)
			}
			want := gen2
			if c.switched { // Kamal's container goes first, in case it's still at it
				want = append([]string{"rm -f houston-kamal-shop"}, gen2...)
			}
			if got := h.removed(); !slices.Equal(got, want) {
				t.Errorf("removed %v, want generation 2's only: %v", got, want)
			}
			if got := h.emptied(); !slices.Equal(got, []string{"shop.g2_pgdata", "shop.g2_storage"}) {
				t.Errorf("emptied %v", got)
			}
			if tried := indexOf(h.docker.whats(), "kamal deploy") >= 0; tried != c.switched {
				t.Errorf("kamal deploy tried: %v, want %v", tried, c.switched)
			}
			if h.reported("Clean up") {
				t.Errorf("Clean up reported without a switch")
			}
		})
	}
}

// kamal deploy failed after kamal-proxy switched to generation 2: the switch
// happened. Mission Control is told (the generation flips, the restore's
// compose.yml applies), and neither generation is removed.
func TestRestoreSwitchFailsAfterTheProxySwitched(t *testing.T) {
	h := restoreHarness(t)
	h.switchesProxy()
	h.docker.exit = failKamalDeploy
	if code := h.run(); code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
	if got := h.removed(); !slices.Equal(got, []string{"rm -f houston-kamal-shop"}) {
		t.Errorf("removed %v with generation 2 serving", got)
	}
	if !h.reported("Clean up") {
		t.Errorf("the switch wasn't reported: %+v", h.mission.reports)
	}
	if !h.finishedWith("no_go", "kamal deploy failed (exit 1) after kamal-proxy switched to the restored version: it's serving, and generation 1 is kept; remove it by hand once you've checked") {
		t.Errorf("reports: %+v", h.mission.reports)
	}
}

// Killed mid-switch (Kamal gone before it could stop the new version):
// generation 2's app runs, but kamal-proxy still routes to generation 1.
// Running isn't serving: it's a failed restore, and generation 2 goes, its
// running app included.
func TestRestoreSwitchKilledBeforeTheProxySwitched(t *testing.T) {
	h := restoreHarness(t)
	h.docker.exit = failKamalDeploy
	h.docker.webs = "newapp\n"
	h.docker.held = map[string]string{"shop.g2_storage": "newapp\n"}
	h.docker.up = map[string]bool{"newapp": true}
	if code := h.run(); code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
	if !slices.ContainsFunc(h.docker.outputs, func(a []string) bool { return slices.Equal(a, []string{"stop", "newapp"}) }) {
		t.Errorf("generation 2's app wasn't stopped before the proxy was read again")
	}
	want := []string{"rm -f houston-kamal-shop", "rm -f shop-db-g2", "rm -f newapp", "volume rm shop.g2_pgdata shop.g2_storage"}
	if got := h.removed(); !slices.Equal(got, want) {
		t.Errorf("removed %v, want %v", got, want)
	}
	if h.reported("Clean up") || !h.finishedWith("no_go", "kamal deploy failed (exit 1); the old version keeps serving") {
		t.Errorf("reports: %+v", h.mission.reports)
	}
}

// kamal-proxy can't be asked after a failed switch: nothing is removed, and
// Mission Control isn't told of a switch that may not have happened.
func TestRestoreSwitchFailsAndTheProxyCantSay(t *testing.T) {
	h := restoreHarness(t)
	h.docker.exit = func(what string) int {
		if strings.HasPrefix(what, "kamal deploy") {
			h.docker.proxyErr = true
			return 1
		}
		return 0
	}
	h.run()
	if got := h.removed(); !slices.Equal(got, []string{"rm -f houston-kamal-shop"}) || h.reported("Clean up") {
		t.Errorf("removed %v; reports %+v", got, h.mission.reports)
	}
}

// A volume something still runs on (a console someone opened) is kept:
// emptying it would go around docker volume rm's own in-use check.
func TestRestoreKeepsAVolumeInUse(t *testing.T) {
	h := restoreHarness(t)
	h.switchesProxy()
	h.docker.held = map[string]string{"shop_storage": "old1\nconsole\n"}
	h.docker.up = map[string]bool{"console": true}
	if code := h.run(); code != 0 {
		t.Fatalf("exit %d\n%s", code, h.stderr.String())
	}
	if got := h.removed(); !slices.Equal(got, []string{"rm -f shop-db shop-cache", "rm -f old1", "volume rm shop_pgdata"}) {
		t.Errorf("removed %v", got)
	}
	if got := h.emptied(); !slices.Equal(got, []string{"shop_pgdata"}) {
		t.Errorf("emptied %v", got)
	}
	if !strings.Contains(h.stdout.String(), "kept shop_storage: in use (console)") {
		t.Errorf("log:\n%s", h.stdout.String())
	}
}

// Generation 1 goes only if the safety snapshot holds it: a skipped one
// (nothing to back up, say) keeps it.
func TestRestoreKeepsTheOldGenerationWithoutASafetySnapshot(t *testing.T) {
	h := restoreHarness(t)
	h.switchesProxy()
	h.mission.snapshotPoll = []mission.Snapshot{{ID: 32, Status: "skipped", Error: "nothing to back up"}}
	if code := h.run(); code != 0 {
		t.Fatalf("exit %d\n%s", code, h.stderr.String())
	}
	if got := h.removed(); len(got) != 0 {
		t.Errorf("removed %v with no safety snapshot", got)
	}
	if !h.reported("Clean up") || !strings.Contains(h.stdout.String(), "no safety snapshot (nothing to back up): generation 1 will be kept") {
		t.Errorf("log:\n%s", h.stdout.String())
	}
}

// Killed mid-switch, kamal-proxy's own deploy goes on and may switch once
// generation 2's app passes its health check, after Houston read it. The app
// is stopped first, the proxy read again: here it had switched just before
// the stop, so the app is started again and the restore counts as switched.
func TestRestoreSwitchKilledAsTheProxySwitches(t *testing.T) {
	h := restoreHarness(t)
	h.docker.exit = failKamalDeploy
	h.docker.webs = "newapp\n"
	h.docker.onOutput = func(args []string) {
		if args[0] == "stop" {
			h.docker.proxy = proxyList("shop-web", "c2") // it switched a moment before
		}
	}
	h.run()
	stop := slices.IndexFunc(h.docker.outputs, func(a []string) bool { return slices.Equal(a, []string{"stop", "newapp"}) })
	start := slices.IndexFunc(h.docker.outputs, func(a []string) bool { return slices.Equal(a, []string{"start", "newapp"}) })
	if stop < 0 || start < stop {
		t.Errorf("outputs: %v", h.docker.outputs)
	}
	if !h.reported("Clean up") || slices.ContainsFunc(h.removed(), func(r string) bool { return strings.Contains(r, "g2") }) {
		t.Errorf("reports %+v; removed %v", h.mission.reports, h.removed())
	}
}

// The proxy can't be read again after generation 2's app was stopped: the
// app is started again (the proxy may have switched to it), and nothing of
// generation 2 is removed.
func TestRestoreSwitchKilledAndTheProxyGoesQuiet(t *testing.T) {
	h := restoreHarness(t)
	h.docker.exit = failKamalDeploy
	h.docker.webs = "newapp\n"
	h.docker.onOutput = func(args []string) {
		if args[0] == "stop" {
			h.docker.proxyErr = true
		}
	}
	h.run()
	if !slices.ContainsFunc(h.docker.outputs, func(a []string) bool { return slices.Equal(a, []string{"start", "newapp"}) }) {
		t.Errorf("generation 2's app left stopped: %v", h.docker.outputs)
	}
	if slices.ContainsFunc(h.removed(), func(r string) bool { return strings.Contains(r, "g2") }) || h.reported("Clean up") {
		t.Errorf("removed %v; reports %+v", h.removed(), h.mission.reports)
	}
}

// Generation 1 goes only once Mission Control has taken the Clean up step,
// which makes generation 2 the project's. It's retried until the fence.
func TestRestoreKeepsTheOldGenerationUnlessTheSwitchIsConfirmed(t *testing.T) {
	h := restoreHarness(t)
	h.switchesProxy()
	h.timing.FenceAfter = 30 * time.Millisecond
	tries := 0
	h.mission.reportErr = func(p mission.Progress) error {
		if p.Step == "Clean up" {
			tries++
			return errors.New("connection refused")
		}
		return nil
	}
	h.run()
	if got := h.removed(); len(got) != 0 {
		t.Errorf("removed %v without the switch confirmed", got)
	}
	if tries < 2 {
		t.Errorf("Clean up tried %d times", tries)
	}
	if !strings.Contains(h.stdout.String(), "generation 1 is kept") {
		t.Errorf("log:\n%s", h.stdout.String())
	}
}

// A claim whose generations don't add up touches nothing.
func TestRestoreRefusesABadClaim(t *testing.T) {
	for _, g := range [][2]int{{0, 2}, {2, 2}, {3, 2}, {0, 1}, {1, 3}} {
		h := restoreHarness(t)
		h.claimed.PreviousGeneration, h.claimed.Generation = g[0], g[1]
		if code := h.run(); code != 1 {
			t.Errorf("%v: exit %d", g, code)
		}
		if len(h.docker.calls) != 0 || len(h.removed()) != 0 {
			t.Errorf("%v: docker was used: %v %v", g, h.docker.whats(), h.docker.outputs)
		}
	}
}

// Mission Control answering 409 (no longer in flight) means another runner
// owns the restore now: nothing more is touched, generation 2 included.
func TestRestoreTakenOverAtItsData(t *testing.T) {
	h := restoreHarness(t)
	h.mission.restoreDataErr = fmt.Errorf("%w: restore #4 is no longer in flight", mission.ErrTakenOver)
	if code := h.run(); code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
	// (The runner's own Kamal and release containers go, as on any stop.)
	if got := h.removed(); slices.ContainsFunc(got, func(r string) bool { return strings.Contains(r, "g2") }) {
		t.Errorf("removed %v of a restore someone else owns", got)
	}
	if h.finishedWith("no_go", "") || h.reported("Safety snapshot") {
		t.Errorf("reports: %+v", h.mission.reports)
	}
}

// A restore stopped mid-Kamal (its deadline): Kamal's container goes before
// generation 2, or Kamal would go on booting into volumes being removed.
func TestRestoreStoppedRemovesKamalFirst(t *testing.T) {
	h := restoreHarness(t)
	h.timing.Timeout = 50 * time.Millisecond
	h.docker.block = func(what string) bool { return strings.HasPrefix(what, "kamal accessory boot") }
	if code := h.run(); code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
	got := h.removed()
	kamal, gen2 := slices.Index(got, "rm -f houston-kamal-shop"), slices.Index(got, "rm -f shop-db-g2")
	if kamal < 0 || gen2 < 0 || kamal > gen2 {
		t.Errorf("removed %v: Kamal's container must go first", got)
	}
}

// kamal-proxy already routing to generation 2 (a restore that switched but
// wasn't recorded) refuses the restore, as does a proxy that can't say.
// Generation 2's app merely running doesn't (TestRestore's c2 isn't routed).
func TestRestoreRefusesWhileTheProxyRoutesToTheNextGeneration(t *testing.T) {
	for want, setup := range map[string]func(*harness){
		"kamal-proxy routes to generation 2 already (a restore that switched but wasn't recorded); deploy once so Mission Control catches up, then restore again": func(h *harness) {
			h.docker.proxy = proxyList("shop-web", "c2")
		},
		"can't tell which generation kamal-proxy routes to; nothing was touched": func(h *harness) { h.docker.proxyErr = true },
	} {
		h := restoreHarness(t)
		setup(h)
		if code := h.run(); code != 1 {
			t.Errorf("exit %d, want 1", code)
		}
		if !h.finishedWith("no_go", want) {
			t.Errorf("reports: %+v", h.mission.reports)
		}
		if len(h.docker.calls) != 0 || len(h.removed()) != 0 || slices.Contains(h.mission.calls, "restore data") {
			t.Errorf("touched: %v %v %v", h.docker.whats(), h.removed(), h.mission.calls)
		}
	}
}

// Every sync says which generation kamal-proxy routes to, so Mission
// Control can catch up after a restore that switched but never said so.
func TestServingIsWhatTheProxyRoutesTo(t *testing.T) {
	for _, c := range []struct {
		name  string
		proxy string
		err   bool
		want  int
	}{
		{"nothing routed", proxyList("", ""), false, 0},
		{"generation 1 (no label)", proxyList("shop-web", "c1"), false, 1},
		{"generation 2", proxyList("shop-web", "c2"), false, 2},
		{"another project's", proxyList("shopfront-web", "c2"), false, 0},
		{"the proxy can't say", "", true, 0},
	} {
		h := newHarness(t, shopCompose)
		h.docker.proxy, h.docker.proxyErr = c.proxy, c.err
		h.docker.labels["c1"], h.docker.labels["c2"] = "", "2"
		old := strings.Repeat("e", 40)
		h.docker.names = map[string]string{"c1": "shop-web-" + old, "c2": "shop-web-" + sha + "_replaced_4f2a9c01"}
		h.run()
		wantSHA := map[int]string{1: old, 2: sha}[c.want]
		if len(h.mission.syncs) == 0 || h.mission.syncs[0].ServingGeneration != c.want || h.mission.syncs[0].ServingSHA != wantSHA || h.mission.syncs[0].RestoreDeploy != 0 {
			t.Errorf("%s: syncs %+v, want serving %d at %q", c.name, h.mission.syncs, c.want, wantSHA)
		}
	}
}

// An accessory whose data no backup holds starts empty in the restored
// generation; the log says so, and the old one's copy is kept.
func TestRestoreSaysWhatNoBackupHolds(t *testing.T) {
	compose := strings.Replace(shopCompose, "volumes:\n  storage:", "  cache:\n    image: redis:7\n    volumes:\n      - cachedata:/data\nvolumes:\n  cachedata:\n  storage:", 1)
	h := restoreHarnessFor(t, compose)
	h.switchesProxy()
	if code := h.run(); code != 0 {
		t.Fatalf("exit %d\n%s", code, h.stderr.String())
	}
	if !strings.Contains(h.stdout.String(), "cache's volume cachedata starts empty in generation 2: no backup holds it") {
		t.Errorf("log:\n%s", h.stdout.String())
	}
	if strings.Contains(h.stdout.String(), "db's volume") {
		t.Errorf("Postgres is backed up:\n%s", h.stdout.String())
	}
}
