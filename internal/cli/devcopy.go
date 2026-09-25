package cli

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/scttymn/houston/internal/docker"
	"github.com/scttymn/houston/internal/project"
)

// devCopyImage copies a volume's files (busybox, pinned by digest).
const devCopyImage = "busybox:1.37@sha256:bdf57e528e45e4433820e045b29b4597825a1c9e38353532d90a01445013f82e"

// copyMainData gives a branch instance its own copy of the main instance's
// named volumes on its first run (docs/plans/dev-localhost.md): none of the
// branch's volumes exist yet, or fresh asks to replace them. Main's running
// containers are paused while it copies, so the copy is what a sudden stop
// would leave, which Postgres and SQLite (and other databases) recover from.
// They're unpaused whatever happens.
func copyMainData(d docker.Runner, p *project.Project, n devNames, fresh bool, stderr io.Writer) error {
	names := make([]string, 0, len(p.Compose.Volumes))
	for name := range p.Compose.Volumes {
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) == 0 {
		return nil
	}
	exists := func(volume string) bool { _, err := d.Output("volume", "inspect", volume); return err == nil }

	var own []string
	for _, name := range names {
		if v := n.Project + "_" + name; exists(v) {
			own = append(own, v)
		}
	}
	if len(own) > 0 && !fresh {
		return nil
	}
	if len(own) > 0 {
		if len(running(d, n.Project)) > 0 {
			return usageError{fmt.Sprintf("%s is running; stop its houston dev before --fresh replaces its data", n.Host)}
		}
		fmt.Fprintf(stderr, "houston: --fresh replaces %s with a copy of %s's data\n", strings.Join(own, ", "), n.Main)
		for _, v := range own {
			if _, err := d.Output("volume", "rm", v); err != nil {
				return fmt.Errorf("couldn't remove %s: %v", v, err)
			}
		}
	}

	var from []string
	for _, name := range names {
		if exists(n.Main + "_" + name) {
			from = append(from, name)
		}
	}
	if len(from) == 0 {
		fmt.Fprintf(stderr, "houston: %s has no data yet, so %s starts empty\n", n.Main, n.Host)
		return nil
	}

	ids := running(d, n.Main)
	paused := ""
	if len(ids) > 0 {
		if _, err := d.Output(append([]string{"pause"}, ids...)...); err != nil {
			return fmt.Errorf("couldn't pause %s to copy its data: %v", n.Main, err)
		}
		defer d.Output(append([]string{"unpause"}, ids...)...)
		paused = ", paused meanwhile"
	}
	fmt.Fprintf(stderr, "houston: copying %s's data into %s (%s%s)\n", n.Main, n.Host, strings.Join(from, ", "), paused)
	var made []string
	undo := func() {
		for _, v := range made {
			d.Output("volume", "rm", v)
		}
	}
	for _, name := range from {
		src, dst := n.Main+"_"+name, n.Project+"_"+name
		// Compose's own labels, so compose takes the volume as its own.
		if _, err := d.Output("volume", "create", "--label", "com.docker.compose.project="+n.Project, "--label", "com.docker.compose.volume="+name, dst); err != nil {
			undo()
			return fmt.Errorf("couldn't create %s: %v", dst, err)
		}
		made = append(made, dst)
		if _, err := d.Output("run", "--rm", "-v", src+":/from:ro", "-v", dst+":/to", devCopyImage, "sh", "-c", "cp -a /from/. /to/"); err != nil {
			undo()
			return fmt.Errorf("couldn't copy %s into %s: %v", src, dst, err)
		}
	}
	return nil
}

// running is the ids of a Compose project's running containers.
func running(d docker.Runner, project string) []string {
	out, _ := d.Output("ps", "-q", "--filter", "label=com.docker.compose.project="+project)
	return strings.Fields(string(out))
}

// usageError is a refusal about how houston was asked (exit 2).
type usageError struct{ msg string }

func (e usageError) Error() string { return e.msg }
