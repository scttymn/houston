// Package appstats is each app's CPU, memory and disk right now, from
// Docker, for the flight board. It's read only
// while someone's looking: an open board asks every 30 seconds, and the API
// when asked, through Fresh, which reads Docker when the last reading is
// over FreshFor old. With no one looking, nothing reads Docker. The
// readings are kept in the process, so a page that renders shows the last.
//
// An app's containers are Houston's own names: its web containers
// <project>-web-<sha>, its accessories <project>-<service>[-g<n>], and its
// volumes <project>[.g<n>]_<name>. CPU is in cores; a limit counts only
// when every running container of the app has one. Without one, a share is
// of the host's: its cores and memory (docker info), and the disk Docker
// keeps its volumes on (the one Mission Control's container is on).
package appstats

import (
	"context"
	"encoding/json"
	"log/slog"
	"math"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/scttymn/houston/mission_control/app/models"
	"github.com/scttymn/houston/mission_control/app/services/dockercmd"
)

const (
	// StaleAfter is when a reading is too old to show as now.
	StaleAfter = 2 * time.Minute
	// FreshFor is how long a reading stands before Fresh reads again.
	FreshFor = 25 * time.Second
	// diskEvery is how often the volumes' sizes are read (slow).
	diskEvery = 5 * time.Minute
	timeout   = 20 * time.Second
)

// Reading is one app's, at SampledAt.
type Reading struct {
	CPUCores    float64
	CPULimit    *float64 // cores; nil without one on every container
	MemoryBytes int64
	MemoryLimit *int64
	DiskBytes   int64
	SampledAt   time.Time
	// Host is the host's cores, memory and disk ("cpus", "memory", "disk").
	Host map[string]int64
}

// MarshalJSON is the API's view of it.
func (r Reading) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{"cpu_cores": math.Round(r.CPUCores*1e4) / 1e4, "cpu_limit": r.CPULimit, "memory_bytes": r.MemoryBytes,
		"memory_limit": r.MemoryLimit, "disk_bytes": r.DiskBytes, "sampled_at": r.SampledAt})
}

// Stale is a reading too old to show as now.
func (r Reading) Stale(now time.Time) bool { return r.SampledAt.Before(now.Add(-StaleAfter)) }

func (r Reading) host(key string) float64 {
	if v := r.Host[key]; v > 0 {
		return float64(v)
	}
	return 0
}

// CPUWhole is what a CPU share is of: the limit, or the host's cores (0:
// unknown). MemoryWhole and DiskWhole are memory's and disk's.
func (r Reading) CPUWhole() float64 {
	if r.CPULimit != nil && *r.CPULimit > 0 {
		return *r.CPULimit
	}
	return r.host("cpus")
}

func (r Reading) MemoryWhole() float64 {
	if r.MemoryLimit != nil && *r.MemoryLimit > 0 {
		return float64(*r.MemoryLimit)
	}
	return r.host("memory")
}

func (r Reading) DiskWhole() float64 { return r.host("disk") }

// CPUOfHost and MemoryOfHost are whether a share is of the host's.
func (r Reading) CPUOfHost() bool {
	return !(r.CPULimit != nil && *r.CPULimit > 0) && r.CPUWhole() > 0
}

func (r Reading) MemoryOfHost() bool {
	return !(r.MemoryLimit != nil && *r.MemoryLimit > 0) && r.MemoryWhole() > 0
}

// Percents of the whole, unrounded; ok is false when the whole is unknown.
func (r Reading) CPUPercent() (float64, bool) { return percent(r.CPUCores, r.CPUWhole()) }
func (r Reading) MemoryPercent() (float64, bool) {
	return percent(float64(r.MemoryBytes), r.MemoryWhole())
}
func (r Reading) DiskPercent() (float64, bool) { return percent(float64(r.DiskBytes), r.DiskWhole()) }

func percent(part, whole float64) (float64, bool) {
	if whole <= 0 {
		return 0, false
	}
	return 100 * part / whole, true
}

// kept is the last reading of them all.
type kept struct {
	sampledAt time.Time
	projects  map[string]Reading
	disk      map[string]int64 // each volume's bytes; nil before the first read
	diskAt    time.Time
	host      map[string]int64
}

// Stats reads Docker and keeps the readings.
type Stats struct {
	Docker dockercmd.Runner
	// DiskSize is the size of the disk under Mission Control's container, in
	// bytes (0: unknown); nil is RootDisk.
	DiskSize func() int64
	Log      *slog.Logger

	sampling sync.Mutex
	mu       sync.Mutex
	kept     *kept
}

// For is the project's latest reading, or nil when nothing of it was
// running.
func (s *Stats) For(name string) *Reading {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.kept == nil {
		return nil
	}
	r, ok := s.kept.projects[name]
	if !ok {
		return nil
	}
	return &r
}

// Fresh reads Docker when the last reading is over FreshFor old: one read at
// a time, so boards open together share it (one that asks while another
// reads gets the reading there is).
func (s *Stats) Fresh(ctx context.Context, projects []models.Project, now time.Time) {
	s.mu.Lock()
	fresh := s.kept != nil && s.kept.sampledAt.After(now.Add(-FreshFor))
	s.mu.Unlock()
	if fresh || !s.sampling.TryLock() {
		return
	}
	defer s.sampling.Unlock()
	s.Sample(ctx, projects, now)
}

// Sample reads Docker and keeps a reading per project. Docker failing keeps
// the last readings.
func (s *Stats) Sample(ctx context.Context, projects []models.Project, now time.Time) {
	s.mu.Lock()
	last := s.kept
	s.mu.Unlock()
	if last == nil {
		last = &kept{}
	}
	running, ok := s.readContainers(ctx)
	if !ok {
		return
	}
	disk, diskAt, host := last.disk, last.diskAt, map[string]int64{}
	for k, v := range last.host {
		host[k] = v
	}
	// The host's cores and memory each time (docker info is quick); its
	// disk's size with the volumes (slow: every 5 minutes), or now when it's
	// missing.
	if machine, ok := s.readMachine(ctx); ok {
		host["cpus"], host["memory"] = machine[0], machine[1]
	}
	if disk == nil || diskAt.Before(now.Add(-diskEvery)) {
		if fresh, ok := s.readVolumes(ctx); ok {
			disk, diskAt = fresh, now
		}
		if size := s.diskSize(); size > 0 {
			host["disk"] = size
		}
	}
	if host["disk"] == 0 {
		if size := s.diskSize(); size > 0 {
			host["disk"] = size
		}
	}
	rows := map[string]Reading{}
	for _, p := range projects {
		var mine []container
		pattern := containers(p)
		for _, c := range running {
			if pattern.MatchString(c.name) {
				mine = append(mine, c)
			}
		}
		if len(mine) == 0 {
			continue
		}
		r := Reading{SampledAt: now, Host: host}
		cpuLimits, memoryLimits := true, true
		var nanoCPUs, memoryLimit int64
		for _, c := range mine {
			r.CPUCores += c.cpu
			r.MemoryBytes += c.memory
			nanoCPUs += c.nanoCPUs
			memoryLimit += c.memoryLimit
			cpuLimits = cpuLimits && c.nanoCPUs > 0
			memoryLimits = memoryLimits && c.memoryLimit > 0
		}
		if cpuLimits {
			cores := float64(nanoCPUs) / 1e9
			r.CPULimit = &cores
		}
		if memoryLimits {
			r.MemoryLimit = &memoryLimit
		}
		volume := regexp.MustCompile(`^` + regexp.QuoteMeta(p.Name) + `(\.g\d+)?_.`)
		for name, bytes := range disk {
			if volume.MatchString(name) {
				r.DiskBytes += bytes
			}
		}
		rows[p.Name] = r
	}
	s.mu.Lock()
	s.kept = &kept{sampledAt: now, projects: rows, disk: disk, diskAt: diskAt, host: host}
	s.mu.Unlock()
}

func (s *Stats) diskSize() int64 {
	if s.DiskSize != nil {
		return s.DiskSize()
	}
	return RootDisk()
}

// RootDisk is the size of the disk under /, in bytes (0: unknown).
func RootDisk() int64 {
	var fs syscall.Statfs_t
	if syscall.Statfs("/", &fs) != nil {
		return 0
	}
	return int64(fs.Blocks) * fs.Bsize
}

// containers matches p's containers' names: its web containers, its
// accessories in any generation.
func containers(p models.Project) *regexp.Regexp {
	names := []string{regexp.QuoteMeta(p.Name) + `-web-[0-9a-fA-F]{7,}`}
	for _, service := range p.Accessories() {
		names = append(names, regexp.QuoteMeta(p.Name+"-"+service)+`(-g\d+)?`)
	}
	return regexp.MustCompile(`^(` + strings.Join(names, "|") + `)$`)
}

type container struct {
	name                string
	cpu                 float64
	memory, memoryLimit int64
	nanoCPUs            int64
}

// readContainers are the running containers: name, CPU (cores), memory
// used, and their limits (0: none). ok is false when Docker can't say.
func (s *Stats) readContainers(ctx context.Context) ([]container, bool) {
	stats := s.Docker.Run(ctx, []string{"stats", "--no-stream", "--format", "{{json .}}"}, dockercmd.Opts{Timeout: timeout})
	if !stats.OK {
		s.warn("docker stats", stats)
		return nil, false
	}
	type row struct{ ID, Name, CPUPerc, MemUsage string }
	var rows []row
	for _, line := range strings.Split(stats.Output, "\n") {
		var r row
		if json.Unmarshal([]byte(line), &r) == nil {
			rows = append(rows, r)
		}
	}
	if len(rows) == 0 {
		return nil, true
	}
	args := []string{"inspect", "--format", "{{slice .Id 0 12}} {{.HostConfig.Memory}} {{.HostConfig.NanoCpus}}"}
	for _, r := range rows {
		args = append(args, r.ID)
	}
	inspected := s.Docker.Run(ctx, args, dockercmd.Opts{Timeout: timeout})
	if !inspected.OK {
		s.warn("docker inspect", inspected)
		return nil, false
	}
	limits := map[string][2]int64{}
	for _, line := range strings.Split(inspected.Output, "\n") {
		f := strings.Fields(line)
		if len(f) == 3 {
			memory, _ := strconv.ParseInt(f[1], 10, 64)
			cpus, _ := strconv.ParseInt(f[2], 10, 64)
			limits[f[0]] = [2]int64{memory, cpus}
		}
	}
	out := make([]container, 0, len(rows))
	for _, r := range rows {
		limit := limits[r.ID[:min(12, len(r.ID))]]
		cpu, _ := strconv.ParseFloat(strings.TrimSuffix(r.CPUPerc, "%"), 64)
		used, _, _ := strings.Cut(r.MemUsage, "/")
		out = append(out, container{name: r.Name, cpu: cpu / 100, memory: Bytes(used), memoryLimit: limit[0], nanoCPUs: limit[1]})
	}
	return out, true
}

// readVolumes is each volume's size in bytes.
func (s *Stats) readVolumes(ctx context.Context) (map[string]int64, bool) {
	df := s.Docker.Run(ctx, []string{"system", "df", "-v", "--format", "{{json .Volumes}}"}, dockercmd.Opts{Timeout: timeout})
	if !df.OK {
		s.warn("docker system df", df)
		return nil, false
	}
	var volumes []struct{ Name, Size string }
	if err := json.Unmarshal([]byte(df.Output), &volumes); err != nil {
		s.warn("docker system df", dockercmd.Result{Output: err.Error()})
		return nil, false
	}
	out := map[string]int64{}
	for _, v := range volumes {
		out[v.Name] = Bytes(v.Size)
	}
	return out, true
}

// readMachine is the host's cores and memory (docker info).
func (s *Stats) readMachine(ctx context.Context) ([2]int64, bool) {
	info := s.Docker.Run(ctx, []string{"info", "--format", "{{.NCPU}} {{.MemTotal}}"}, dockercmd.Opts{Timeout: timeout})
	if !info.OK {
		s.warn("docker info", info)
		return [2]int64{}, false
	}
	var out [2]int64
	for i, f := range strings.Fields(info.Output) {
		if i < 2 {
			out[i], _ = strconv.ParseInt(f, 10, 64)
		}
	}
	return out, true
}

func (s *Stats) warn(what string, r dockercmd.Result) {
	lines := strings.Split(strings.TrimSpace(r.Output), "\n")
	s.Log.Warn("couldn't read the apps' stats ("+what+")", "err", lines[len(lines)-1])
}

var units = map[string]float64{"B": 1, "kB": 1e3, "KB": 1e3, "MB": 1e6, "GB": 1e9, "TB": 1e12,
	"KiB": 1024, "MiB": 1 << 20, "GiB": 1 << 30, "TiB": 1 << 40}

var size = regexp.MustCompile(`^([\d.]+)\s*([A-Za-z]+)$`)

// Bytes is Docker's size in bytes: "310.2MiB", "48.2MB", "0B" (it writes
// both kinds of unit); 0 when it isn't one.
func Bytes(text string) int64 {
	m := size.FindStringSubmatch(strings.TrimSpace(text))
	if m == nil {
		return 0
	}
	n, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0
	}
	return int64(math.Round(n * units[m[2]]))
}
