package cli

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/scttymn/houston/internal/docker"
)

// devRecord is a branch instance houston dev started (docs/plans/dev-worktrees.md):
// its Compose project, its app (the compose file's name), its address and
// the checkout it ran in. houston dev prune reads these to know what's its.
type devRecord struct {
	Project string `json:"project"`
	App     string `json:"app"`
	Host    string `json:"host"`
	Dir     string `json:"dir"`
}

// The records live in the repo's shared git directory, beside every
// worktree's own, and are never committed.
const devRecordsFile = "houston-dev-instances.json"

func readDevRecords(common string) ([]devRecord, error) {
	data, err := os.ReadFile(filepath.Join(common, devRecordsFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var recs []devRecord
	if err := json.Unmarshal(data, &recs); err != nil {
		return nil, fmt.Errorf("%s: %v", devRecordsFile, err)
	}
	return recs, nil
}

func writeDevRecords(common string, recs []devRecord) error {
	if recs == nil {
		recs = []devRecord{} // [], not null
	}
	data, err := json.MarshalIndent(recs, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(common, devRecordsFile+".tmp")
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(common, devRecordsFile))
}

// recordDevInstance keeps one record per project: a later run replaces it.
func recordDevInstance(common string, r devRecord) error {
	recs, err := readDevRecords(common)
	if err != nil {
		return err
	}
	for i := range recs {
		if recs[i].Project == r.Project {
			if recs[i] == r {
				return nil
			}
			recs[i] = r
			return writeDevRecords(common, recs)
		}
	}
	return writeDevRecords(common, append(recs, r))
}

// runDevPrune implements houston dev prune: the branch instances this
// repo's houston dev started that no checkout runs now (its worktree was
// removed, or it's on another branch or name) lose their containers,
// volumes and networks. Main, anything running, and anything not recorded
// are never touched. It asks first, unless --yes.
func runDevPrune(file string, yes bool, stdin io.Reader, stdout, stderr io.Writer, d docker.Runner) int {
	abs, err := filepath.Abs(file)
	if err != nil {
		fmt.Fprintf(stderr, "houston: %v\n", err)
		return exitUsage
	}
	p, ok := loadProject(file, stderr)
	if !ok {
		return exitUsage
	}
	_, _, common := gitDirs(filepath.Dir(abs))
	if common == "" {
		fmt.Fprintln(stdout, "Nothing to prune: this isn't a git checkout, so it has no branch instances.")
		return 0
	}
	recs, err := readDevRecords(common)
	if err != nil {
		fmt.Fprintf(stderr, "houston: %v\n", err)
		return exitFailure
	}

	// What some checkout runs now, in dev and --production form; and main, always.
	live := map[string]bool{p.Name: true, p.Name + "-production": true}
	for _, c := range checkouts(common) {
		instance, err := devInstance(c.dir, p, savedDevName(c.own))
		if err != nil {
			continue
		}
		live[devNaming(p.Name, instance, false).Project] = true
		live[devNaming(p.Name, instance, true).Project] = true
	}
	var stale []devRecord
	for _, r := range recs {
		if r.App == p.Name && !live[r.Project] {
			stale = append(stale, r)
		}
	}
	if len(stale) == 0 {
		fmt.Fprintln(stdout, "Nothing to prune: every branch instance here is one a checkout runs.")
		return 0
	}

	sizes := volumeSizes(d)
	var gone []devRecord
	fmt.Fprintln(stdout, "No checkout runs these any more:")
	for _, r := range stale {
		if len(running(d, r.Project)) > 0 {
			fmt.Fprintf(stdout, "  %s is running; stop its houston dev (in %s) first\n", r.Host, r.Dir)
			continue
		}
		vols := byProject(d, r.Project, "volume", "ls", "-q")
		var sized []string
		for _, v := range vols {
			if s := sizes[v]; s != "" {
				sized = append(sized, v+" "+s)
			} else {
				sized = append(sized, v)
			}
		}
		what := "no volumes"
		if len(sized) > 0 {
			what = strings.Join(sized, ", ")
		}
		fmt.Fprintf(stdout, "  %-40s %s (%s)\n", r.Host, r.Dir, what)
		gone = append(gone, r)
	}
	if len(gone) == 0 {
		return 0
	}

	if !yes {
		if !stdinIsTerminal() {
			fmt.Fprintln(stderr, "houston: nothing removed; to remove them without asking: houston dev prune --yes")
			return exitUsage
		}
		fmt.Fprint(stdout, "Remove them? [y/N] ")
		answer, _ := bufio.NewReader(stdin).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(answer)); a != "y" && a != "yes" {
			fmt.Fprintln(stdout, "Nothing removed.")
			return 0
		}
	}

	removed := map[string]bool{}
	code := 0
	for _, r := range gone {
		if err := removeProject(d, r.Project); err != nil {
			fmt.Fprintf(stderr, "houston: %s: %v\n", r.Host, err)
			code = exitFailure
			continue
		}
		removed[r.Project] = true
		fmt.Fprintf(stdout, "removed %s\n", r.Host)
	}
	var kept []devRecord
	for _, r := range recs {
		if !removed[r.Project] {
			kept = append(kept, r)
		}
	}
	if err := writeDevRecords(common, kept); err != nil {
		fmt.Fprintf(stderr, "houston: %v\n", err)
		return exitFailure
	}
	return code
}

// byProject lists a Compose project's containers, volumes or networks by
// Compose's project label.
func byProject(d docker.Runner, project string, args ...string) []string {
	out, _ := d.Output(append(args, "--filter", "label=com.docker.compose.project="+project)...)
	return strings.Fields(string(out))
}

// removeProject removes a stopped Compose project's containers, then its
// volumes and networks.
func removeProject(d docker.Runner, project string) error {
	steps := []struct{ list, remove []string }{
		{[]string{"ps", "-aq"}, []string{"rm", "-f"}},
		{[]string{"volume", "ls", "-q"}, []string{"volume", "rm"}},
		{[]string{"network", "ls", "-q"}, []string{"network", "rm"}},
	}
	for _, s := range steps {
		if ids := byProject(d, project, s.list...); len(ids) > 0 {
			if out, err := d.Output(append(s.remove, ids...)...); err != nil {
				return fmt.Errorf("%s: %v %s", strings.Join(s.remove, " "), err, strings.TrimSpace(string(out)))
			}
		}
	}
	return nil
}

// volumeSizes is each volume's size as Docker reports it ("48.2MB"); empty
// when Docker can't say.
func volumeSizes(d docker.Runner) map[string]string {
	out, err := d.Output("system", "df", "-v", "--format", "{{json .Volumes}}")
	sizes := map[string]string{}
	if err != nil {
		return sizes
	}
	var vols []struct{ Name, Size string }
	if json.Unmarshal(out, &vols) == nil {
		for _, v := range vols {
			sizes[v.Name] = v.Size
		}
	}
	return sizes
}
