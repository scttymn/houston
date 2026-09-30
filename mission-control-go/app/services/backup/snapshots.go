package backup

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/scttymn/houston/mission-control-go/app/models"
	"github.com/scttymn/houston/mission-control-go/app/services/dockercmd"
)

// Snapshot is one of a project's, as restic lists it: Houston keeps no
// snapshot table.
type Snapshot struct {
	ID       string    `json:"id"`
	ShortID  string    `json:"short_id"`
	Time     time.Time `json:"time"`
	Kind     string    `json:"kind"`
	Reason   string    `json:"reason"`
	Deploy   *int64    `json:"deploy"`
	Sha      string    `json:"sha"`
	Bytes    *int64    `json:"bytes"`
	Location string    `json:"-"`
}

// Unavailable is a location whose snapshots couldn't be read.
type Unavailable string

func (u Unavailable) Error() string { return string(u) }

// Snapshots lists projects' snapshots, newest first, cached for a while:
// listing a remote repository can take seconds. A GO backup forgets the
// project's list at its location.
type Snapshots struct {
	Docker dockercmd.Runner
	mu     sync.Mutex
	cache  map[string]cached
}

type cached struct {
	list []Snapshot
	at   time.Time
}

const (
	snapshotsFor   = 10 * time.Minute
	snapshotsLimit = 1000
	listTimeout    = 120 * time.Second
)

func cacheKey(project string, location int64) string {
	return project + "@" + strconv.FormatInt(location, 10)
}

// For is project's snapshots in location, newest first (at most 1000).
func (s *Snapshots) For(ctx context.Context, project string, l models.StorageLocation) ([]Snapshot, error) {
	key := cacheKey(project, l.ID)
	s.mu.Lock()
	if c, ok := s.cache[key]; ok && time.Since(c.at) < snapshotsFor {
		s.mu.Unlock()
		return c.list, nil
	}
	s.mu.Unlock()
	list, err := s.list(ctx, project, l)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	if s.cache == nil {
		s.cache = map[string]cached{}
	}
	s.cache[key] = cached{list: list, at: time.Now()}
	s.mu.Unlock()
	return list, nil
}

// Forget drops project's cached list at location.
func (s *Snapshots) Forget(project string, location int64) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.cache, cacheKey(project, location))
}

func (s *Snapshots) list(ctx context.Context, project string, l models.StorageLocation) ([]Snapshot, error) {
	env := ResticEnv(l)
	ran := s.Docker.Run(ctx, ResticArgs(l, env, []string{"snapshots", "--json", "--host", "houston", "--tag", "project:" + project}, "", nil),
		dockercmd.Opts{Env: env, Timeout: listTimeout})
	if !ran.OK {
		msg := strings.TrimSpace(lastLines(ran.Output, 5))
		if len(msg) > 500 {
			msg = msg[:497] + "..."
		}
		return nil, Unavailable(msg)
	}
	var entries []struct {
		ID      string   `json:"id"`
		ShortID string   `json:"short_id"`
		Time    string   `json:"time"`
		Tags    []string `json:"tags"`
		Summary struct {
			Bytes *int64 `json:"total_bytes_processed"`
		} `json:"summary"`
	}
	if err := json.Unmarshal([]byte(ran.Output), &entries); err != nil {
		out := ran.Output
		if len(out) > 200 {
			out = out[:197] + "..."
		}
		return nil, Unavailable("restic's list wasn't JSON: " + out)
	}
	list := make([]Snapshot, 0, len(entries))
	for _, e := range entries {
		t, err := time.Parse(time.RFC3339Nano, e.Time)
		if err != nil {
			return nil, Unavailable(fmt.Sprintf("restic's list has a time %q", e.Time))
		}
		tags := map[string]string{}
		for _, tag := range e.Tags {
			if k, v, ok := strings.Cut(tag, ":"); ok {
				tags[k] = v
			}
		}
		snap := Snapshot{ID: e.ID, ShortID: e.ShortID, Time: t.UTC(), Kind: tags["kind"], Reason: tags["reason"], Sha: tags["sha"], Bytes: e.Summary.Bytes}
		if snap.ShortID == "" {
			snap.ShortID = e.ID[:min(8, len(e.ID))]
		}
		if d, ok := tags["deploy"]; ok {
			n, _ := strconv.ParseInt(d, 10, 64)
			snap.Deploy = &n
		}
		list = append(list, snap)
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].Time.After(list[j].Time) })
	return list[:min(len(list), snapshotsLimit)], nil
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	return strings.Join(lines[max(0, len(lines)-n):], "\n")
}

// LocationsFor are every location the project's backups used, the ones
// holding the final snapshots of deleted projects of the same name, and
// its current target, by name.
func LocationsFor(ctx context.Context, q *models.Queries, p models.Project) ([]models.StorageLocation, error) {
	used, err := q.LocationsUsedBy(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	finals, err := q.DeletionSnapshotLocations(ctx, p.Name)
	if err != nil {
		return nil, err
	}
	for _, l := range finals {
		if !slices.ContainsFunc(used, func(u models.StorageLocation) bool { return u.ID == l.ID }) {
			used = append(used, l)
		}
	}
	sort.Slice(used, func(i, j int) bool { return used[i].Name < used[j].Name })
	target, err := q.BackupLocationFor(ctx, p.BackupLocationID.Int64)
	if err == nil {
		seen := false
		for _, l := range used {
			seen = seen || l.ID == target.ID
		}
		if !seen {
			used = append(used, target)
			sort.Slice(used, func(i, j int) bool { return used[i].Name < used[j].Name })
		}
	}
	return used, nil
}
