# Plan: versioned releases (tags → images and binaries), and the version on the flight board

## Your direction
- "We can build images and binaries, I assume when we update the code it will regenerate those."
- "Let's do tags." "We can also display the version on the Flight Board so we know what we're running."
- "It's private for now, but I may open it up."

## What's there (evidence)
- **The installer builds everything on the server** from a checkout (`HOUSTON_SOURCE`, `install/install.sh`):
  - the CLI, from the Dockerfile's `release` stage (`install_cli`)
  - Mission Control (`houston/mission-control:local`) and the runner image (`houston/runner:local`, `install/runner.Dockerfile`)
  - Without `HOUSTON_SOURCE` it stops: "published images come later".
- **The CLI is stamped with a version** at build time (`-X internal/cli.version`, `bin/release` uses `git describe`); `houston --version` prints it. It's "dev" otherwise.
- **Mission Control has no version** anywhere.
- **The repo has no tags and no `.github/`**, so there's no CI.
- **The production server is x86_64.**

## Goal
Pushing a tag like `v0.1.0` builds and publishes Houston at that version:
- Mission Control's and the runner's images on ghcr.io
- the CLI binaries on the GitHub Release
- the installer, also on the Release

A server installs or updates with `HOUSTON_VERSION=v0.1.0`, building nothing. The flight board says which version is running.

## Design (short)
- **`.github/workflows/release.yml`, on `push: tags: ['v*']`:**
  1. **test:** Go tests and gofmt (`internal/…`), and the Rails tests and rubocop (`mission_control/`). Nothing is published unless these pass.
  2. **images:** `ghcr.io/scttymn/houston-mission-control:<tag>` and `ghcr.io/scttymn/houston-runner:<tag>`, built with buildx for **linux/amd64**. Mission Control gets `HOUSTON_VERSION=<tag>` as a build argument, baked in as an environment variable.
  3. **release:** the four CLI binaries (`houston-{linux,darwin}-{amd64,arm64}`, stamped with the tag), `install.sh`, and a `SHA256SUMS` file, on a GitHub Release named after the tag.
  - `GITHUB_TOKEN` alone is enough, so no secrets are needed.
  - **arm64 images later (named):** under emulation, a Rails image takes long and uses up a private repo's Actions minutes, and your server is amd64. When the repo goes public, GitHub's free arm64 runners make arm64 images cheap. The CLI binaries include arm64 now, since Go cross-compiles for free.
- **The installer:**
  - **`HOUSTON_VERSION=v0.1.0`:** pull the two images at that tag and download that Release's CLI binary, checked against `SHA256SUMS`.
  - **`HOUSTON_SOURCE=…`:** builds from a checkout, as today (for development). Mission Control's version is then `source <short sha>`.
  - Exactly one of the two must be set; both, or neither, is refused.
  - **`HOUSTON_GITHUB_TOKEN` (optional):** only while the repo and packages are private. It logs in to ghcr.io and downloads the Release assets, and is needed only during an install or update. When the repo is public, you just leave it out.
  - An unknown version stops before anything changes: "v9.9.9 isn't a Houston release".
- **The version is shown:**
  - **The flight board's eyebrow:** "FLIGHT BOARD · SVNMNS.COM · V0.1.0" (or "· SOURCE 4c4f80a").
  - **CLI-first:** `houston status` (outside a project) prints the server's version first, from a `version` field in `/api/v1/me`.
  - The runners use the host's CLI, as today. The installer checks the CLI against the release's `SHA256SUMS`, which is stronger than comparing version strings.
- **Updating the server then looks like:**
  1. Tag and push (you, or me when you ask).
  2. The workflow goes green.
  3. On the server: `curl` the Release's `install.sh` (with the token while private), then run it with `HOUSTON_VERSION=<tag>`.

## AC ↔ test map
| # | Acceptance criterion | Test | Lens |
|---|---|---|---|
| 1 | Mission Control knows its version: `HOUSTON_VERSION` if set, else `source <sha>` from the image, else `dev` | `test/models/houston_version_test.rb` `test "the version is the release, the source commit, or dev"` | Contract |
| 2 | The flight board shows it in its eyebrow | `projects_controller_test.rb` `test "the flight board shows the version"` | Contract |
| 3 | `/api/v1/me` returns it, and `houston status` prints it first | `api_v1_auth_test.rb` `test "a personal token reaches the remote API"`; Go `TestStatusPrintsServerVersion` | Contract |
| 4 | The installer refuses both or neither of `HOUSTON_VERSION`/`HOUSTON_SOURCE`, a malformed version and an unknown one, before changing anything | `install/test/install-version.sh` (refusals) | Preconditions |
| 5 | With `HOUSTON_VERSION`, the installer pulls the tagged images and installs the Release's CLI, checked against `SHA256SUMS`, with the token when given and without it when not | `install/test/install-version.sh` (a fake Release and registry) | Contract, Authz |
| 6 | A checksum mismatch stops the install, and the old CLI stays | same | Crash & repair |
| 7 | The workflow runs the tests before publishing; failing tests publish nothing | the workflow's `needs:`, and the first real run | Whole batch |
| 8 | A real tag (v0.1.0) publishes both images and the Release; the server updates to it; the flight board says V0.1.0; the runners' CLI says v0.1.0 | the live check, recorded here | Parity |

## Evidence
- **Tests:** Rails 317 runs, 0 failures (rubocop clean); Go all packages pass (gofmt clean); `install/test/install-version.sh` passes. actionlint and shellcheck are clean.
  - The installer test runs `install.sh` as a library (`HOUSTON_INSTALL_LIB=1`) in a Debian container, against a fake GitHub and a stubbed docker. It checks the refusals, the verified CLI download, a request with and without the token, a checksum mismatch keeping the old CLI, and the image step: the token reaches `docker login` only on stdin, both images are pulled at the tag, and it logs out after.
- **Mutation check.** Each of these fails a test:
  - no checksum check
  - HOUSTON_VERSION and HOUSTON_SOURCE both allowed
  - no logout
  - the token as a docker argument
  - any string accepted as a commit
  - no version on the board
  - (in the Go test) no status header
  - At first, "no logout" survived, because the image step had no test. The image checks were added.
- **The design, as built:** images are fetched right after Docker is installed, before anything is written, so a failed pull or build changes nothing. Source builds now stamp the CLI `source-<sha>` and Mission Control `source <sha>`.

## Later (named, not built)
- **arm64 images:** when the repo is public, via GitHub's free arm64 runners.
- **Going public:** flip the repo's and packages' visibility, then drop `HOUSTON_GITHUB_TOKEN` from the update. That's the whole change, since the token is already optional.
- **An install URL** (for example `get.houston.sh`) for `curl | sh`, once it's public.
- **An "update available" note** on the flight board, when a newer tag exists.
