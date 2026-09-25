package deploy

import (
	"context"
	"strings"
	"testing"

	"github.com/scttymn/houston/internal/mission"
)

// Secret values never reach the deploy's log in Mission Control, which API
// tokens can read (docs/plans/security-fixes.md, L3).

func sentLog(m *fakeMission) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var b strings.Builder
	for _, r := range m.reports {
		b.WriteString(r.Log)
	}
	return b.String()
}

func TestDeployLogHidesSecretValues(t *testing.T) {
	h := newHarness(t, shopCompose)
	h.mission.secrets["SECRET_KEY_BASE"] = "skb-0123456789"
	h.mission.secrets["POSTGRES_PASSWORD"] = "skb-0123456789-longer"
	h.docker.emit = func(what string) string {
		if what == "release" {
			return "booting with skb-0123456789 and skb-0123456789-longer; true stays\n"
		}
		return ""
	}
	if code := h.run(); code != 0 {
		t.Fatalf("exit %d\n%s", code, h.stderr.String())
	}
	log := sentLog(h.mission)
	if strings.Contains(log, "skb-0123456789") {
		t.Errorf("a secret value reached the log:\n%s", log)
	}
	if !strings.Contains(log, "booting with [secret SECRET_KEY_BASE] and [secret POSTGRES_PASSWORD]; true stays") {
		t.Errorf("the log doesn't say which secret was hidden, or didn't hide the longer one whole:\n%s", log)
	}
}

func TestReporterHidesASecretSplitAcrossReports(t *testing.T) {
	m := &fakeMission{}
	var out strings.Builder
	r := &reporter{ctx: context.Background(), mission: m, out: &out}
	r.hide(map[string]string{"API_KEY": "abcdef123456", "FLAG": "true"})
	r.logf("key abcdef")
	r.send(mission.Progress{})
	r.logf("123456 done\n")
	r.send(mission.Progress{})
	r.logf("tail abcdef123456, true")
	r.finish("go", "")
	if log := sentLog(m); log != "key [secret API_KEY] done\ntail [secret API_KEY], true" {
		t.Errorf("sent %q", log)
	}
}
