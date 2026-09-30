package owncontainer_test

import (
	"context"
	"testing"

	"github.com/scttymn/houston/mission-control-go/app/services/dockercmd/dockercmdtest"
	"github.com/scttymn/houston/mission-control-go/app/services/owncontainer"
)

func TestPortAddress(t *testing.T) {
	for bindings, want := range map[string]string{
		`{"80/tcp":[{"HostIp":"0.0.0.0","HostPort":"3000"}]}`:   "0.0.0.0",
		`{"80/tcp":[{"HostIp":"","HostPort":"3000"}]}`:          "0.0.0.0",
		`{"80/tcp":[{"HostIp":"::","HostPort":"3000"}]}`:        "0.0.0.0",
		`{"80/tcp":[{"HostIp":"127.0.0.1","HostPort":"3000"}]}`: "127.0.0.1",
		`{"80/tcp":[{"HostIp":"10.0.0.5","HostPort":"3000"}]}`:  "10.0.0.5",
		`{}`:   "",
		`null`: "",
		`nope`: "",
	} {
		fake := &dockercmdtest.Fake{}
		fake.On(dockercmdtest.OK(bindings), "inspect", "--format", "{{json .HostConfig.PortBindings}}", "mc")
		if got := (owncontainer.Own{Docker: fake, Hostname: "mc"}).PortAddress(context.Background()); got != want {
			t.Errorf("%s = %q", bindings, got)
		}
	}
	fake := &dockercmdtest.Fake{}
	fake.On(dockercmdtest.Fail(1, "Cannot connect"), "inspect")
	if got := (owncontainer.Own{Docker: fake, Hostname: "mc"}).PortAddress(context.Background()); got != "" {
		t.Errorf("Docker down = %q", got)
	}
}

func TestComposeFile(t *testing.T) {
	fake := &dockercmdtest.Fake{}
	fake.On(dockercmdtest.OK(`{"com.docker.compose.project.config_files":"/opt/houston/compose.yml,/opt/houston/override.yml","com.docker.compose.project.working_dir":"/opt/houston"}`),
		"inspect", "--format", "{{json .Config.Labels}}", "mc")
	if file, dir := (owncontainer.Own{Docker: fake, Hostname: "mc"}).ComposeFile(context.Background()); file != "/opt/houston/compose.yml" || dir != "/opt/houston" {
		t.Errorf("= %q %q", file, dir)
	}
}
