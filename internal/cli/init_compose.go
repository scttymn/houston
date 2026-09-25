package cli

import (
	"errors"
	"fmt"
	"strings"

	"go.yaml.in/yaml/v3"
)

// defaultXHouston is the x-houston block `houston init` writes: health, and
// every other key as a commented example of what can be set.
const defaultXHouston = `x-houston:
  health: /
  # app_port: 8080                # production's port, when the production image listens elsewhere
  # domains: [example.com]
  # deploy: { on: commit, branch: main }
  # commands:
  #   console: sh
  #   test: "true"
  # hooks:
  #   release: ./migrate
  # backups: { schedule: "daily 03:00", keep: { auto: 14, deploy: 10 } }
`

// appPortLine is the app_port init adds when Houston can't tell the port
// from `expose` or `ports` (none, or several); indented by the caller.
const appPortLine = "app_port: 8080                # the port your app listens on; change it"

// defaultCompose is the compose.yml `houston init` writes where there's none.
func defaultCompose(name string) string {
	return fmt.Sprintf(`name: %s

services:
  app:
    build: { context: ., target: dev }
    expose: ["8080"]               # the port the app listens on; houston dev serves it at http://%s.localhost
    volumes:
      - .:/app

`, name, name) + defaultXHouston
}

// upsertXHouston adds what Houston requires to an existing compose file: a
// missing x-houston block (appended), or the keys a present block lacks
// (health; app_port when the one built service has no single `ports`
// entry), as lines right after `x-houston:` at the block's indentation, so
// comments and formatting stay. A block written on one line can't take
// lines: it's an error naming what to add.
func upsertXHouston(src string) (content string, added []string, appended bool, err error) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(src), &doc); err != nil || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return src, nil, false, nil // project.Parse says what's wrong with it
	}
	top := doc.Content[0]
	needPort := appNeedsPort(top)

	key, value := entry(top, "x-houston")
	if key == nil {
		block := defaultXHouston
		if needPort {
			block = strings.Replace(block, "  # app_port: 8080                # production's port, when the production image listens elsewhere\n", "  "+appPortLine+"\n", 1)
		}
		eol := "\n"
		if strings.Contains(src, "\r\n") {
			eol, block = "\r\n", strings.ReplaceAll(block, "\n", "\r\n")
		}
		if !strings.HasSuffix(src, "\n") {
			src += eol
		}
		return src + eol + block, nil, true, nil
	}

	var lines []string
	if k, _ := entry(value, "health"); k == nil {
		added, lines = append(added, "health"), append(lines, "health: /")
	}
	if k, _ := entry(value, "app_port"); k == nil && needPort {
		added, lines = append(added, "app_port"), append(lines, appPortLine)
	}
	if len(added) == 0 {
		return src, nil, false, nil
	}
	if value.Kind == yaml.MappingNode && value.Style&yaml.FlowStyle != 0 {
		wants := make([]string, len(lines))
		for i, l := range lines {
			wants[i], _, _ = strings.Cut(l, "   ")
		}
		return src, nil, false, fmt.Errorf("x-houston is written on one line; add %s to it", andList(wants))
	}
	indent := "  "
	switch {
	case value.Kind == yaml.MappingNode && len(value.Content) > 0:
		indent = strings.Repeat(" ", value.Content[0].Column-1)
	case value.Kind == yaml.ScalarNode && value.Tag == "!!null" && value.Value == "":
		// an empty `x-houston:` takes the lines at the default indentation
	default: // ~, null, a string, a list
		return src, nil, false, errors.New("x-houston must be a mapping of keys (health:, domains:, …)")
	}
	cr := ""
	if strings.Contains(src, "\r\n") {
		cr = "\r"
	}

	all := strings.Split(src, "\n")
	at := key.Line // the line after `x-houston:` (Line is 1-based)
	insert := make([]string, len(lines))
	for i, l := range lines {
		insert[i] = indent + l + cr
	}
	all = append(all[:at], append(insert, all[at:]...)...)
	return strings.Join(all, "\n"), added, false, nil
}

// appNeedsPort reports whether the one service with build: has no single
// `expose` or `ports` entry. With no built service, or several, it's false:
// Parse says which is the problem.
func appNeedsPort(top *yaml.Node) bool {
	_, services := entry(top, "services")
	if services == nil || services.Kind != yaml.MappingNode {
		return false
	}
	var built []*yaml.Node
	for i := 0; i+1 < len(services.Content); i += 2 {
		if k, _ := entry(services.Content[i+1], "build"); k != nil {
			built = append(built, services.Content[i+1])
		}
	}
	if len(built) != 1 {
		return false
	}
	one := func(key string) bool {
		_, list := entry(built[0], key)
		return list != nil && list.Kind == yaml.SequenceNode && len(list.Content) == 1
	}
	return !one("expose") && !one("ports")
}

// entry is a mapping's key and value nodes for name, or nils.
func entry(m *yaml.Node, name string) (key, value *yaml.Node) {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil, nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == name {
			return m.Content[i], m.Content[i+1]
		}
	}
	return nil, nil
}
