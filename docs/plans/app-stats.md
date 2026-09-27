# Plan: each app's CPU, memory and disk on the flight board

## Your direction
- "How hard would it be to show live server stats on each app? memory, data, etc."
- "Displaying an accurate percentage is the most helpful if we can know what the max is. This could reside on the flight board on each app entry row."
- "Without a percentage, it would need to be an amount… when a max is set, it could also show an indicator of percentage used too."
- "If there's an upper limit, it could just be represented with a donut chart so you can see the value and percentage in one."

## Evidence
- Mission Control has the host's Docker socket (install/install.sh, the mission-control service's volumes).
- Container names are Houston's own:
  - an app's web containers are `<project>-web-<40-hex sha>` (Kamal's `<service>-<role>-<version>`, the version being the deployed sha)
  - its accessories are `<project>-<service>`, with `-g<n>` after a restore (`Generation#container`, mission_control/app/models/generation.rb:16)
  - its volumes are `<project>_<name>`, with `.g<n>` after a restore (`Generation#volume`, generation.rb:13)
- `docker stats --no-stream --format '{{json .}}'` gives each running container's `Name`, `CPUPerc` ("3.45%", where 100% is one core) and `MemUsage` ("310.2MiB / 1GiB").
- `docker inspect --format '{{.Name}} {{.HostConfig.Memory}} {{.HostConfig.NanoCpus}}'` gives each container's actual limits, 0 when there's none. Houston applies `deploy.resources.limits` to the app and, since v0.4.2, to its accessories (internal/kamal/kamal.go:539). Reading them from the containers is what Docker actually enforces, and needs no change to the sync (which only carries the app service's limits, internal/mission/request.go:52).
- `docker system df -v --format '{{json .Volumes}}'` gives each volume's size ("48.2MB", decimal units). It's slow, since Docker walks the volumes.
- The flight board refreshes live through `FlightBoard.refresh!`. The cache (Solid Cache in production) survives restarts, as `CloudflareView` uses it (mission_control/app/models/cloudflare_view.rb:23).
- None of the three apps sets limits today, so they'll show amounts, not donuts, until limits are set.

## Design (short)
- **`AppStats.sample!`**, run by `AppStatsJob` every 30 seconds:
  1. One `docker stats --no-stream`, one `docker inspect` of the running containers, and a `docker system df -v` when the last one is over 5 minutes old.
  2. It matches containers and volumes to projects by the names above.
  3. Per project it keeps CPU in cores (the sum of its containers), memory in bytes (the sum), each with a limit only when **every** running container of the project has one (the limits summed), and disk in bytes (its volumes). It also keeps when it sampled.
  4. It saves that in the cache, and refreshes the board only when what it shows changed.
  5. Docker failing or timing out keeps the last readings and logs it. Readings over 2 minutes old show as stale.
- **Never at render time:** the board reads only the cache.
- **The board row:** CPU, MEM and DISK.
  - Each is an amount ("0.03 cores", "310 MB", "1.2 GB").
  - Where there's a limit, it's a small SVG donut: the ring fills to the percentage, with the amount beside it, and a hover says "310 MB of the 1 GB limit (31%)".
  - Disk is always an amount (volumes have no limit).
  - A project with nothing running shows dashes.
  - At 375 px, the three sit under the row, as the row's other details do.
- **CLI-first:** `/api/v1/projects` and `/api/v1/projects/:name` carry `stats` (cpu_cores, cpu_limit, memory_bytes, memory_limit, disk_bytes, sampled_at). `houston status <project>` prints a line: "CPU 0.03 cores · MEM 310 MB of 1 GB (31%) · DISK 1.2 GB".

## AC ↔ test map
| # | Acceptance criterion | Test | Lens |
|---|---|---|---|
| 1 | docker stats and inspect parse into per-project CPU (cores) and memory, web and accessories summed (a restored generation's too); other containers and a look-alike project's are left out | `app_stats_test.rb` `test "containers add up per project"` | Contract |
| 2 | A limit counts only when every running container of the project has one; limits are summed | `test "a limit only when every container has one"` | Contract |
| 3 | Volume sizes add up per project (`<p>_x` and `<p>.g2_x`), in decimal units; disk is re-read only after 5 minutes | `test "disk per project, now and then"` | Scale |
| 4 | Docker failing keeps the last readings and logs it; readings over 2 minutes old are stale | `test "a failed sample keeps what we had"` | Signals |
| 5 | The board refreshes only when what it shows changes | `test "the board refreshes when the numbers change"` | Scale |
| 6 | The job is scheduled every 30 seconds | `app_stats_job_test.rb` | Whole batch |
| 7 | The row: amounts, a donut with its percentage and hover where there's a limit, dashes when nothing runs, "stale" when old | `projects_controller_test.rb` `test "each row shows its app's CPU, memory and disk"` | Contract |
| 8 | The API carries `stats`; `houston status <project>` prints its line | `api_v1_read_test.rb`; Go `TestStatusPrintsStats` | Contract |
| 9 | Visual check, 1280 and 375 px; live on the server: the three apps' figures against `docker stats` there | recorded here | Parity |
| 10 | The gauge fills only as far as the share used: unlit ticks are outlines, one lit per 10% (rounded), and at least one for any use; none at 0% or without a limit | `usage_helper_test.rb` `test "one tick per 10% of the limit, and one for any use"` | Contract |
| 11 | The board shows the amount used, not the limit; the hover keeps the limit ("MEM 1.9 GB of 2 GB (95%)"), and the project's page lists it | `usage_helper_test.rb` `test "amber at 85% and up"`; `projects_controller_test.rb` `test "each row shows its app's CPU, memory and disk"` | Contract |

## Evidence
- **Tests:**
  - Rails: 413 runs, 0 failures; rubocop clean. Go: `bin/go test ./...` passes; gofmt clean.
  - New tests: `app_stats_test.rb` (five cases), `app_stats_job_test.rb`, `projects_controller_test.rb` "each row shows its app's CPU, memory and disk", `api_v1_read_test.rb` "a project's stats", and Go `TestStatusPrintsStats`.
- **Mutation check:** 18, each caught. Among them:
  - a limit when only some containers have one (CPU and memory)
  - the web and restored-generation name rules
  - accessories matched by any name
  - volumes of a restored generation
  - disk read every time
  - a failed CPU and memory read, or a failed disk read, wiping what was kept
  - a board refresh on every sample
  - never stale
  - binary units read as decimal
  - CPU not divided into cores
  - no donut
  - no stats in the API
  - the CLI dropping limits, or printing a line with no stats
  - One survived at first (a failed disk read wiped the disk figures), because the failure test failed the CPU read first. It has its own case now.
- **Visual check** with real Docker on this Mac: containers named as Houston names them, `visual-web-<sha>` (1 GB, 2 CPUs, spinning a core), `visual-db` (512 MB, 1 CPU, a 60 MB volume) and `cart-web-<sha>` (no limits), sampled by `AppStats.sample!`:
  - visual: "1 core of the 3 cores limit (33%)" with a donut, "1.58 MB of the 1.5 GB limit (0%)", and "60 MB" of disk.
  - cart: amounts only.
  - idle (nothing running): a dash.
  - No overflow at 1440, 800 (two columns) and 375 px.
  - To share readings between a runner and the server, the dev cache was switched to a file store for the check and put back.
- **Live on the server:** once this is released and the server updated, the three apps' figures are to be checked against `docker stats` there.

## Revised: tick gauges (your design, 2026-09-26)
- **What you said:** "how can a CPU have 0 cores?", "There's got to be a more efficient way to display this content that's meaningful", then "I prefer tick gauges like in the mockups here", with the Claude Design canvas (its `UsageOptions` artboard).
- **Donuts replaced by the design's tick gauges.** Each figure is one line: label, ten ticks (one per 10% of the limit), the percentage, and the amount against the limit ("351 MB / 512 MB").
  - Unlit ticks are filled (#E2DBC9) where there's a limit, and outlined (the rule colour) where there isn't, so the column stays lined up. Without a limit, the percentage is a faint "—".
  - At 85% and up, the lit ticks and the amount turn amber (#B7791F, #75480A), and the percentage goes bold. The "/ limit" stays grey.
  - Hovers read as the design's: "MEM 351 MB of 512 MB (69%)", "DISK 20 MB · no limit set".
  - Phone sizes come from the `MobileFlightBoard` artboard: 9 px gaps, a 10 px label, and a 14 px amount.
  - Sizes on the board say "0 B", not "0 Bytes", as the CLI does.
- **Tests:** `test/helpers/usage_helper_test.rb` covers ticks per percentage (0, 4, 5, 16, 42, 69, 95, 100, 140% and no limit), amber from 85% (84% isn't), outlined ticks without a limit, and sizes and cores in words. The board test pins the ticks, percentages, amounts and hovers. Rails: 417 runs, 0 failures.
- **Seen locally** with real stand-in containers (shop limited, docs at 90% of 256 MB, blog unlimited, wiki idle), at 1440 and 375 px with no overflow.
- **Not built here:** the `MobileFlightBoard` artboard's labelled phone cards (DOMAINS, RUNNING SHA, LAST DEPLOY, LAST BACKUP, RESOURCES). That's a redesign of the phone board of its own.

## Revised: usage only, and gauges that fill to it (rows 10 and 11, 2026-09-27)
- **Direction:** "resource display is inconsistent. Since we display total resources on the detail page, let's just display usage on the flight board so all resources display the same… instead of: 0 cores / 0.5 cores, we'll just display the graph, 0% 0 cores." Then: "the graphs should somewhat represent the usage. So instead of the graph being 100% full, it should only fill up to the point of the percent in use."
- **Evidence:**
  - valleybuiltcrossfit's lines read "0 cores / 0.5 cores" and "7.88 MB / 384 MB" beside estherpictures' "0 cores" and "170 MB". On a tablet card the resources column grows to fit (`minmax(222px, max-content)`), so the limit squeezed that card's other details into one column while the others had two.
  - Unlit ticks were filled (#E2DBC9), so at 0% and 2% (no tick lit: 2% rounded to 0) the gauge read as full.
  - The project's page already lists the limits (the facts strip's RESOURCES, `resources_words`).
- **As built:**
  - The amount is what's used ("8.58 MB"). The hover keeps the limit and the share ("MEM 8.58 MB of 384 MB (2%)").
  - Every tick is an outline, and a lit one is filled (ink, or amber from 85%). So a gauge fills only as far as the share used.
  - One tick per 10%, rounded, with at least one for any use: 2% lights one, 26% three, 95% all ten. 0% lights none.
  - Without a limit, no tick lights and the percentage is a faint "—", as before. With every tick an outline, that's the same gauge as 0%, and the dash is the difference.
  - `.usage__limit` is gone.
- **Tests first:** the helper tests failed on 1% lighting no tick and on the amount still carrying "/ 2 GB"; the board test on "0.15 cores / 3 cores". All 5 mutations were caught: no tick for small use; `ceil` for `round` (42% → 5); a tick at 0%; the limit back in the amount; the limit gone from the hover.
- **Visual check,** rendered from a throwaway test with the real stylesheet and fonts, measured with Playwright: estherpictures without limits, valleybuiltcrossfit at 0% CPU of 0.5 cores and 2% of 384 MB, and equip at 26% CPU and 95% memory.
  - 1440 px: the table's gauges show 3 ticks at 26%, 10 amber at 95%, 1 at 2%, and none at 0% or without a limit.
  - 664 px: all three cards lay their details out in two columns, with resources 222–225 px wide. Before, the limit made valleybuiltcrossfit's one column.
  - 375 px: the page is 375 px wide.
