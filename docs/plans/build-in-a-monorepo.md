# Plan: an app's build may reach the rest of its git checkout

## Direction
- Mission Control's Go version lives in Houston's module ("right now mission control is in the houston repo. I don't care about structure so whatever you need to do to make it work", 2026-09-29), as an app in a folder of the repo, made by `gantry new --in-module`. Its build needs the module's root (`go.mod` is there), one level above its compose file.

## Evidence
- **The build's boundary is the compose file's folder.** `BuildPaths` (`internal/project/project.go:480`) resolves the context and Dockerfile and refuses anything outside `dir`, the compose file's folder. So an app in `repo/app/` can't build with `context: ..`, though `..` is the same checkout.
- **Why the rule exists:** a repo's symlink mustn't point the build at the runner's files (`TestBuildPathsStayInsideTheCheckout`, `internal/project/load_test.go:660`). Its callers: `houston test` (`internal/cli/test.go:38`) and a deploy's build (`internal/deploy/deploy.go:556`), where `dir` is the runner's clone.

## Design
- The boundary is the git checkout the compose file is in: the nearest folder at or above it with a `.git` (a folder, or a worktree's file). Outside git, it stays the compose file's folder.
- Everything else is as it was: symlinks are resolved and checked against the boundary; a context or Dockerfile outside it builds nothing.
- `houston init` edits only its own folder: the Dockerfile must be there (the context may be above it), and when the context is above, the ignore file it writes is `<Dockerfile>.dockerignore`, beside the Dockerfile.
- On a runner, a deploy's clone has its own `.git`, so its boundary is the clone, as before. Locally, an app in a repo's folder can build from the repo's root.

## AC ↔ test
| # | Acceptance criterion | Test |
|---|---|---|
| 1 | In a git checkout, an app in a subfolder builds with `context: ..` (the checkout's root), and its Dockerfile there | `TestBuildPathsInAMonorepo` |
| 2 | A context or Dockerfile above the checkout, or symlinked out of it, is still refused | `TestBuildPathsInAMonorepo` |
| 3 | Outside git, the boundary is the compose file's folder, as before | `TestBuildPathsInAMonorepo`, `TestBuildPathsStayInsideTheCheckout` |
| 4 | `houston init` completes an app whose context is above its folder (the checkout's root): its Dockerfile, in its folder, and the ignore file beside it (`Dockerfile.dockerignore`, which BuildKit reads), never the root's `.dockerignore` | `TestInit_AnAppInAFolderOfTheRepo` |
| 5 | `houston init` still refuses a Dockerfile outside its folder, changing nothing | `TestInit_DockerfileElsewhereInTheRepo` |

## Evidence
- **Tests first:** `TestBuildPathsInAMonorepo` failed on the parse rule before the change (`services.app.build.context: must be a path inside the repo (relative, without ..), not ".."`), which was a second boundary at the compose file's folder, in the same spirit; both now measure from the checkout's root.
- **Suites:** `bin/go test ./...` ok, gofmt clean. Two init tests matched the old message word for word; they now match the new one ("without .. above its root"), with the same refusals.
- **Mutation check.** All 6 were caught: the parse rule allowing no `..`, or any; the build's boundary back at the compose folder, or the whole disk; one level too many; outside git, the disk's root. (A first run showed the parse rule's refusal of `../..` was only proven through the build's check, so the test asserts the parse refusal directly.)
- **`houston init` (rows 4 and 5):** the first run of `gantry new mission-control-go --in-module` stopped at init (`the app builds from .., outside this folder`), a third check at the folder, on the context as well as the Dockerfile. The test failed for that reason first; now only the Dockerfile must be in the folder. The mutation check caught all 3: the root's `.dockerignore` written, the context checked again, and any Dockerfile allowed.
