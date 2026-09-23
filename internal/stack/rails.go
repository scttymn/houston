// Package stack holds what Houston knows about specific frameworks. It's used
// only by `houston init`; everything else is stack-agnostic.
package stack

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// errNotRails means dir isn't a Rails app.
var errNotRails = errors.New("Houston can set up Rails apps for now; other stacks are coming")

var pgGem = regexp.MustCompile(`(?m)^\s*gem\s+["']pg["']`)

// DetectRails checks that dir is a Rails app Houston can set up.
func DetectRails(dir string) error {
	gemfile, err := os.ReadFile(filepath.Join(dir, "Gemfile"))
	if err != nil {
		return errNotRails
	}
	if _, err := os.Stat(filepath.Join(dir, "config", "application.rb")); err != nil {
		return errNotRails
	}
	if pgGem.Match(gemfile) {
		return errors.New("Rails with Postgres isn't supported by `houston init` yet; SQLite apps are")
	}
	return nil
}

var fromRE = regexp.MustCompile(`(?i)^\s*FROM\s+(\S+)(?:\s+AS\s+(\S+))?\s*$`)

type stage struct {
	line int    // index of the FROM line
	name string // lowercased; "" when unnamed
}

// DockerfileResult is Rails' Dockerfile with Houston's stages added.
type DockerfileResult struct {
	Content     string
	AddedStages bool   // dev and test were inserted
	NamedFinal  bool   // the final stage was named production
	Workdir     string // the base stage's WORKDIR
}

// RailsDockerfile adds dev and test stages after Rails' base stage and names
// the final stage production, keeping every other line as it was. The dev
// stage reuses the build stage's apt-get and Gemfile COPY lines, so
// app-specific packages come along.
func RailsDockerfile(src string) (DockerfileResult, error) {
	lines := strings.Split(src, "\n")
	var stages []stage
	for i, l := range lines {
		if m := fromRE.FindStringSubmatch(l); m != nil {
			stages = append(stages, stage{line: i, name: strings.ToLower(m[2])})
		}
	}
	find := func(name string) int {
		for i, s := range stages {
			if s.name == name {
				return i
			}
		}
		return -1
	}
	base := find("base")
	if base < 0 {
		return DockerfileResult{}, errors.New("the Dockerfile has no `base` stage (Rails' generated Dockerfile has one); Houston adds its dev and test stages on top of it")
	}
	end := func(i int) int { // first line after stage i
		if i+1 < len(stages) {
			return stages[i+1].line
		}
		return len(lines)
	}
	res := DockerfileResult{Workdir: "/rails"}
	for _, l := range lines[stages[base].line:end(base)] {
		if f := strings.Fields(l); len(f) == 2 && strings.EqualFold(f[0], "WORKDIR") {
			res.Workdir = f[1]
		}
	}

	hasDev, hasTest := find("dev") >= 0, find("test") >= 0
	if hasDev != hasTest {
		return DockerfileResult{}, errors.New("the Dockerfile has only one of the dev and test stages; add the other by hand or remove it")
	}
	final := stages[len(stages)-1]
	switch final.name {
	case "production":
	case "":
		lines[final.line] = strings.TrimRight(lines[final.line], " \t") + " AS production"
		res.NamedFinal = true
	default:
		return DockerfileResult{}, fmt.Errorf("the final Dockerfile stage is named `%s`; Houston deploys the stage named `production`, so rename it", final.name)
	}

	if !hasDev {
		if base == len(stages)-1 {
			return DockerfileResult{}, errors.New("the Dockerfile's `base` stage is its last one; Houston expects Rails' build and final stages after it")
		}
		at := stages[base+1].line
		for at > 0 && strings.HasPrefix(strings.TrimSpace(lines[at-1]), "#") {
			at-- // keep the next stage's comment with it
		}
		apt, copies := buildStageLines(lines, stages, find("build"), end)
		block := devTestStages(apt, copies)
		lines = append(lines[:at], append(block, lines[at:]...)...)
		res.AddedStages = true
	}
	res.Content = strings.Join(lines, "\n")
	return res, nil
}

// buildStageLines returns the build stage's apt-get RUN (with continuation
// lines) and its COPY lines before `bundle install`, or Rails defaults.
func buildStageLines(lines []string, stages []stage, build int, end func(int) int) (apt, copies []string) {
	apt = []string{
		`RUN apt-get update -qq && \`,
		`    apt-get install --no-install-recommends -y build-essential git libyaml-dev pkg-config && \`,
		`    rm -rf /var/lib/apt/lists /var/cache/apt/archives`,
	}
	copies = []string{"COPY Gemfile Gemfile.lock ./"}
	if build < 0 {
		return apt, copies
	}
	var foundApt []string
	var foundCopies []string
	body := lines[stages[build].line+1 : end(build)]
	for i := 0; i < len(body); i++ {
		trimmed := strings.TrimSpace(body[i])
		switch {
		case strings.HasPrefix(trimmed, "RUN"):
			block := []string{body[i]}
			for strings.HasSuffix(strings.TrimSpace(body[i]), `\`) && i+1 < len(body) {
				i++
				block = append(block, body[i])
			}
			joined := strings.Join(block, "\n")
			if strings.Contains(joined, "bundle install") {
				if foundApt != nil {
					apt = foundApt
				}
				if foundCopies != nil {
					copies = foundCopies
				}
				return apt, copies
			}
			if foundApt == nil && strings.Contains(joined, "apt-get install") {
				foundApt = block
			}
		case strings.HasPrefix(trimmed, "COPY") && trimmed != "COPY . .":
			foundCopies = append(foundCopies, body[i])
		}
	}
	if foundApt != nil {
		apt = foundApt
	}
	if foundCopies != nil {
		copies = foundCopies
	}
	return apt, copies
}

func devTestStages(apt, copies []string) []string {
	block := []string{
		"# Development and test stages, added by houston init.",
		"FROM base AS dev",
		`ENV RAILS_ENV="development" \`,
		`    BUNDLE_DEPLOYMENT="0" \`,
		`    BUNDLE_WITHOUT=""`,
	}
	block = append(block, apt...)
	block = append(block, copies...)
	return append(block,
		"RUN bundle install",
		`CMD ["sh", "-c", "bin/rails db:prepare && exec bin/rails server -b 0.0.0.0 -p 3000 -P /tmp/server.pid"]`,
		"",
		"FROM dev AS test",
		`ENV RAILS_ENV="test"`,
		"COPY . .",
		"",
	)
}

// RailsXHouston is the x-houston block for a Rails app.
const RailsXHouston = `x-houston:
  health: /up
  commands:
    console: bin/rails console
    test: bin/rails test
  hooks:
    release: bin/rails db:migrate
`

// RailsCompose is a new compose.yml for a Rails + SQLite app.
func RailsCompose(name, workdir string) string {
	return fmt.Sprintf(`name: %s

services:
  app:
    build: { context: ., target: dev }
    ports: ["3000:3000"]
    environment:
      # Blank in dev: Rails then reads config/master.key. Set it on the server.
      RAILS_MASTER_KEY: ${RAILS_MASTER_KEY:-}
    volumes:
      - .:%s
      - storage:%s/storage

volumes:
  storage:

`, name, workdir, workdir) + RailsXHouston
}
