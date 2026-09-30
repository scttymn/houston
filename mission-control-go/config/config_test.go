package config_test

import (
	"testing"

	"github.com/scttymn/houston/mission-control-go/config"
)

func TestShadow(t *testing.T) {
	env := map[string]string{"MISSION_CONTROL_SHADOW": "1"}
	if c := config.Load(func(k string) string { return env[k] }); !c.Shadow || c.JobsInServer {
		t.Errorf("a preview: shadow %v, jobs %v", c.Shadow, c.JobsInServer)
	}
	if c := config.Load(func(string) string { return "" }); c.Shadow || !c.JobsInServer || c.MissionControlURL != "http://mission-control:80" {
		t.Errorf("by default: %+v", c)
	}
}
