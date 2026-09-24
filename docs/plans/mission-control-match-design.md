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

## Batch 2: Add project
Layout, from `AddProject.dc.html`:
- **Left, 01 Link the repo:** the repo URL and Check access; the deploy key box with its GO or NO-GO line; branch and config path side by side; Read compose.yml; the `houston init` hint.
- **On the right, once compose.yml is read:**
  - 02 What Houston found
  - 03 Secrets: one row per variable in compose.yml, with a password field, "Required" or "Optional"
  - 04 Connect pushes: the webhook URL and secret with Copy, and what to send
  - then Cancel, Save and Deploy, with a line saying required secrets left blank hold deploys
- **Before the read,** the right side says what will appear there.

Behaviour (decisions 2 and 3):
- **The webhook secret** is made with the draft, so step 04 can show it before Save. Linking a project again keeps its secret, and shows that one.
- **Secrets entered here are saved with the project** in the same transaction. Blank fields are skipped, and a name compose.yml doesn't list is ignored. A value the container can't receive refuses the whole Save: the draft is kept and the field says why.
- **Deploy** saves, then queues the head the deploy rule matches (as the project page's Deploy does) and opens its log. If the repo can't be read at that point, the project is still saved, and the project page says why nothing deployed.
- **No volume step:** every volume starts on Local disk, and placement is on the project page. The `locations` parameter is gone.

Where it differs from the design, on purpose:
- No "Add secret" or Remove, since secrets come from compose.yml.
- Domains are listed without the design's per-domain DNS readiness, since checking it needs Cloudflare calls at read time. The project page shows it after Save.

### AC ↔ test map (Batch 2, `test/controllers/project_links_test.rb`)
| # | Acceptance criterion | Test | Lens |
|---|---|---|---|
| 1 | Step 01 is on the left; after the read, 02, 03 and 04 are on the right in order, and before it the right side has a placeholder and no secrets panel | `test "the steps sit where the design has them"` | Parity |
| 2 | Secrets typed on Add project are saved with the project, write-only; blank ones are skipped; names outside compose.yml are ignored | `test "secrets set on Add project are saved with it"` | Contract |
| 3 | A secret the container can't receive refuses Save: no project, no secrets, the draft kept, the error on its field | `test "a bad secret refuses the whole save"` | Atomicity |
| 4 | The webhook secret shown in step 04 is the one the project gets; linking again shows and keeps the project's own | `test "the webhook secret is shown before saving and kept"` | Contract |
| 5 | Deploy saves and queues the head, then opens its log; when the repo can't be read, the project is saved and the page says why | `test "Deploy saves then deploys; a failed read still saves"` | Crash & repair |
| 6 | No volume step; volumes start on Local disk, and a `locations` parameter changes nothing | `test "volumes start on local disk; placement is on the project page"` | Honest surface |
| 7 | Every existing Add project test still passes (hostile input, access, read, clash, admin, needs a read) | the file | Parity |

### Batch 2: done
- **Red first:** the six new rows failed (no layout, no secrets field, no webhook secret before Save, no Deploy, and a volume menu still shown).
- **Green:** the Mission Control suite passes, 292 runs with 0 failures, and rubocop is clean. The migration (`repo_links.webhook_secret`, encrypted) runs both ways.
- **Visual check** (rendered after a read, served with the real fonts): at 1440 wide it follows AddProject, with 01 on the left and 02, 03 and 04 with Cancel, Save and Deploy on the right. At 375 wide the document is 375 wide.
- **Found while fixing:** a Save with no draft showed nothing, because its error sat inside the draft-only part of the page. It's a NO-GO notice at the top of the right side now.


## Batch 3: every other screen against its artboard
Each screen was rendered from the tests with realistic data, served with the real fonts, and set beside its artboard.

### The audit
| Screen | Artboard | What differed | Now |
|---|---|---|---|
| Flight board | `Main` | Plain-text status; services without images; underlined mono domains; no LAST BACKUP; in-flight rows not tinted; Add project underlined, no + | Status pills; `db · postgres:17`; host rows with dot and ↗; LAST BACKUP (time, kind, size); the in-flight tint; the + button; the "let the first houston deploy register it" note |
| Empty board | `ProjectsEmpty` | Close; the button was underlined, and it overflowed at 375 | Fixed |
| Top bar | every artboard | Status order TUNNEL · REGISTRY · RUNNERS; no GO on runners; Projects not current on project pages; overflow on phones | TUNNEL · RUNNERS n/n GO · REGISTRY; Projects current off Settings; it fits a phone |
| Deploy log | `DeployLog` | The pill under the title; no MISSION ELAPSED clock; an amber banner; narrow steps; a light log head | Pill beside the title; the elapsed clock (TOOK once done); the serving line with its dot; 440px steps, current row amber; the dark log with LIVE |
| Restore | `Restore` | A left-aligned page panel | The dialog over the dimmed page: the banner, the NOW → AFTER RESTORE grid, the note, the two phases, the confirm row |
| Settings | `Settings` | A crumb line for nav; a full-width select; a giant Save and token field; a cramped storage list | The section nav on the left (the current one filled), sections as the design's panels, the Cloudflare facts and ingress rules, and the storage table with its head row |
| First run | `SetupAdmin`, `SetupCloudflare`, `SetupStorage` | Matched, except finished steps showed their number | Finished steps show GO |
| Every page | | A paragraph row broke its inline text into lines (the rows are flex columns) | `p.panel__row` flows as text |

### AC ↔ test map (Batch 3)
| # | Acceptance criterion | Test | Lens |
|---|---|---|---|
| 1 | The flight board follows Main | `projects_controller_test.rb` `test "the flight board follows the design"` | Parity |
| 2 | The top bar's order and runner verdict; Projects current on project pages | `projects_controller_test.rb` `test "the top bar: status in the design's order, and Projects current on project pages"` | Parity |
| 3 | The deploy page follows DeployLog, LIVE only while in flight | `deploy_pages_test.rb` `test "the deploy page follows the design"` | Parity |
| 4 | Restore is the design's dialog | `project_restores_test.rb` `test "the confirm page and restoring"` (its new assertions; shown failing against the old view first) | Parity |
| 5 | Settings follows the design, the current section marked | `settings/general_controller_test.rb` `test "settings follow the design"`; `settings/storage_controller_test.rb` (its head row) | Parity |
| 6 | Finished setup steps show GO | `setup/cloudflare_controller_test.rb` `test "the step indicator marks finished steps GO"` | Parity |
| 7 | No screen scrolls sideways at 375px | the visual check: all twelve rendered pages measured 375 wide with no element past the edge | Honest surface |

### Batch 3: done
- **Red first** for rows 1-3, 5 and 6. Row 4's view was written before its test. The test was then run against the old view from git, and it failed.
- **Green:** the Mission Control suite passes, 297 runs with 0 failures. Rubocop is clean on the whole app, and every Go package passes.
- **Dead CSS removed:** `.hosts`, `.column`, `.column--side`, `.webhook__actions` and `.secret__form`.

## Batch 4: one column, with a section menu (Settings and the project page)
Your direction: "Let's move to a single column layout for settings and project detail with a menu on the left to jump to the individual sections. The multiple columns look terrible. For the project detail, put the menu and single column of tables under the header section."

This replaces Batch 1's two-row grid and Batch 3's three Settings pages.
- **Project page:** the header (the name and pill, host chips, console box) and the facts strip stay full width. Below them, a menu on the left links to each section: Deploy history, Snapshots, Secrets, Backup plan, then Connect pushes, Volumes and Maintenance when the project has them. The sections follow in one column.
- **Settings:** one page at `/settings`, as the design's Settings: a menu on the left (Cloudflare, Time zone, Storage, API tokens) and the sections in one column. The old addresses (`/settings/general`, `/settings/storage`, `/settings/tokens`) send you to their section. Add storage location and a new location's password stay pages of their own, with the same menu. Creating a token shows the new token on the Settings page, above the tokens.
- **The menu marks the section in view** (a small Stimulus controller). Without JavaScript, the links still jump.
- **On a phone** the menu becomes a row of links above the sections, and nothing scrolls sideways.

### AC ↔ test map (Batch 4)
| # | Acceptance criterion | Test | Lens |
|---|---|---|---|
| 1 | The project page: the header and facts strip, then a menu whose links match the sections, in one column, in order | `project_pages_test.rb` `test "one column of sections under the header, with a menu"` | Parity |
| 2 | The menu lists Connect pushes, Volumes and Maintenance only when the page has them | the same test, with an unlinked project | Honest surface |
| 3 | Settings is one page with the menu and four sections | `settings/general_controller_test.rb` `test "settings is one page: the menu and its sections"` | Parity |
| 4 | The old Settings addresses send you to their section; saving the time zone, making a default and revoking a token come back to their section | `settings/*_test.rb` (their redirects) | Re-entry |
| 5 | A new token shows once, on the Settings page | `settings/tokens_controller_test.rb` `test "a token is shown once"` | Contract |
| 6 | No sideways scroll at 375px; checked at 1440 and 375 | the visual check | Honest surface |

### Batch 4: done
- **Red first:** the new project page and Settings tests failed with no `settings_path`, no menu and no section ids.
- **Green:** the Mission Control suite passes, 299 runs with 0 failures, and rubocop is clean.
- **Visual check:** at 1440 wide, both pages show the menu on the left and one column of sections; the project page keeps its header and facts strip across the top. At 375 both are 375 wide, with the menu as a row of links. That needed a fix: the stacked project column had sized itself to its content, 3,636px wide.
- **The snapshots frame** is `snapshots-list` now, so its id no longer clashes with the Snapshots section's `#snapshots`.

## Later (named, not built)
From the Settings artboard, features rather than layout:
- **Cloudflare:** Replace API token, and the zones list (Houston records per zone, their sync state, Export DNS).
- **Account:** change email and password, signed-in sessions, sign out everywhere else.
- **Tokens:** the runner's token row, with Rotate.
- **Header:** the design's account avatar, once there's more than one user.

## Decisions (made: "go with your recommendations, run all three batches")
1. Panels the design doesn't have stay, below Secrets and Backup plan, in the design's styles.
2. Add project collects secrets and the webhook before saving, and has a Deploy button.
3. Volume placement leaves Add project; every volume starts on Local disk and is placed on the project page.
4. (Added) A Deploy button at the top of Deploy history.

## Decisions as they were asked
1. **Panels the design doesn't have** (Connect pushes after linking, Maintenance page, Volumes): I'd keep them, below Secrets and Backup plan, styled like the design's panels. The other choice is to fold them into the design's panels, for example the volumes' placement into Backup plan's VOLUMES row. That's closer to the design, but crowds that panel.
2. **Add project collects secrets and the webhook before saving, as the design does, and gets a Deploy button.** It's more to build than a restyle: today the secrets are set on the project page after Save. I'd follow the design.
3. **Volume placement leaves Add project** (the design has no step for it). Every volume starts on Local disk, and placement moves to the project page (decision 1). This matches what you said about the volumes UI.
