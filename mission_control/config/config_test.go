package config_test

import (
	"testing"

	"github.com/scttymn/houston/mission_control/config"
)

func TestDefaults(t *testing.T) {
	if c := config.Load(func(string) string { return "" }); !c.JobsInServer || c.MissionControlURL != "http://mission-control:80" {
		t.Errorf("by default: %+v", c)
	}
}
