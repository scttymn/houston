package appstats

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/scttymn/houston/mission-control-go/app/models"
	"github.com/scttymn/houston/mission-control-go/app/services/dockercmd"
	"github.com/scttymn/houston/mission-control-go/app/services/dockercmd/dockercmdtest"
)

const (
	gib = 1 << 30
	mib = 1 << 20
)

var stats = func() string {
	var lines []string
	for _, r := range [][4]string{
		{"w1", "equip-web-" + strings.Repeat("a", 40), "12.50%", "300MiB / 1GiB"},
		{"d1", "equip-db", "2.00%", "100MiB / 512MiB"},
		{"d2", "equip-db-g2", "1.00%", "50MiB / 125.7GiB"},
		{"c1", "cart-web-" + strings.Repeat("b", 40), "3.00%", "12MiB / 125.7GiB"},
		// Not an app's: Houston's own, the proxy, a look-alike and an unknown service.
		{"m1", "houston-mission-control-1", "50.00%", "900MiB / 125.7GiB"},
		{"k1", "kamal-proxy", "1.00%", "20MiB / 125.7GiB"},
		{"x1", "equip-web-web-" + strings.Repeat("c", 40), "9.00%", "9MiB / 125.7GiB"},
		{"x2", "equip-redis", "9.00%", "9MiB / 125.7GiB"},
	} {
		b, _ := json.Marshal(map[string]string{"ID": r[0], "Name": r[1], "CPUPerc": r[2], "MemUsage": r[3]})
		lines = append(lines, string(b))
	}
	return strings.Join(lines, "\n")
}()

const df = `[{"Name":"equip_storage","Size":"48.2MB"},{"Name":"equip.g2_pgdata","Size":"1.5GB"},{"Name":"equipment_x","Size":"9GB"},{"Name":"cart_data","Size":"0B"}]`

var projects = []models.Project{
	{Name: "equip", AppService: "app", Services: models.Names{V: []string{"app", "db"}}},
	{Name: "cart", AppService: "web", Services: models.Names{V: []string{"web"}}},
	{Name: "idle", AppService: "web", Services: models.Names{V: []string{"web"}}},
}

// docker answers: limits are each container's memory and nano CPUs; a
// command's answer replaced in answers ("stats", "system", "info").
func docker(limits map[string][2]int64, answers map[string]dockercmd.Result) *dockercmdtest.Fake {
	fake := &dockercmdtest.Fake{}
	for cmd, r := range answers {
		fake.On(r, cmd)
	}
	var inspect []string
	for _, id := range []string{"w1", "d1", "d2", "c1", "m1", "k1", "x1", "x2"} {
		inspect = append(inspect, fmt.Sprintf("%s %d %d", id, limits[id][0], limits[id][1]))
	}
	fake.On(dockercmdtest.OK(stats), "stats")
	fake.On(dockercmdtest.OK(strings.Join(inspect, "\n")), "inspect")
	fake.On(dockercmdtest.OK(df), "system")
	fake.On(dockercmdtest.OK(fmt.Sprintf("8 %d", 16*gib)), "info")
	return fake
}

func newStats(disk int64) (*Stats, *bytes.Buffer) {
	logs := &bytes.Buffer{}
	return &Stats{DiskSize: func() int64 { return disk }, Log: slog.New(slog.NewTextHandler(logs, nil))}, logs
}

func sample(s *Stats, fake *dockercmdtest.Fake, now time.Time) int {
	s.Docker = fake
	s.Sample(context.Background(), projects, now)
	systems := 0
	for _, c := range fake.Calls() {
		if c.Args[0] == "system" {
			systems++
		}
	}
	return systems
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-4 }

func TestContainersAddUp(t *testing.T) {
	s, _ := newStats(100 * gib)
	sample(s, docker(nil, nil), time.Now())
	equip := s.For("equip")
	// web 12.5% + db 2% + the restored db 1%, of one core each.
	if !near(equip.CPUCores, 0.155) || equip.MemoryBytes != 450*mib {
		t.Errorf("equip %+v", equip)
	}
	if cart := s.For("cart"); !near(cart.CPUCores, 0.03) || cart.MemoryBytes != 12*mib {
		t.Errorf("cart %+v", cart)
	}
	if s.For("idle") != nil { // nothing running, nothing read
		t.Error("idle read")
	}
}

func TestLimits(t *testing.T) {
	s, _ := newStats(100 * gib)
	some := map[string][2]int64{"w1": {gib, 2_000_000_000}, "d1": {512 * mib, 1_000_000_000}} // the restored db has none
	sample(s, docker(some, nil), time.Now())
	if r := s.For("equip"); r.MemoryLimit != nil || r.CPULimit != nil {
		t.Errorf("some: %+v", r)
	}
	sample(s, docker(map[string][2]int64{"d2": {gib, 2_000_000_000}}, nil), time.Now()) // the last only
	if r := s.For("equip"); r.MemoryLimit != nil || r.CPULimit != nil {
		t.Errorf("the last only: %+v", r)
	}
	some["d2"] = [2]int64{256 * mib, 500_000_000}
	sample(s, docker(some, nil), time.Now())
	if r := s.For("equip"); r.MemoryLimit == nil || *r.MemoryLimit != gib+768*mib || r.CPULimit == nil || !near(*r.CPULimit, 3.5) {
		t.Errorf("all: %+v", r)
	}
	if r := s.For("cart"); r.MemoryLimit != nil {
		t.Errorf("cart %+v", r)
	}
}

// Without a limit, a share is of the host's cores, memory and disk; a limit
// wins when every container has one.
func TestShares(t *testing.T) {
	s, _ := newStats(100 * gib)
	now := time.Now()
	sample(s, docker(map[string][2]int64{"c1": {64 * mib, 500_000_000}}, nil), now)
	equip := s.For("equip")
	cpu, _ := equip.CPUPercent()
	memory, _ := equip.MemoryPercent()
	disk, _ := equip.DiskPercent()
	if !equip.CPUOfHost() || equip.CPUWhole() != 8 || !near(cpu, 100*0.155/8) || !equip.MemoryOfHost() ||
		!near(memory, 100.0*450*mib/(16*gib)) || !near(disk, 100.0*(48_200_000+1_500_000_000)/(100*gib)) {
		t.Errorf("equip %+v", equip)
	}
	cart := s.For("cart")
	cpu, _ = cart.CPUPercent()
	memory, _ = cart.MemoryPercent()
	if cart.CPUOfHost() || cart.MemoryOfHost() || !near(cpu, 100*0.03/0.5) || !near(memory, 100.0*12/64) { // its limit, not the host
		t.Errorf("cart %+v", cart)
	}

	// The host's cores and memory are read each time; when Docker can't
	// say, what was read stays, and before anything was, no share.
	sample(s, docker(nil, map[string]dockercmd.Result{"info": dockercmdtest.OK(fmt.Sprintf("16 %d", 32*gib))}), now.Add(30*time.Second))
	if w := s.For("equip").CPUWhole(); w != 16 {
		t.Errorf("each sample: %v", w)
	}
	sample(s, docker(nil, map[string]dockercmd.Result{"info": dockercmdtest.Fail(1, "no")}), now.Add(time.Minute))
	if w := s.For("equip").CPUWhole(); w != 16 {
		t.Errorf("kept: %v", w)
	}
	s.kept = nil
	sample(s, docker(nil, map[string]dockercmd.Result{"info": dockercmdtest.Fail(1, "no")}), now.Add(2*time.Minute))
	if _, ok := s.For("equip").CPUPercent(); ok {
		t.Error("a share of nothing")
	}
}

// The disk's size is read with the volumes, every 5 minutes, or at once
// when a reading has none.
func TestHostDisk(t *testing.T) {
	s, _ := newStats(100 * gib)
	now := time.Now()
	sample(s, docker(nil, nil), now)
	s.kept.host = map[string]int64{}
	s.DiskSize = func() int64 { return 200 * gib }
	if n := sample(s, docker(nil, nil), now.Add(time.Second)); n != 0 {
		t.Errorf("the volumes read: %d", n)
	}
	if w := s.For("equip").DiskWhole(); w != 200*gib { // the missing size is read now
		t.Errorf("= %v", w)
	}
	s.DiskSize = func() int64 { return 300 * gib }
	sample(s, docker(nil, nil), now.Add(time.Minute))
	if w := s.For("equip").DiskWhole(); w != 200*gib { // kept until the volumes are read again
		t.Errorf("= %v", w)
	}
	sample(s, docker(nil, nil), now.Add(6*time.Minute))
	if w := s.For("equip").DiskWhole(); w != 300*gib {
		t.Errorf("= %v", w)
	}
}

func TestDisk(t *testing.T) {
	s, _ := newStats(100 * gib)
	now := time.Now()
	if n := sample(s, docker(nil, nil), now); n != 1 || s.For("equip").DiskBytes != 48_200_000+1_500_000_000 || s.For("cart").DiskBytes != 0 {
		t.Errorf("%d reads: %+v", n, s.For("equip"))
	}
	empty := map[string]dockercmd.Result{"system": dockercmdtest.OK("[]")}
	if n := sample(s, docker(nil, empty), now.Add(4*time.Minute)); n != 0 || s.For("equip").DiskBytes != 48_200_000+1_500_000_000 {
		t.Errorf("within 5 minutes: %d reads, %+v", n, s.For("equip"))
	}
	if n := sample(s, docker(nil, empty), now.Add(6*time.Minute)); n != 1 || s.For("equip").DiskBytes != 0 {
		t.Errorf("after: %d reads, %+v", n, s.For("equip"))
	}
}

// A failed sample keeps what was read.
func TestFailedSample(t *testing.T) {
	s, logs := newStats(100 * gib)
	now := time.Now()
	sample(s, docker(nil, nil), now)
	sample(s, docker(nil, map[string]dockercmd.Result{"stats": dockercmdtest.Fail(1, "Cannot connect to the Docker daemon"), "system": dockercmdtest.Fail(1, "no")}),
		now.Add(time.Minute))
	if r := s.For("equip"); r.MemoryBytes != 450*mib || r.Stale(now.Add(time.Minute)) || !strings.Contains(logs.String(), "couldn't read the apps' stats (docker stats)") {
		t.Errorf("%+v %s", r, logs)
	}
	// Past 5 minutes, CPU and memory read fine and disk doesn't: disk keeps
	// what it had.
	sample(s, docker(nil, map[string]dockercmd.Result{"system": dockercmdtest.Fail(1, "timed out")}), now.Add(6*time.Minute))
	if r := s.For("equip"); r.DiskBytes != 48_200_000+1_500_000_000 || !r.Stale(now.Add(9*time.Minute)) {
		t.Errorf("%+v", r)
	}
}

// Fresh reads Docker only when the reading is over 25 seconds old, one
// read at a time.
func TestFresh(t *testing.T) {
	s, _ := newStats(100 * gib)
	now := time.Now()
	reads := func(at time.Time) int {
		fake := docker(nil, nil)
		s.Docker = fake
		s.Fresh(context.Background(), projects, at)
		n := 0
		for _, c := range fake.Calls() {
			if c.Args[0] == "stats" {
				n++
			}
		}
		return n
	}
	if n := reads(now); n != 1 {
		t.Errorf("nothing read yet: %d", n)
	}
	if n := reads(now.Add(20 * time.Second)); n != 0 {
		t.Errorf("within 25 seconds: %d", n)
	}
	if n := reads(now.Add(30 * time.Second)); n != 1 {
		t.Errorf("after: %d", n)
	}
	s.sampling.Lock()
	defer s.sampling.Unlock()
	if n := reads(now.Add(time.Minute)); n != 0 { // another is reading: it doesn't wait for it
		t.Errorf("while another reads: %d", n)
	}
}

func TestBytes(t *testing.T) {
	for text, want := range map[string]int64{"310.2MiB": 325268275, "48.2MB": 48_200_000, "0B": 0, " 1GiB ": gib, "12 kB": 12_000, "nope": 0, "1.5XB": 0} {
		if got := Bytes(text); got != want {
			t.Errorf("%q = %d", text, got)
		}
	}
}

func TestReadingJSON(t *testing.T) {
	limit := int64(64 * mib)
	b, _ := json.Marshal(Reading{CPUCores: 0.123456, MemoryBytes: 12, MemoryLimit: &limit, DiskBytes: 3, SampledAt: time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)})
	if string(b) != `{"cpu_cores":0.1235,"cpu_limit":null,"disk_bytes":3,"memory_bytes":12,"memory_limit":67108864,"sampled_at":"2026-09-30T10:00:00Z"}` {
		t.Errorf("= %s", b)
	}
}
