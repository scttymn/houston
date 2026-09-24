# Plan: Mission Control matches the design

## Your direction
- "This page is a complete mess as well. You did not follow the mockup at all" (the project page, with the design's ProjectDetail next to it).
- "That ui is completely styled wrong though. It should look like the rest." (Add project's volumes step.)
- Standing rule: the design wins over the spec when they conflict.

## Goal
Every Mission Control screen follows its artboard in the design canvas (https://claude.ai/artifact/UswdZ62N4ygiKdGh1vDEPH, `project/*.dc.html`): the same sections in the same places, the same hierarchy and components, and no horizontal scroll. Where the design predates a later decision, the decision wins and the plan names it (below).

## How it drifted (evidence)
- The views were built feature by feature, and each new feature added a `panel` to whichever column had room. Nothing compared a screen against its artboard, and the tests check content, never layout. So the project page has every feature and none of the design's layout.
- The Turbo bug found today (forms that answer in place showed nothing) came from the same gap: request tests never ran a browser.

## Where the design is older than a decision (the decision wins)
- `houston.yml` → `compose.yml` with an `x-houston` block. For example, "Edit in houston.yml" becomes "Edit in compose.yml", and "No houston.yml yet?" becomes "No x-houston block yet?".
- The header's avatar ("SM") stays **Sign out** until there are several users.

## Batch 1: the project page (fully specified)

Layout, from `ProjectDetail.dc.html`:
1. **Header:**
   - on the left: the name, with the status chip beside it; below them, one chip per hostname (the state dot, the host, the DNS state, and ↗)
   - on the right: the console box ("CONSOLE · RUNS `<commands.console>`", `houston console --server`, and Copy)
   - **Create Snapshot** (was Back up now) moved into the Snapshots panel's head, with the last backup run under it ("Let's move the back up now button into the snapshots table. And change the text to \"Create Snapshot\""). The head renders with the page; only the list is the lazy frame, so the button never waits on reading the repository. Everywhere "Back up now" named that action (the snapshot's note, the schedule's words, `houston snapshots`) now says Create Snapshot.
2. **Notices** (maintenance, OK, NO-GO), full width, as now.
3. **The facts strip:** one full-width bordered strip of five cells:
   - RUNNING SHA
   - DEPLOY RULE
   - RESOURCES (cpus and memory from x-houston, or "no limits")
   - SERVICES · n (one line per service: dot, name, image)
   - REPO (the host and `owner/repo` from the URL, or "not linked")
4. **Row 1:** Deploy history (wide: #, sha, message and source, state, duration, Log, pager) | Snapshots (the storage name in the title, tabs for Scheduled and Pre-deploy with counts, the keep rule, rows with Restore). This reuses the existing lazy snapshots frame, restyled.
5. **Row 2:** Secrets (wide: "Values are write-only." and Add secret; the HOLD banner when a required secret is blank; one row per secret) | Backup plan (a `dl` of schedule, storage, keep, volumes, databases and not backed up; "Edit in compose.yml").
6. **Below the two rows**, in the same grid: Connect pushes, Maintenance page and Volumes. The design has none of them (decision 1), so they get the design's panel and row styles.
7. **Narrow screens:** each row stacks to one column, with no horizontal scroll at any width down to 375px.

8. **Deploy** (your addition): a button at the top of Deploy history. It deploys the head of what the deploy rule matches, the same as `houston deploy --server` (`ChangeCheck#queue_head!`), then opens that deploy's log.
9. **What the facts strip and console box need, carried in the sync.** Mission Control stores service names only. The sync's new `details` field carries each service's image, the app's CPU and memory limits, and `commands.console` (its server form), from the same compose-go read. An older CLI sends none, and the page shows "—".

**How it's checked:** the request tests pin the structure. The visual check renders the page with the fixtures at 1440 wide, puts it next to the artboard, records it in this plan, and looks again at 375 wide.

### AC ↔ test map (Batch 1)
| # | Acceptance criterion | Test (`test/controllers/project_pages_test.rb` unless named) | Lens |
|---|---|---|---|
| 1 | The header has the name and chip together, one chip per hostname with its DNS state, and the console box with the project's console command; Create Snapshot is in the Snapshots head, not here | `test "the header follows the design: chip beside the name, host chips, console box"` | Contract |
| 2 | The console box is left out when x-houston has no `commands.console`, and doesn't invent a command | `test "no console box without commands.console"` | Contract |
| 3 | The facts strip has its five cells, and a project that was never deployed or linked says so in words | `test "the facts strip: sha, rule, resources, services, repo"` | Contract |
| 4 | Row 1 is Deploy history then Snapshots; row 2 is Secrets then Backup plan, in the order the design has them | `test "the panels sit in the design's rows"` | Parity |
| 5 | Secrets shows the HOLD banner only while a required secret is blank; an optional secret (equip's `RAILS_MASTER_KEY`) is listed and can be set | `test "secrets: HOLD only for a blank required secret; optional ones can be set"` | Preconditions |
| 6 | The snapshots panel has the Scheduled and Pre-deploy tabs with their counts (a project always has storage once setup is done: its gate needs a default) | `test/controllers/project_snapshots_test.rb` `test "tabs by kind with counts"` | Contract |
| 7 | Nothing on the page is wider than the screen at 375px; checked with the browser at 1440 and 375 against the artboard | the visual check, recorded here | Honest surface |
| 8 | Deploy queues the head the deploy rule matches and opens its log; a second click reuses the queued deploy | `test/controllers/deploy_pages_test.rb` `test "Deploy queues the head and opens its log; a second click reuses it"` | At-least-once |
| 9 | Deploy on a project with no repo linked, or whose repo can't be read, says why and queues nothing | `test/controllers/deploy_pages_test.rb` `test "Deploy refuses without a repo or when the repo can't be read"` | Preconditions |
| 10 | The sync carries `details` (images, CPUs, memory, console) from compose.yml | `internal/mission/request_test.go` `TestRequestForDetails` | Contract |
| 11 | Mission Control stores valid details, refuses malformed ones, and accepts a sync with none (an older CLI) | `test/integration/api_sync_test.rb` `test "details: stored, validated, optional"` | Contract |
| 12 | Every existing project page test still passes (the forms, maintenance, webhook, volumes and backups keep working) | the full Mission Control suite | Parity |

### Batch 1: done
- **Red first:** the eight new page rows failed before the view changed ("no console box" passed as a guard); the Deploy tests failed with no route; `TestRequestForDetails` didn't compile; the details sync test failed with no column.
- **Green:** the Mission Control suite passes, 288 runs with 0 failures, and rubocop is clean; every Go package passes. The migration runs both ways.
- **Visual check** (the page rendered from the tests with 13 deploys, one NO-GO, three secrets, two services and snapshots, served with the real fonts):
  - At 1440 wide it follows ProjectDetail: the header (the pill beside the name, the host chips with DNS state, the console box), the five-cell facts strip, Deploy history next to Snapshots, then Secrets next to Backup plan, then Connect pushes, Volumes and Maintenance in a row of three.
  - It found three layout bugs, now fixed: NOT BACKED UP split across lines, the maintenance message box grew tall, and Log wrapped on phones.
  - At 375 wide the document is 375 wide, with no element past the edge. That needed the top bar fixed too (it pushed Sign out off screen on every page).
- **Where it differs from the design, on purpose:**
  - A deploy's row shows its branch or tag, not a commit message, since Houston doesn't read commit messages.
  - The console command reads `houston`, not `hou`, as the README does.
  - Backup plan keeps its storage menu.
  - "Add secret" is left out, since secrets come from compose.yml.

## Batch 2: Add project (title only until Batch 1 is green)
`AddProject.dc.html`: step 01 on the left (repo, deploy key with GO or NO-GO, branch and config side by side, Read); on the right, 02 What Houston found, 03 Secrets, 04 Connect pushes, then Cancel, Save and Deploy. Decisions 2 and 3.

## Batch 3: every other screen against its artboard (titles only)
The projects list (`Main`, `ProjectsEmpty`), the deploy log (`DeployLog`), restore (`Restore`), Settings (`Settings`), and first run (`SetupAdmin`, `SetupCloudflare`, `SetupStorage`). An audit table first (artboard vs screen), then the fixes.

## Decisions (made: "go with your recommendations, run all three batches")
1. Panels the design doesn't have stay, below Secrets and Backup plan, in the design's styles.
2. Add project collects secrets and the webhook before saving, and has a Deploy button.
3. Volume placement leaves Add project; every volume starts on Local disk and is placed on the project page.
4. (Added) A Deploy button at the top of Deploy history.

## Decisions as they were asked
1. **Panels the design doesn't have** (Connect pushes after linking, Maintenance page, Volumes): I'd keep them, below Secrets and Backup plan, styled like the design's panels. The other choice is to fold them into the design's panels, for example the volumes' placement into Backup plan's VOLUMES row. That's closer to the design, but crowds that panel.
2. **Add project collects secrets and the webhook before saving, as the design does, and gets a Deploy button.** It's more to build than a restyle: today the secrets are set on the project page after Save. I'd follow the design.
3. **Volume placement leaves Add project** (the design has no step for it). Every volume starts on Local disk, and placement moves to the project page (decision 1). This matches what you said about the volumes UI.
