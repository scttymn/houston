package cli

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// A branch instance starts from a copy of the main instance's data
// (docs/plans/dev-localhost.md, row 10).

const withVolumes = `name: shop
services:
  app:
    build: .
    expose: ["3000"]
    volumes: [storage:/app/storage]
  db:
    image: postgres:17
    volumes: [pgdata:/var/lib/postgresql/data]
volumes:
  storage:
  pgdata:
x-houston:
  health: /up
`

// onBranch writes compose.yml in a checkout of branch.
func onBranch(t *testing.T, branch string) (dir, path string) {
	dir, path = newProject(t, withVolumes, nil)
	must(t, os.MkdirAll(filepath.Join(dir, ".git"), 0o755))
	must(t, os.WriteFile(filepath.Join(dir, ".git", "HEAD"), []byte("ref: refs/heads/"+branch+"\n"), 0o644))
	return dir, path
}

// volumes answers docker for a world where these volumes exist and main's
// containers (ids) are running; copyErr fails the copy.
func volumes(existing []string, ids string, copyErr error) func([]string) ([]byte, error, bool) {
	proxy := proxyState("true " + devProxyImage + " 80")
	return func(args []string) ([]byte, error, bool) {
		switch {
		case args[0] == "volume" && args[1] == "inspect":
			for _, v := range existing {
				if v == args[2] {
					return nil, nil, true
				}
			}
			return nil, errors.New("Error: No such volume: " + args[2]), true
		case args[0] == "ps" && args[1] == "-q":
			if strings.HasSuffix(args[3], "=shop") {
				return []byte(ids), nil, true
			}
			return nil, nil, true
		case args[0] == "run" && args[len(args)-1] == "cp -a /from/. /to/":
			return nil, copyErr, true
		}
		return proxy(args)
	}
}

// verbs are the Output calls about volumes and pausing, in order.
func verbs(d *fakeDocker) []string {
	var got []string
	for _, o := range d.outputs {
		switch {
		case o[0] == "pause" || o[0] == "unpause":
			got = append(got, strings.Join(o, " "))
		case o[0] == "volume" && o[1] != "inspect":
			got = append(got, o[0]+" "+o[1]+" "+o[len(o)-1])
		case o[0] == "run" && o[len(o)-1] == "cp -a /from/. /to/":
			got = append(got, "copy "+o[3]+" "+o[5])
		}
	}
	return got
}

func TestDevCopiesMainsData(t *testing.T) {
	t.Run("a branch's first run: main paused, each volume copied, unpaused", func(t *testing.T) {
		_, path := onBranch(t, "feature1")
		d := &fakeDocker{outputFn: volumes([]string{"shop_storage", "shop_pgdata"}, "c1\nc2\n", nil)}
		code, _, stderr := run(d, "-f", path, "dev")
		want := []string{"pause c1 c2",
			"volume create shop-feature1_pgdata", "copy shop_pgdata:/from:ro shop-feature1_pgdata:/to",
			"volume create shop-feature1_storage", "copy shop_storage:/from:ro shop-feature1_storage:/to",
			"unpause c1 c2"}
		if code != 0 || !reflect.DeepEqual(verbs(d), want) {
			t.Errorf("exit %d\n got %q\nwant %q\n%s", code, verbs(d), want, stderr)
		}
		if !strings.Contains(stderr, "copying shop's data") || len(d.runs) != 1 || d.runs[0][2] != "shop-feature1" {
			t.Errorf("stderr %q, runs %q", stderr, d.runs)
		}
		created := d.outputs[indexOf(d.outputs, "volume", "create")]
		if !contains(created, "com.docker.compose.project=shop-feature1") || !contains(created, "com.docker.compose.volume=pgdata") {
			t.Errorf("a volume compose won't recognise as its own: %q", created)
		}
		if copied := d.outputs[indexOf(d.outputs, "run")]; !contains(copied, devCopyImage) {
			t.Errorf("copy image: %q", copied)
		}
	})

	t.Run("a failed copy unpauses main, removes what it made, and starts nothing", func(t *testing.T) {
		_, path := onBranch(t, "feature1")
		d := &fakeDocker{outputFn: volumes([]string{"shop_storage", "shop_pgdata"}, "c1\n", errors.New("cp: write error: No space left on device"))}
		code, _, stderr := run(d, "-f", path, "dev")
		want := []string{"pause c1", "volume create shop-feature1_pgdata", "copy shop_pgdata:/from:ro shop-feature1_pgdata:/to", "volume rm shop-feature1_pgdata", "unpause c1"}
		if code != 1 || !reflect.DeepEqual(verbs(d), want) || len(d.runs) != 0 || !strings.Contains(stderr, "No space left") {
			t.Errorf("exit %d, runs %q\n got %q\nwant %q\n%s", code, d.runs, verbs(d), want, stderr)
		}
	})

	t.Run("a branch with its own data keeps it", func(t *testing.T) {
		_, path := onBranch(t, "feature1")
		d := &fakeDocker{outputFn: volumes([]string{"shop_storage", "shop_pgdata", "shop-feature1_pgdata"}, "c1\n", nil)}
		run(d, "-f", path, "dev")
		if got := verbs(d); len(got) != 0 {
			t.Errorf("touched: %q", got)
		}
	})

	t.Run("main with no data: the branch starts empty, and says so", func(t *testing.T) {
		_, path := onBranch(t, "feature1")
		d := &fakeDocker{outputFn: volumes(nil, "", nil)}
		_, _, stderr := run(d, "-f", path, "dev")
		if got := verbs(d); len(got) != 0 || !strings.Contains(stderr, "starts empty") {
			t.Errorf("%q: %s", got, stderr)
		}
	})

	t.Run("--fresh names what it replaces, then copies again", func(t *testing.T) {
		_, path := onBranch(t, "feature1")
		d := &fakeDocker{outputFn: volumes([]string{"shop_storage", "shop_pgdata", "shop-feature1_pgdata"}, "", nil)}
		code, _, stderr := run(d, "-f", path, "dev", "--fresh")
		want := []string{"volume rm shop-feature1_pgdata",
			"volume create shop-feature1_pgdata", "copy shop_pgdata:/from:ro shop-feature1_pgdata:/to",
			"volume create shop-feature1_storage", "copy shop_storage:/from:ro shop-feature1_storage:/to"}
		if code != 0 || !reflect.DeepEqual(verbs(d), want) || !strings.Contains(stderr, "replaces shop-feature1_pgdata") {
			t.Errorf("exit %d\n got %q\nwant %q\n%s", code, verbs(d), want, stderr)
		}
	})

	t.Run("--fresh on main, or while the branch runs, is refused", func(t *testing.T) {
		_, path := onBranch(t, "main")
		if code, _, stderr := run(&fakeDocker{outputFn: volumes(nil, "", nil)}, "-f", path, "dev", "--fresh"); code != 2 || !strings.Contains(stderr, "branch") {
			t.Errorf("main: exit %d: %s", code, stderr)
		}
		_, path = onBranch(t, "feature1")
		running := volumes([]string{"shop_pgdata", "shop-feature1_pgdata"}, "", nil)
		d := &fakeDocker{outputFn: func(args []string) ([]byte, error, bool) {
			if args[0] == "ps" && args[1] == "-q" && strings.HasSuffix(args[3], "=shop-feature1") {
				return []byte("b1\n"), nil, true
			}
			return running(args)
		}}
		if code, _, stderr := run(d, "-f", path, "dev", "--fresh"); code != 2 || !strings.Contains(stderr, "running") || len(verbs(d)) != 0 {
			t.Errorf("running branch: exit %d, %q: %s", code, verbs(d), stderr)
		}
	})

	t.Run("main copies nothing", func(t *testing.T) {
		_, path := onBranch(t, "main")
		d := &fakeDocker{outputFn: volumes([]string{"shop_pgdata"}, "c1\n", nil)}
		run(d, "-f", path, "dev")
		if got := verbs(d); len(got) != 0 {
			t.Errorf("main: %q", got)
		}
	})
}

func indexOf(calls [][]string, prefix ...string) int {
	for i, c := range calls {
		if len(c) >= len(prefix) && reflect.DeepEqual(c[:len(prefix)], prefix) {
			return i
		}
	}
	return 0
}
