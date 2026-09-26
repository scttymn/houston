package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// houston dev in plain git worktrees (docs/plans/dev-worktrees.md): main's
// .env, a remembered --as, and houston dev prune.

const worktreeCompose = `name: shop
services:
  app:
    build: .
    expose: ["3000"]
    environment:
      SECRET_KEY_BASE: ${SECRET_KEY_BASE}
    volumes: [storage:/app/storage]
volumes:
  storage:
x-houston:
  health: /up
`

// mainCheckout is a checkout of shop on main, with .env when env isn't "".
func mainCheckout(t *testing.T, env string) (dir, path string) {
	files := map[string]string{".git/HEAD": "ref: refs/heads/main\n"}
	if env != "" {
		files[".env"] = env
	}
	return newProject(t, worktreeCompose, files)
}

// addWorktree adds a linked worktree of main on branch, as `git worktree
// add` lays it out: <main>/.git/worktrees/<id> and a .git file pointing at it.
func addWorktree(t *testing.T, main, id, branch string) (dir, path string) {
	admin := filepath.Join(main, ".git", "worktrees", id)
	must(t, os.MkdirAll(admin, 0o755))
	dir = filepath.Join(t.TempDir(), id)
	must(t, os.MkdirAll(dir, 0o755))
	path = filepath.Join(dir, "compose.yml")
	must(t, os.WriteFile(path, []byte(worktreeCompose), 0o644))
	must(t, os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: "+admin+"\n"), 0o644))
	must(t, os.WriteFile(filepath.Join(admin, "HEAD"), []byte("ref: refs/heads/"+branch+"\n"), 0o644))
	must(t, os.WriteFile(filepath.Join(admin, "commondir"), []byte("../..\n"), 0o644))
	must(t, os.WriteFile(filepath.Join(admin, "gitdir"), []byte(filepath.Join(dir, ".git")+"\n"), 0o644))
	return dir, path
}

// devDocker answers houston dev's calls for shop, the proxy up.
func devDocker() *fakeDocker { return &fakeDocker{outputFn: volumes(nil, "", nil)} }

// servedAt is the host a houston dev run registered with the proxy.
func servedAt(t *testing.T, d *fakeDocker) string {
	t.Helper()
	if len(d.streams) == 0 {
		t.Fatalf("nothing registered with the proxy; runs %q", d.runs)
	}
	return d.streams[0][slices.Index(d.streams[0], "--host")+1]
}

func TestDevWorktreeUsesMainsEnv(t *testing.T) {
	main, _ := mainCheckout(t, "SECRET_KEY_BASE=x\n")
	_, wtPath := addWorktree(t, main, "feature1", "feature1")

	d := devDocker()
	code, _, stderr := run(d, "-f", wtPath, "dev")
	mainEnv := filepath.Join(main, ".env")
	if code != 0 || len(d.runs) != 1 || !slices.Contains(d.runs[0], "--env-file") || d.runs[0][slices.Index(d.runs[0], "--env-file")+1] != mainEnv {
		t.Fatalf("exit %d, runs %q\n%s", code, d.runs, stderr)
	}
	if !strings.Contains(stderr, "no .env here; using the main checkout's ("+mainEnv+")") || strings.Contains(stderr, "warning:") {
		t.Errorf("stderr = %q", stderr)
	}

	// Its own .env wins.
	wt2, wt2Path := addWorktree(t, main, "feature2", "feature2")
	must(t, os.WriteFile(filepath.Join(wt2, ".env"), []byte("SECRET_KEY_BASE=y\n"), 0o644))
	d = devDocker()
	_, _, stderr = run(d, "-f", wt2Path, "dev")
	if slices.Contains(d.runs[0], "--env-file") || strings.Contains(stderr, "main checkout's") {
		t.Errorf("its own .env: runs %q\n%s", d.runs, stderr)
	}

	// Not a worktree: as before.
	_, plain := mainCheckout(t, "")
	d = devDocker()
	_, _, stderr = run(d, "-f", plain, "dev")
	if slices.Contains(d.runs[0], "--env-file") || !strings.Contains(stderr, "warning: no .env next to the compose file") {
		t.Errorf("no .env, not a worktree: runs %q\n%s", d.runs, stderr)
	}

	// A worktree whose main checkout has no .env either warns as before.
	bare, _ := mainCheckout(t, "")
	_, wt3Path := addWorktree(t, bare, "feature3", "feature3")
	d = devDocker()
	_, _, stderr = run(d, "-f", wt3Path, "dev")
	if slices.Contains(d.runs[0], "--env-file") || !strings.Contains(stderr, "warning: no .env next to the compose file") {
		t.Errorf("main has none either: runs %q\n%s", d.runs, stderr)
	}
}

func TestDevRemembersAs(t *testing.T) {
	main, mainPath := mainCheckout(t, "SECRET_KEY_BASE=x\n")
	_, wtPath := addWorktree(t, main, "feature1", "feature1")
	_, otherPath := addWorktree(t, main, "feature2", "feature2")
	saved := filepath.Join(main, ".git", "worktrees", "feature1", "houston-dev-name")

	d := devDocker()
	_, _, stderr := run(d, "-f", wtPath, "dev", "--as", "Login")
	if got := servedAt(t, d); got != "login.shop.localhost" {
		t.Errorf("--as Login served %s", got)
	}
	if b, err := os.ReadFile(saved); err != nil || string(b) != "login\n" {
		t.Errorf("saved %q, %v", b, err)
	}
	if !strings.Contains(stderr, "this checkout is login.shop.localhost from now on") {
		t.Errorf("stderr = %q", stderr)
	}

	d = devDocker()
	if run(d, "-f", wtPath, "dev"); servedAt(t, d) != "login.shop.localhost" {
		t.Errorf("remembered: %s", servedAt(t, d))
	}
	d = devDocker()
	if run(d, "-f", wtPath, "dev", "--as", "demo"); servedAt(t, d) != "demo.shop.localhost" {
		t.Errorf("an explicit --as: %s", servedAt(t, d))
	}
	d = devDocker()
	if run(d, "-f", otherPath, "dev"); servedAt(t, d) != "feature2.shop.localhost" {
		t.Errorf("another worktree: %s", servedAt(t, d))
	}
	d = devDocker()
	if run(d, "-f", mainPath, "dev"); servedAt(t, d) != "shop.localhost" {
		t.Errorf("main: %s", servedAt(t, d))
	}

	d = devDocker()
	_, _, stderr = run(d, "-f", wtPath, "dev", "--as=")
	if servedAt(t, d) != "feature1.shop.localhost" || !strings.Contains(stderr, "goes back to its branch's name: feature1.shop.localhost") {
		t.Errorf("--as= : %s\n%s", servedAt(t, d), stderr)
	}
	if _, err := os.Stat(saved); !os.IsNotExist(err) {
		t.Errorf("still saved: %v", err)
	}
}

// records reads what houston dev recorded in main's shared git directory.
func records(t *testing.T, main string) []devRecord {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(main, ".git", "houston-dev-instances.json"))
	if os.IsNotExist(err) {
		return nil
	}
	must(t, err)
	var got []devRecord
	must(t, json.Unmarshal(b, &got))
	return got
}

func TestDevRecordsInstances(t *testing.T) {
	main, mainPath := mainCheckout(t, "SECRET_KEY_BASE=x\n")
	wt, wtPath := addWorktree(t, main, "feature1", "feature1")

	run(devDocker(), "-f", mainPath, "dev")
	if got := records(t, main); len(got) != 0 {
		t.Errorf("main recorded: %+v", got)
	}
	run(devDocker(), "-f", wtPath, "dev")
	run(devDocker(), "-f", wtPath, "dev")
	run(devDocker(), "-f", wtPath, "dev", "--production")
	want := []devRecord{
		{Project: "shop-feature1", App: "shop", Host: "feature1.shop.localhost", Dir: wt},
		{Project: "shop-feature1-production", App: "shop", Host: "feature1.shop-production.localhost", Dir: wt},
	}
	if got := records(t, main); !slices.Equal(got, want) {
		t.Errorf("records\n got %+v\nwant %+v", got, want)
	}
}

// pruneDocker answers prune's questions about Compose projects: containers
// (running ones too), volumes and networks by project, and volume sizes.
func pruneDocker(containers, runningIDs, vols, nets map[string]string) *fakeDocker {
	project := func(args []string) string {
		for _, a := range args {
			if p, ok := strings.CutPrefix(a, "label=com.docker.compose.project="); ok {
				return p
			}
		}
		return ""
	}
	return &fakeDocker{outputFn: func(args []string) ([]byte, error, bool) {
		switch {
		case args[0] == "ps" && args[1] == "-q":
			return []byte(runningIDs[project(args)]), nil, true
		case args[0] == "ps" && args[1] == "-aq":
			return []byte(containers[project(args)]), nil, true
		case args[0] == "volume" && args[1] == "ls":
			return []byte(vols[project(args)]), nil, true
		case args[0] == "network" && args[1] == "ls":
			return []byte(nets[project(args)]), nil, true
		case args[0] == "system" && args[1] == "df":
			return []byte(`[{"Name":"shop-old_storage","Size":"48.2MB"},{"Name":"shop-feature1_storage","Size":"1.1MB"}]`), nil, true
		case args[0] == "rm" || args[0] == "volume" || args[0] == "network":
			return nil, nil, true
		}
		return nil, nil, false
	}}
}

func writeRecords(t *testing.T, main string, recs ...devRecord) {
	b, err := json.Marshal(recs)
	must(t, err)
	must(t, os.WriteFile(filepath.Join(main, ".git", "houston-dev-instances.json"), b, 0o644))
}

// removals are prune's rm, volume rm and network rm calls, in order.
func removals(d *fakeDocker) []string {
	var got []string
	for _, o := range d.outputs {
		if o[0] == "rm" || (o[0] == "volume" || o[0] == "network") && o[1] == "rm" {
			got = append(got, strings.Join(o, " "))
		}
	}
	return got
}

func TestDevPrune(t *testing.T) {
	oldRec := devRecord{Project: "shop-old", App: "shop", Host: "old.shop.localhost", Dir: "/gone/shop.old"}
	stale := map[string]string{"shop-old": "c9\n"}
	staleVols := map[string]string{"shop-old": "shop-old_storage\n"}
	staleNets := map[string]string{"shop-old": "shop-old_default\n"}

	t.Run("a removed worktree's instance goes, and so does one its worktree no longer runs", func(t *testing.T) {
		main, mainPath := mainCheckout(t, "")
		wt, _ := addWorktree(t, main, "feature1", "feature1")
		moved := devRecord{Project: "shop-feature9", App: "shop", Host: "feature9.shop.localhost", Dir: wt}
		live := devRecord{Project: "shop-feature1", App: "shop", Host: "feature1.shop.localhost", Dir: wt}
		// A worktree deleted by hand: git's entry (with its remembered name) stays behind.
		byHand, _ := addWorktree(t, main, "demo", "feature7")
		must(t, os.WriteFile(filepath.Join(main, ".git", "worktrees", "demo", "houston-dev-name"), []byte("login\n"), 0o644))
		must(t, os.RemoveAll(byHand))
		login := devRecord{Project: "shop-login", App: "shop", Host: "login.shop.localhost", Dir: byHand}
		writeRecords(t, main, oldRec, moved, live, login)
		d := pruneDocker(stale, nil, staleVols, staleNets)
		code, out, stderr := run(d, "-f", mainPath, "dev", "prune", "--yes")
		want := []string{"rm -f c9", "volume rm shop-old_storage", "network rm shop-old_default"}
		if code != 0 || !slices.Equal(removals(d), want) {
			t.Errorf("exit %d\n got %q\nwant %q\n%s%s", code, removals(d), want, out, stderr)
		}
		if !strings.Contains(out, "old.shop.localhost") || !strings.Contains(out, "48.2MB") || !strings.Contains(out, "feature9.shop.localhost") ||
			!strings.Contains(out, "login.shop.localhost") || strings.Contains(out, "feature1.shop.localhost") {
			t.Errorf("out = %q", out)
		}
		if got := records(t, main); !slices.Equal(got, []devRecord{live}) {
			t.Errorf("records left: %+v", got)
		}
	})

	t.Run("what a worktree runs stays, and main and other apps are never touched", func(t *testing.T) {
		main, mainPath := mainCheckout(t, "")
		// Main's own checkout is on a branch, so only the guard keeps shop and shop-production.
		must(t, os.WriteFile(filepath.Join(main, ".git", "HEAD"), []byte("ref: refs/heads/feature5\n"), 0o644))
		wt1, _ := addWorktree(t, main, "feature1", "feature1")
		wt2, _ := addWorktree(t, main, "feature2", "feature2")
		must(t, os.WriteFile(filepath.Join(main, ".git", "worktrees", "feature2", "houston-dev-name"), []byte("login\n"), 0o644))
		keep := []devRecord{
			{Project: "shop-feature1", App: "shop", Host: "feature1.shop.localhost", Dir: wt1},
			{Project: "shop-feature1-production", App: "shop", Host: "feature1.shop-production.localhost", Dir: wt1},
			{Project: "shop-login", App: "shop", Host: "login.shop.localhost", Dir: wt2},
			{Project: "shop", App: "shop", Host: "shop.localhost", Dir: main},
			{Project: "shop-production", App: "shop", Host: "shop-production.localhost", Dir: main},
			{Project: "cart-old", App: "cart", Host: "old.cart.localhost", Dir: "/gone/cart.old"},
		}
		writeRecords(t, main, keep...)
		d := pruneDocker(map[string]string{"shop-feature1": "c1\n", "cart-old": "c2\n"}, nil, nil, nil)
		code, out, _ := run(d, "-f", mainPath, "dev", "prune", "--yes")
		if code != 0 || len(removals(d)) != 0 || !strings.Contains(out, "Nothing to prune") {
			t.Errorf("exit %d, removed %q\n%s", code, removals(d), out)
		}
		if got := records(t, main); !slices.Equal(got, keep) {
			t.Errorf("records: %+v", got)
		}
	})

	t.Run("a running one is skipped", func(t *testing.T) {
		main, mainPath := mainCheckout(t, "")
		writeRecords(t, main, oldRec)
		d := pruneDocker(stale, map[string]string{"shop-old": "c9\n"}, staleVols, staleNets)
		code, out, _ := run(d, "-f", mainPath, "dev", "prune", "--yes")
		if code != 0 || len(removals(d)) != 0 || !strings.Contains(out, "old.shop.localhost is running") {
			t.Errorf("exit %d, removed %q\n%s", code, removals(d), out)
		}
		if got := records(t, main); len(got) != 1 {
			t.Errorf("records: %+v", got)
		}
	})

	t.Run("it asks first", func(t *testing.T) {
		terminal := func(is bool) {
			was := stdinIsTerminal
			stdinIsTerminal = func() bool { return is }
			t.Cleanup(func() { stdinIsTerminal = was })
		}
		main, mainPath := mainCheckout(t, "")
		writeRecords(t, main, oldRec)

		terminal(false)
		d := pruneDocker(stale, nil, staleVols, staleNets)
		if code, _, stderr := run(d, "-f", mainPath, "dev", "prune"); code != 2 || len(removals(d)) != 0 || !strings.Contains(stderr, "--yes") {
			t.Errorf("no terminal: exit %d, removed %q: %s", code, removals(d), stderr)
		}

		terminal(true)
		d = pruneDocker(stale, nil, staleVols, staleNets)
		if code, out, _ := runWithInput(d, "n\n", "-f", mainPath, "dev", "prune"); code != 0 || len(removals(d)) != 0 || !strings.Contains(out, "Remove them? [y/N]") {
			t.Errorf("n: exit %d, removed %q\n%s", code, removals(d), out)
		}
		d = pruneDocker(stale, nil, staleVols, staleNets)
		if code, _, _ := runWithInput(d, "y\n", "-f", mainPath, "dev", "prune"); code != 0 || len(removals(d)) != 3 {
			t.Errorf("y: exit %d, removed %q", code, removals(d))
		}
		if code, out, _ := run(pruneDocker(nil, nil, nil, nil), "-f", mainPath, "dev", "prune"); code != 0 || !strings.Contains(out, "Nothing to prune") {
			t.Errorf("nothing left: exit %d\n%s", code, out)
		}
		if b, _ := os.ReadFile(filepath.Join(main, ".git", "houston-dev-instances.json")); strings.TrimSpace(string(b)) != "[]" {
			t.Errorf("no records left is [], not %q", b)
		}
	})
}
