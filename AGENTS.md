# AGENTS.md

For agents working **on Houston's code**. To *use* Houston (set up an app, deploy it), read [docs/agents.md](docs/agents.md) instead.

## What's where
- `cmd/houston`, `internal/`: the Go CLI. It runs locally (`houston dev`, `houston test`) and against a server (`--server`, via Mission Control's API).
- `mission_control/`: Mission Control, the web admin (Rails 8.1, SQLite, Solid Queue, Solid Cable, Turbo, Stimulus).
- `install/`: `install.sh` (the server installer), `runner.Dockerfile`, and `install/test/`, real runs on throwaway OrbStack machines.
- `docs/plans/`: one plan per feature, each with an acceptance-criteria-to-test map and what was found while building it. Read the relevant plan before changing a feature.
- `.github/workflows/release.yml`: a `v*` tag tests, then publishes the images and a GitHub Release.

## Build and test (everything runs in Docker)
```sh
bin/go test ./...                                   # Go tests, in the toolchain container
docker compose --progress quiet run --rm cli gofmt -l internal cmd   # must print nothing
bin/test-integration                                # Go tests that drive real Docker
cd mission_control && docker compose run --rm --no-deps -T -e RAILS_ENV=test app sh -c 'bin/rails test && bin/rubocop'
install/test/install-version.sh                     # the installer's release path, in a container
install/test/install-bind.sh                        # where port 3000 listens, in a container
install/test/install-ssh-key.sh                     # houston's SSH key, in a container
```
- Don't install Go, Ruby or gems on the host.
- `bin/release` builds the CLI for all four platforms into `dist/`, and `bin/install` puts this machine's in `~/.local/bin`.
- If a bind-mounted file seems to be ignored by Rails, add `-e DISABLE_BOOTSNAP=1`: bootsnap caches by mtime and size.

## How changes are made
- **Tests first.** Write the failing tests for the plan's rows, see them fail for the right reason, then implement until they pass. Then break the code on purpose (mutation check) to prove the tests catch it. A behaviour without a test that fails when it breaks isn't done.
- **Plans live in `docs/plans/`.** A plan has the direction it came from, the evidence (file:line), a short design, an acceptance-criteria-to-test map, and afterwards an Evidence section with the commands that proved it.
- **UI changes get a visual check:** render the page, and check phone width (375 px) for overflow.
- **Commits:** plain English, saying what changed and why. Commit messages end with the co-author trailer your tool uses.

## Rules that shape the code
- **No framework knowledge.** Houston knows Docker and Compose, not Rails or Phoenix. `houston init` writes generic defaults to edit; there are no stack presets anywhere.
- **Config is Compose plus an `x-houston` block.** There's no `houston.yml`. The compose file stays valid for plain `docker compose`.
- **CLI-first.** Every Mission Control action has a `houston … --server` path, so agents can drive it. Adding a UI action means adding its CLI command and API route.
- **Git-host agnostic.** A webhook only rings the doorbell; Houston reads the repo's refs itself.
- **Secrets never touch a command line or a log.** They go in on stdin, are stored encrypted, and are written only to their destination. `houston secrets` is write-only.
- **DNS:** Houston changes only records commented `managed-by:houston project:<name>`, and never anything else.
- **Fail closed, and before changing anything.** Installers and destructive commands check everything first. A failed deploy or restore leaves the running version serving.
- **Mission Control never waits on the outside world at render time.** GitHub, Cloudflare and registries are checked by jobs or timeouts, and failures are logged, never raised to the page.

## Releasing
Tag `vX.Y.Z` and push the tag, only when a human asks. The workflow must go green; then a server updates to it (the latest) with:
`curl -fsSL https://github.com/scttymn/houston/releases/latest/download/install.sh | sudo sh`
or, from anywhere, the flight board's Update button or `houston update` (docs/plans/update-from-mission-control.md).
