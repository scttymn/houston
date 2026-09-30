package projects

import (
	"testing"
	"time"

	"github.com/scttymn/houston/mission-control-go/app/models"
)

func TestWords(t *testing.T) {
	for in, want := range map[float64]string{0: "0 cores", 1: "1 core", 0.15: "0.15 cores", 2: "2 cores", 10: "10 cores", 0.003: "0.003 cores", 0.0004: "<0.001 cores", 1.5: "1.5 cores"} {
		if got := CoresWords(in); got != want {
			t.Errorf("CoresWords(%v) = %q", in, got)
		}
	}
	for in, want := range map[int64]string{0: "0 B", 1: "1 B", 512: "512 B", 1300000: "1.24 MB", 1 << 30: "1 GB"} {
		if got := SizeWords(in); got != want {
			t.Errorf("SizeWords(%v) = %q", in, got)
		}
	}
	for _, c := range []struct {
		p     float64
		ok    bool
		words string
		ticks int
	}{{42.4, true, "42%", 4}, {0.04, true, "<1%", 1}, {0, true, "0%", 0}, {0, false, "—", 0}, {2, true, "2%", 1}, {100, true, "100%", 10}, {140, true, "140%", 10}, {0.5, true, "1%", 1}} {
		if w, n := PercentWords(c.p, c.ok), Ticks(c.p, c.ok); w != c.words || n != c.ticks {
			t.Errorf("%v %v = %q %d", c.p, c.ok, w, n)
		}
	}
	for in, want := range map[time.Duration]string{0: "0s", 59 * time.Second: "59s", 108 * time.Second: "1m 48s", 3600 * time.Second: "60m 00s"} {
		if got := DurationWords(in); got != want {
			t.Errorf("DurationWords(%v) = %q", in, got)
		}
	}
	if got := MissionElapsed(52 * time.Second); got != "T+00:52" {
		t.Errorf("MissionElapsed = %q", got)
	}
	if got := MissionElapsed(125 * time.Second); got != "T+02:05" {
		t.Errorf("MissionElapsed = %q", got)
	}
	for _, c := range [][2]string{
		{UsageTitle("CPU", "1 core", "", 0, false, false), "CPU 1 core · no limit set"},
		{UsageTitle("MEM", "1 GB", "16 GB", 6.25, true, true), "MEM 1 GB of the host's 16 GB (6%) · no limit set"},
		{UsageTitle("MEM", "1 GB", "2 GB", 50, true, false), "MEM 1 GB of 2 GB (50%)"},
		{Parameterize("DNS PENDING"), "dns-pending"}, {Parameterize("ZONE NOT IN CLOUDFLARE YET"), "zone-not-in-cloudflare-yet"}, {Parameterize("CAN'T CHECK"), "can-t-check"},
		{RefName("refs/heads/main"), "main"}, {RefName("refs/tags/v1"), "v1"}, {Dasherize("in_flight"), "in-flight"},
		{ServicesWords(models.Project{AppService: "web", Services: models.Names{V: []string{"web"}}}), "no services"},
		{ServicesWords(models.Project{AppService: "web", Services: models.Names{V: []string{"web", "db", "cache"}}, Details: models.ProjectDetails{V: models.Details{Images: map[string]string{"db": "postgres:17"}}}}), "db · postgres:17, cache"},
		{upcaseFirst("no answer"), "No answer"}, {connectionsWords(1), "1 connection to Cloudflare"}, {connectionsWords(4), "4 connections to Cloudflare"},
	} {
		if c[0] != c[1] {
			t.Errorf("%q, want %q", c[0], c[1])
		}
	}
}
