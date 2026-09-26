# Plan: `houston dev` in plain git worktrees

## Your direction
- "Instead of a custom worktree command we use standard git worktrees… maybe we also add a way to give it a subdomain too so we can use standard git worktrees and don't reinvent the wheel."
- Choices made along the way: worktrees go next to the repo, and branches are kept (deleting them is git's business). Then: "plan and build all three", meaning the `.env` fallback, a remembered `--as`, and `houston dev prune`.

## What already works (docs/plans/dev-localhost.md)
- `houston dev` reads the branch from a worktree's `.git` file (`gitBranch`, internal/cli/devname.go:56). A branch runs at `<branch>.<name>.localhost` as its own Compose project, `<name>-<slug>`.
- Its first run copies the main instance's named volumes from this laptop (`copyMainData`, internal/cli/devcopy.go:22).
- `--as <name>` names an instance, whatever the branch.

## Evidence
- A new worktree has no untracked files, so no `.env`. `warnAboutVariables` then warns that required secrets are blank (internal/cli/dev.go:129–158), and Compose interpolates them blank.
- Houston refuses `env_file:` (internal/project/project.go:383), so an app's variables come only from `${VAR}` interpolation. `docker compose --env-file <path>` is all a worktree needs. equip keeps `RAILS_MASTER_KEY` in `.env`.
- Git's layout, checked with real git: a worktree's `.git` is a file, `gitdir: <main>/.git/worktrees/<id>`. That admin directory holds `HEAD`, `commondir` (`../..`, the shared `.git`) and `gitdir` (the worktree's `.git` path). `git worktree remove` deletes the admin directory.
- Nothing removes a branch instance's containers and copied volumes. Deleting a worktree leaves `<name>-<slug>_*` volumes behind.

## Design (short)
1. **`.env` from the main checkout.**
   - When the compose file's folder has no `.env` and the checkout is a linked worktree, Houston uses the main checkout's `.env` at the same relative path.
   - It passes it to Compose with `--env-file`, checks required secrets against it, and says: "houston: no .env here; using the main checkout's (<path>)".
   - A worktree's own `.env` always wins. Nothing is copied.
2. **`--as` remembered per checkout.**
   - `houston dev --as login` saves `login` in the checkout's own git admin directory (`<admin>/houston-dev-name`), and says so. Later, plain `houston dev` there uses it.
   - `houston dev --as=` forgets it and goes back to the branch's name.
   - An explicit `--as` wins over the saved one. Other worktrees and the main checkout are unaffected.
   - The file goes when git removes the worktree. It's not in the working tree, so it's never committed.
3. **`houston dev prune`.**
   - Every branch instance `houston dev` starts is recorded in the repo's shared git directory (`<common>/houston-dev-instances.json`): its Compose project, its app, its address and its folder. The main instance isn't recorded.
   - `prune` keeps each instance some current worktree would run now (its remembered name, else its branch), in both dev and `--production` form. It lists the rest with their volumes' size.
   - It removes their containers, volumes and networks, found by Compose's project label, then drops the records.
   - A running one is skipped and named. It never touches the main instance, or anything not recorded.
   - It asks first: `y` on a terminal, or `--yes`. Without a terminal and without `--yes`, it refuses and removes nothing.

## AC ↔ test map
| # | Acceptance criterion | Test | Lens |
|---|---|---|---|
| 1 | In a worktree without `.env`: main's `.env` goes to Compose (`--env-file`), its values satisfy the required-secret check, and it says so. A worktree's own `.env` wins. A non-worktree without `.env` warns as before | `dev_worktree_test.go` `TestDevWorktreeUsesMainsEnv` | Contract |
| 2 | `--as` saves the name in the checkout's admin directory, and a plain run uses it. `--as=` forgets it. An explicit `--as` wins. Another worktree and main are unaffected | `TestDevRemembersAs` | Contract, Re-entry |
| 3 | A branch instance run is recorded (project, app, host, folder); main isn't; a second run keeps one record | `TestDevRecordsInstances` | Contract |
| 4 | prune removes a stale instance's containers, volumes and networks, then its record | `TestDevPrune` "a removed worktree's instance goes" | Contract |
| 5 | prune keeps what current worktrees run (branch, remembered name, `--production`), and never main or another app's records | `TestDevPrune` "what a worktree runs stays" | Preconditions |
| 6 | A running stale instance is skipped and named | `TestDevPrune` "a running one is skipped" | Concurrency |
| 7 | It asks first: `y` removes, anything else keeps; no terminal and no `--yes` refuses with exit 2 and removes nothing; nothing stale says so | `TestDevPrune` "it asks first" | Authz |
| 8 | Real Docker: a pruned branch's volumes are gone, and main's are intact | `dev_localhost_integration_test.go` `TestDevPruneIntegration` | Parity |
| 9 | Live on this Mac: `git worktree add`, then `houston dev --as` in the worktree (main's `.env`, a copy of main's data), then `git worktree remove` and `houston dev prune` | recorded here | Parity |

## Boundary
- Instances started before this change aren't recorded, so prune doesn't know them. None exist on this laptop now (the earlier branch copies were removed by hand).

## Evidence
- **Tests:**
  - `bin/go test ./...` and `bin/test-integration` pass, and gofmt is clean.
  - New tests: `internal/cli/dev_worktree_test.go` (`TestDevWorktreeUsesMainsEnv`, `TestDevRemembersAs`, `TestDevRecordsInstances`, `TestDevPrune` with four cases), `TestDevPruneIntegration` (real Docker), and the "production" refusal in `TestDevInstanceName`.
- **Mutation check:** 20, each caught once three survivors were dealt with:
  - The main-checkout test in the `.env` fallback was redundant (the path it would pick is the missing `.env` itself), so it was cut.
  - Protecting main's own projects in prune survived because main's checkout already counts as live. But a branch named `production` would be `<name>-production`, the main instance's `--production` project. So the guard stays, tested with main's checkout on another branch, and `houston dev` now refuses to name an instance `production`.
  - Skipping a worktree deleted by hand (git's entry left behind, with a remembered name) wasn't tested. Now it is.
- **Live on this Mac (2026-09-26),** with real git and Docker, on a throwaway copy of equip renamed houston-equip-test (your equip untouched):
  - `houston dev` on main answered 200 at houston-equip-test.localhost.
  - `git worktree add ../equip-login -b feature/login` made a worktree with no `.env`.
  - `houston dev --as login` there: "no .env here; using the main checkout's", "this checkout is login.houston-equip-test.localhost from now on", "copying houston-equip-test's data", and 200.
  - A plain `houston dev` there reused `login`, with no second copy.
  - `git worktree remove`, then `houston dev prune --yes`: it listed `login.houston-equip-test.localhost (houston-equip-test-login_storage 40.96kB)`, "removed …". Main's volume stayed, `feature/login` stayed, and a second prune found nothing.
  - Found there: with no records left, the file read `null`. It's `[]` now, with a test.
