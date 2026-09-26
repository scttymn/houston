package deploy

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/compose-spec/compose-go/v2/types"

	"github.com/scttymn/houston/internal/kamal"
	"github.com/scttymn/houston/internal/mission"
)

// restore is a claimed restore (docs/plans/restore.md, Batch 6): the
// snapshot's commit, and its data put back into data generation g+1 beside
// g, which keeps serving until Kamal switches traffic. Until the switch, a
// failure removes g+1 and leaves g as it was. After it, Mission Control makes
// g+1 the project's generation, and only then is g removed.
func (r *run) restore() int {
	claim := r.report.deploy
	if r.generation < 2 || claim.PreviousGeneration != r.generation-1 {
		// A claim from a Mission Control that doesn't know generations, or one
		// that doesn't add up: never build over, or clean up, the wrong one.
		return r.noGo(fmt.Sprintf("the restore's generations don't add up (from %d to %d); nothing was touched", claim.PreviousGeneration, r.generation))
	}
	switch serving, known := routed(r.d.Docker, r.p.Name); {
	case !known:
		return r.noGo("can't tell which generation kamal-proxy routes to; nothing was touched")
	case serving.generation == r.generation:
		return r.noGo(fmt.Sprintf("kamal-proxy routes to generation %d already (a restore that switched but wasn't recorded); deploy once so Mission Control catches up, then restore again", r.generation))
	}

	r.report.step("Prepare")
	if msg := r.secrets(); msg != "" {
		return r.restoreFailed(msg)
	}
	if err := r.writeKamalFiles(); err != nil {
		return r.restoreFailed(fmt.Sprintf("can't write .houston/kamal: %v", err))
	}
	r.warnUnsaved()
	if claim.TookOver > 0 {
		r.report.logf("Releasing Kamal's deploy lock, left by the silent deploy.\n")
		if code, err := r.kamal("lock", "release", "--version", r.sha); err != nil || code != 0 {
			r.report.logf("kamal lock release: exit %d %v\n", code, errText(err))
		}
	}

	// The commit's image is usually still in Houston's registry; a pruned
	// one is built again from the commit.
	r.report.step("Image")
	if code, err := r.d.Docker.Stream(r.ctx, r.dir, nil, r.report, "pull", r.image); err != nil || code != 0 {
		if r.ctx.Err() != nil {
			return r.restoreStopped()
		}
		r.report.logf("%s isn't in Houston's registry any more; building it again.\n", r.sha[:7])
		if msg := r.build(); msg != "" {
			return r.restoreFailed(msg)
		}
	}

	if len(r.p.Compose.Services) > 1 {
		r.report.step("Accessories")
		if code, err := r.kamal("accessory", "boot", "all", "--version", r.sha); err != nil || code != 0 {
			return r.restoreFailed(fmt.Sprintf("the accessories didn't boot (exit %d%s)", code, errText(err)))
		}
		// Left from a restore that died: booted with another config.
		if msg := r.rebootChangedAccessories(); msg != "" {
			return r.restoreFailed(msg)
		}
	}

	r.report.step("Restore data")
	if _, err := r.await(r.d.Mission.RestoreData, r.d.Mission.RestoreDataStatus, "the restore's data"); err != nil {
		return r.restoreAwaitFailed("its data", err)
	}
	r.report.logf("ok  data restored into generation %d\n", r.generation)

	// The version that's serving, as it is just before the switch: the way
	// back if the restore was the wrong one.
	r.report.step("Safety snapshot")
	s, err := r.await(r.d.Mission.Snapshot, r.d.Mission.SnapshotStatus, "the safety snapshot")
	if err != nil {
		return r.restoreAwaitFailed("its safety snapshot", err)
	}
	// Generation g goes after the switch only if this snapshot holds it.
	safe := s.Status == "go"
	if safe {
		r.logSnapshot(s)
	} else {
		r.report.logf("no safety snapshot (%s): generation %d will be kept\n", s.Error, claim.PreviousGeneration)
	}

	r.report.step("Switch")
	code, err := r.kamalDeploy()
	if err != nil || code != 0 {
		// Whatever the exit, the switch happened only if kamal-proxy routes
		// to g+1. Kamal's container goes first: a killed docker CLI leaves it.
		r.removeKamal()
		switch next, known := r.routedToNext(); {
		case !known:
			msg := fmt.Sprintf("kamal deploy failed (exit %d%s), and kamal-proxy can't say which generation it routes to; both are kept: check, then deploy", code, errText(err))
			if r.ctx.Err() != nil {
				return r.stop()
			}
			return r.noGo(msg)
		case next:
			r.switched(false)
			return r.finishRestore("no_go", fmt.Sprintf("kamal deploy failed (exit %d%s) after kamal-proxy switched to the restored version: it's serving, and generation %d is kept; remove it by hand once you've checked", code, errText(err), claim.PreviousGeneration))
		}
		if r.ctx.Err() != nil {
			return r.restoreStopped()
		}
		return r.restoreFailed(fmt.Sprintf("kamal deploy failed (exit %d%s); the old version keeps serving", code, errText(err)))
	}
	r.switched(safe)
	return r.finishRestore("go", "")
}

// routedToNext settles whether kamal-proxy routes to g+1 after a switch
// that didn't finish cleanly. A killed Kamal doesn't cancel the proxy's own
// deploy, which may still switch once g+1's app passes its health check: so
// g+1's app is stopped (it can't pass any more), and the proxy read again.
// If it switched in between, the app is started again: it's serving.
func (r *run) routedToNext() (next, known bool) {
	serving, known := routed(r.d.Docker, r.p.Name)
	if !known || serving.generation == r.generation {
		return known, known
	}
	out, err := r.d.Docker.Output("ps", "-q", "--filter", "label=service="+r.p.Name, "--filter", "label=role=web",
		"--filter", "label="+kamal.GenerationLabel+"="+strconv.Itoa(r.generation))
	if err != nil {
		return false, false
	}
	apps := strings.Fields(string(out))
	if len(apps) > 0 {
		r.d.Docker.Output(append([]string{"stop"}, apps...)...)
	}
	serving, known = routed(r.d.Docker, r.p.Name)
	// Switched in between, or can't say now (it may have): start it again.
	if (!known || serving.generation == r.generation) && len(apps) > 0 {
		r.d.Docker.Output(append([]string{"start"}, apps...)...)
		r.report.logf("kamal-proxy switched to generation %d while Houston checked, or can't say; its app is started again.\n", r.generation)
	}
	return known && serving.generation == r.generation, known
}

// warnUnsaved says which of this commit's accessories keep data no backup
// holds (Houston backs up the app's volumes and Postgres): in g+1 they
// start empty, and g's copies are kept.
func (r *run) warnUnsaved() {
	postgres := map[string]bool{}
	for _, db := range mission.RequestFor(r.p).Databases {
		postgres[db.Service] = true
	}
	names := make([]string, 0, len(r.p.Compose.Services))
	for name := range r.p.Compose.Services {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if name == r.p.AppService || postgres[name] {
			continue
		}
		for _, v := range r.p.Compose.Services[name].Volumes {
			if v.Type == types.VolumeTypeVolume {
				r.report.logf("%s's volume %s starts empty in generation %d: no backup holds it (Houston backs up the app's volumes and Postgres). Generation %d's is kept.\n",
					name, v.Source, r.generation, r.report.deploy.PreviousGeneration)
			}
		}
	}
}

// switched is past the switch: Mission Control is told (the Clean up step,
// which makes g+1 the project's generation and applies the restore's
// compose.yml, kept since its check sync), then, if clean, g is removed.
// g goes only once that report is confirmed: before it, the next deploy
// would boot g.
func (r *run) switched(clean bool) {
	previous := r.report.deploy.PreviousGeneration
	if !r.report.stepConfirmed("Clean up") {
		r.report.logf("Mission Control didn't confirm the switch; generation %d is kept (the next sync catches Mission Control up; remove it by hand then).\n", previous)
		return
	}
	if clean {
		r.removePrevious()
	}
}

// finishRestore finishes past the switch: the restore is serving, so only a
// takeover stops the report, which is retried as the Clean up step was.
func (r *run) finishRestore(status, msg string) int {
	if errors.Is(context.Cause(r.ctx), errTakenOver) {
		return r.stop()
	}
	if status == "go" {
		r.report.logf("GO: %s is serving %s with the snapshot's data\n", r.p.Name, r.sha[:7])
	} else {
		r.report.logf("NO-GO: %s\n", msg)
		fmt.Fprintf(r.o.Stderr, "houston deploy: %s\n", msg)
	}
	r.report.confirmed(mission.Progress{Status: status, Error: msg})
	if status == "go" {
		return 0
	}
	return exitFailure
}

func (r *run) restoreAwaitFailed(what string, err error) int {
	switch {
	case errors.Is(err, errStopped):
		return r.restoreStopped()
	case errors.Is(err, errDeadline):
		r.dropNext()
		return r.noGo(fmt.Sprintf("restore failed at %s: it didn't finish before the restore's deadline (%s); the old version keeps serving", what, r.timeout))
	}
	return r.restoreFailed(fmt.Sprintf("restore failed at %s: %v; the old version keeps serving", what, err))
}

// restoreFailed ends a restore before the switch: g+1 goes, g stays.
func (r *run) restoreFailed(msg string) int {
	if r.ctx.Err() != nil {
		return r.restoreStopped()
	}
	r.dropNext()
	return r.noGo(msg)
}

// restoreStopped ends a restore whose context ended. A taken-over restore
// is someone else's, g+1 included. Otherwise Kamal's container goes first
// (cancelling only killed the docker CLI), then g+1.
func (r *run) restoreStopped() int {
	if !errors.Is(context.Cause(r.ctx), errTakenOver) {
		r.removeKamal()
		r.dropNext()
	}
	return r.stop()
}

func (r *run) removeKamal() { r.d.Docker.Output("rm", "-f", "houston-kamal-"+r.p.Name) }

// dropNext removes g+1, unless kamal-proxy routes to it (or can't say).
// Nothing in g+1 is only there: it's a snapshot's data, or empty.
func (r *run) dropNext() {
	if next, known := r.routedToNext(); !known || next {
		r.report.logf("kamal-proxy routes to generation %d, or can't say; both data generations are kept. Check which one serves before restoring or deploying again.\n", r.generation)
		return
	}
	r.report.logf("Removing generation %d.\n", r.generation)
	r.removeContainers(kamal.Accessories(r.p, r.generation))
	prefix := kamal.Names{Project: r.p.Name, Generation: r.generation}.VolumePrefix()
	volumes, err := r.volumesWith(prefix)
	if err != nil {
		r.report.logf("couldn't list generation %d's volumes: %v; remove them by hand\n", r.generation, err)
		return
	}
	r.removeVolumes(volumes, true)
}

// removePrevious removes g after the switch, but only what a snapshot
// holds: its accessories' containers, its app volumes, and its Postgres
// containers' volumes. Any other g volume is kept, and logged.
func (r *run) removePrevious() {
	claim := r.report.deploy
	r.report.logf("Removing generation %d.\n", claim.PreviousGeneration)
	volumes := append([]string{}, claim.PreviousVolumes...)
	for _, db := range claim.PreviousDatabases {
		out, err := r.d.Docker.Output("inspect", "-f", `{{range .Mounts}}{{if eq .Type "volume"}}{{.Name}} {{end}}{{end}}`, db)
		if err != nil {
			r.report.logf("couldn't read %s's volumes: %v; they're kept\n", db, err)
			continue
		}
		volumes = append(volumes, strings.Fields(string(out))...)
	}
	r.removeContainers(claim.PreviousAccessories)
	prefix := kamal.Names{Project: r.p.Name, Generation: claim.PreviousGeneration}.VolumePrefix()
	if all, err := r.volumesWith(prefix); err == nil {
		for _, v := range all {
			if !slices.Contains(volumes, v) {
				r.report.logf("kept %s: no backup holds it; remove it by hand when you're sure\n", v)
			}
		}
		// Only what's there (a volume the serving config named may never have been made).
		volumes = slices.DeleteFunc(volumes, func(v string) bool { return !slices.Contains(all, v) })
	}
	r.removeVolumes(volumes, false)
}

func (r *run) removeContainers(containers []string) {
	if len(containers) == 0 {
		return
	}
	if out, err := r.d.Docker.Output(append([]string{"rm", "-f"}, containers...)...); err != nil {
		r.report.logf("couldn't remove %s: %v %s\n", strings.Join(containers, ", "), err, strings.TrimSpace(string(out)))
	}
}

// volumesWith lists the volumes whose names begin with prefix (docker's
// name filter matches anywhere in the name).
func (r *run) volumesWith(prefix string) ([]string, error) {
	out, err := r.d.Docker.Output("volume", "ls", "-q", "--filter", "name="+prefix)
	if err != nil {
		return nil, err
	}
	var volumes []string
	for _, v := range strings.Fields(string(out)) {
		if strings.HasPrefix(v, prefix) {
			volumes = append(volumes, v)
		}
	}
	return volumes, nil
}

// removeVolumes removes the containers holding each volume (Kamal keeps old
// app versions, which pin them; all of them for g+1, only stopped ones for
// g), empties it (a volume placed on a storage location keeps its data
// there otherwise), and removes it. Best effort: what's left is logged.
func (r *run) removeVolumes(volumes []string, running bool) {
	var free []string
	for _, v := range volumes {
		args := []string{"ps", "-aq", "--filter", "volume=" + v}
		if !running {
			args = append(args, "--filter", "status=exited", "--filter", "status=created", "--filter", "status=dead")
		}
		held, _ := r.d.Docker.Output(args...)
		if ids := strings.Fields(string(held)); len(ids) > 0 {
			r.d.Docker.Output(append([]string{"rm", "-f"}, ids...)...)
		}
		// Anything still holding it (a console someone opened) keeps it:
		// emptying goes around docker volume rm's own in-use check.
		if still, err := r.d.Docker.Output("ps", "-aq", "--filter", "volume="+v); err != nil || strings.TrimSpace(string(still)) != "" {
			r.report.logf("kept %s: in use (%s); remove it by hand\n", v, strings.Join(strings.Fields(string(still)), ", "))
			continue
		}
		if out, err := r.d.Docker.Output("run", "--rm", "--user", "0", "-v", v+":/v", "--entrypoint", "sh", KamalImage, "-c", "find /v -mindepth 1 -delete"); err != nil {
			r.report.logf("couldn't empty %s: %v %s\n", v, err, strings.TrimSpace(string(out)))
		}
		free = append(free, v)
	}
	volumes = free
	if len(volumes) == 0 {
		return
	}
	if out, err := r.d.Docker.Output(append([]string{"volume", "rm"}, volumes...)...); err != nil {
		r.report.logf("couldn't remove the volumes %s: %v %s; remove them by hand\n", strings.Join(volumes, ", "), err, strings.TrimSpace(string(out)))
	}
}

// stepConfirmed reports step and says whether Mission Control took it.
func (r *reporter) stepConfirmed(step string) bool {
	return r.confirmed(mission.Progress{Step: step})
}

// confirmed sends p until Mission Control takes it, retryEvery apart, for
// as long as the fence allows (after that, the deploy may be taken over).
// A final status closes the reporter, as finish does.
func (r *reporter) confirmed(p mission.Progress) bool {
	until := time.Now().Add(r.fenceAfter)
	for {
		if r.send(p) {
			if p.Status != "" {
				r.mu.Lock()
				r.closed = true
				r.mu.Unlock()
			}
			return true
		}
		if r.isClosed() || time.Now().After(until) {
			return false // taken over, or Mission Control is gone
		}
		select {
		case <-r.ctx.Done():
			return false
		case <-time.After(r.retryEvery):
		}
	}
}
