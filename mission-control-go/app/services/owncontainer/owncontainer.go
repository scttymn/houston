// Package owncontainer is Mission Control's own container, as Docker sees
// it (the Rails app's OwnContainer). The installer starts it with compose,
// whose labels say where its compose.yml and directory are; without them,
// it wasn't started by the installer, and Mission Control can't restart or
// update itself (the port switch, the server update).
package owncontainer

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/scttymn/houston/mission-control-go/app/services/dockercmd"
)

// The compose labels it reads.
const (
	ConfigFiles = "com.docker.compose.project.config_files"
	WorkingDir  = "com.docker.compose.project.working_dir"
)

// Own is this container: its name on Docker is its hostname.
type Own struct {
	Docker   dockercmd.Runner
	Hostname string
}

// Labels are its labels; none when Docker can't say.
func (o Own) Labels(ctx context.Context) map[string]string {
	ran := o.Docker.Run(ctx, []string{"inspect", "--format", "{{json .Config.Labels}}", o.Hostname}, dockercmd.Opts{Timeout: 5 * time.Second})
	labels := map[string]string{}
	if ran.OK {
		json.Unmarshal([]byte(ran.Output), &labels)
	}
	return labels
}

// ComposeFile is the compose.yml it was started from, and its directory:
// "" when it wasn't started by the installer.
func (o Own) ComposeFile(ctx context.Context) (file, dir string) {
	labels := o.Labels(ctx)
	file, _, _ = strings.Cut(labels[ConfigFiles], ",")
	return file, labels[WorkingDir]
}

// PortAddress is what its port is bound to: "0.0.0.0" (every interface),
// "127.0.0.1", another address, or "" when Docker can't say.
func (o Own) PortAddress(ctx context.Context) string {
	ran := o.Docker.Run(ctx, []string{"inspect", "--format", "{{json .HostConfig.PortBindings}}", o.Hostname}, dockercmd.Opts{Timeout: 5 * time.Second})
	if !ran.OK {
		return ""
	}
	var bindings map[string][]struct {
		HostIP string `json:"HostIp"`
	}
	if json.Unmarshal([]byte(ran.Output), &bindings) != nil {
		return ""
	}
	for _, list := range bindings {
		for _, b := range list {
			switch b.HostIP {
			case "", "0.0.0.0", "::":
				return "0.0.0.0"
			}
			return b.HostIP
		}
	}
	return ""
}
