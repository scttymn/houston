# Plan: Deploy path (build step 3)

Spec v11: https://claude.ai/artifact/R3d4fzN1u5QUP4kT88mqug (§4 how the file is read on the server, §7 the deploy job, §8 Cloudflare, §10 API and security notes, §12 build step 3, §14 verify before building on).
Design: https://claude.ai/artifact/UswdZ62N4ygiKdGh1vDEPH (project detail, deploy page). The design wins over the spec where they differ.

## Goal

On a Houston server, `houston deploy` in a checkout of a project takes it from `compose.yml` to a running app at `<name>.<base>`, with zero downtime. It gets secrets from Mission Control, runs the release hook before switching traffic, and leaves the old version serving if anything fails. Every run is a numbered deploy with its log in Mission Control.

## Scope

- **`internal/kamal`:** Kamal config generated from compose: the app becomes Kamal service `<name>`, other services become accessories named `<name>-<service>`, named volumes are prefixed with the project name, proxy hosts are `<name>.<base>` plus `domains`, and env is split into clear and secret.
- **Mission Control's local API:**
  - the runner token
  - `POST /api/projects/sync`
  - `GET /api/projects/:name/secrets/:key`
  - deploy records (numbered per project, one in flight)
- **`houston deploy`:** the steps in spec §7, except the pre-deploy snapshot and test step 00.
- **Mission Control pages:** project detail (facts, secrets, deploy history) and the deploy page (steps, log).
- **The installer** puts the `houston` CLI on the server and writes the runner token.
- **Defer:**
  - the pre-deploy snapshot → build step 5 (volumes and backups)
  - test step 00, runners, webhooks, the `hooks.<base>` reachability probe, and the live-streaming deploy page → build step 4
  - volume locations other than local disk → build step 5
- **Boundary:** "hooks" in build step 3 means the app's `hooks.release` / `hooks.post_deploy`, not webhooks.

## Batches

1. **Kamal config from compose, proven on a server** (done).
2. **Mission Control's local API: auth, sync, secrets.** (Split from deploy records, which would push one batch well past ~12 rows.)
3. **Deploy records:** numbering, one in flight per project, a deploy token for ownership, heartbeat and stale takeover, capped log.
4. **`houston deploy`:**
   - ref vs deploy rule, sync, secrets (`CarrierValue`), build with `--label service=<name>` and push
   - accessories (boot; reboot on changed config), release hook, `kamal deploy --skip-push --version <sha>`, post_deploy, report
   - stale Kamal lock released only on takeover
   - the installer installs the CLI and writes the runner token
5. **Mission Control pages:** project detail (facts, write-only secrets with Generate, deploy history), the deploy page (steps, log), and projects on the flight board.
6. **Real run on svnmns.com:**
   - equip and a Postgres fixture on an OrbStack VM, at `<name>.svnmns.com` through the tunnel
   - a zero-downtime redeploy
   - a failed release and a failed health check leave the old version serving (NO-GO)
   - cleanup

## Batch 1: Kamal config from compose, proven on a server

### Design (short)

- **New package `internal/kamal`**, pure: `project.Project` + `Target` in, bytes out. No Docker, no network.
  - `Config(p, t) ([]byte, error)` returns `deploy.yml`.
  - `SecretsFile(p) ([]byte, error)` returns `.kamal/secrets`.
  - Batch 3's `houston deploy` is their production caller. Batch 1 proves them with golden tests, and the golden output is what the VM spike deploys, so the tests and the real server check the same bytes.
- **`Target`:**
  - `BaseDomain`
  - `Arch`: `amd64` | `arm64`; Kamal requires `builder.arch` even when Houston pushes the image itself
  - (The version isn't in `deploy.yml` at all; it's `kamal … --version <sha>`. So `Target` has no `Version`.)
- **Constants, not parameters** (single-host v1; honest surface):
  - registry `127.0.0.1:5000`
  - SSH `houston@127.0.0.1` with key `/ssh/id_ed25519`
  - Kamal image `ghcr.io/basecamp/kamal:v2.12.0`
- **Registry.** The server is `127.0.0.1:5000`, deliberately not `localhost:5000`. For `localhost…`, Kamal 2.12 starts its own `kamal-docker-registry` on 127.0.0.1:<port> and forwards the port over SSH, which fights the installer's registry on the same host (`lib/kamal/commands/registry.rb`, `cli/build.rb`). A non-localhost server needs a username/password (`validator/registry.rb`). Houston's registry has no auth, so these are fixed dummies: `username: houston`, password secret `KAMAL_REGISTRY_PASSWORD`. **Spike item S1.**
- **Building.** Houston builds and pushes the production target itself (`docker build` / `docker push` through the host daemon, Batch 3), then runs `kamal deploy --skip-push --version <sha>`. Kamal's default buildx container driver can't reach the host's 127.0.0.1:5000.
- **Proxy.**
  - `hosts: [<name>.<base>, …domains]`, `ssl: false`
  - `app_port` from the project
  - `healthcheck.path` from `x-houston.health`
  - `run.publish: false`: kamal-proxy takes no host ports. cloudflared reaches it on the `kamal` network, and the only inbound traffic is the tunnel (spec §2).
- **Env.** Each environment value, taken from the uninterpolated model (`SkipInterpolation`), is one of:
  - no references (after `$$` → `$`): **clear**, literal
  - only `<SERVICE>_HOST` references for services in the file: **clear**, resolved to `<name>-<service>`
  - any secret reference (a name that isn't a service's `_HOST`, e.g. `${QUEUE_HOST:-queue}` with no `queue` service): **secret**. If the value is exactly one variable with no default (`$VAR`, `${VAR}`, `${VAR:?msg}`), it's aliased `KEY:VAR`, so one value is shared everywhere VAR is used. Anything else, including `${VAR:-default}` (two uses can have different defaults), is a composite aliased `KEY:<SERVICE>__<KEY>` (uppercase, `-`→`_`), resolved by `houston deploy` in Batch 3.
  - a secret anywhere but `environment` (`command`, `image`, a healthcheck) is an error. Kamal's `cmd` and docker options are plain text on the host, and Kamal leaves `${…}` in option values unescaped for the host shell, so `${` in a resolved option value is an error too ("write `$$NAME`").
- **ERB.** Kamal renders `deploy.yml` through ERB (`configuration.rb:42`), so every string taken from compose has `<%` written as `<%%`. The YAML itself is produced by marshalling structures, never by text templates.
- **Secrets file.** `NAME=$(printenv HOUSTON_S_NAME)`, one line per secret name, sorted, plus `KAMAL_REGISTRY_PASSWORD`. Why not `NAME=$NAME`: a value containing `$(…)` could then execute inside the Kamal container, which has the Docker socket and the SSH key. With `printenv`, the only command is fixed text. (The spike showed that dotenv 3.2 in the image runs command substitution first and variable substitution on the result, so `$VAR` inside a value still needs `CarrierValue`'s escaping; see Done.) The `HOUSTON_S_` prefix keeps an app secret named `PATH` or `HOME` from clobbering the Kamal container's own environment. Known loss: a trailing newline is chomped. **Spike item S3.**
- **Accessories** (every non-app service):
  - `image` and `cmd` (from `command`)
  - `host: 127.0.0.1`
  - env split as above
  - named volumes `<name>_<volume>:<path>`
  - a compose `healthcheck` becomes docker `options` (`health-cmd`, `health-interval`, `health-timeout`, `health-retries`, `health-start-period`)
  - `ports` are dropped (spec §4), and so is `restart` (Kamal already restarts accessories)
- **App:**
  - `servers.web.hosts: [127.0.0.1]`
  - `cmd` from `command`
  - `options` from `deploy.resources.limits` (`cpus`, `memory`)
  - named volumes `<name>_<volume>:<path>`
  - the compose `healthcheck` is dropped; the proxy checks `x-houston.health`
- **Volume names** `<name>_<volume>` match what `docker compose -p <name>` would use, so they're predictable. Build step 5 replaces "local disk" with the chosen location.

### Contract pin

- **`Config(p *project.Project, t Target) ([]byte, error)`**
  - Inputs: `p` from `project.Load`, so it's already valid against the supported subset. `t.Version` must match `^[0-9a-f]{40}$`, `t.BaseDomain` must be a valid lowercase hostname, and `t.Arch` must be `amd64` or `arm64`. Anything else is an error, and no bytes are returned.
  - Env keys that end up secret must match `^[A-Za-z_][A-Za-z0-9_]*$` (Kamal and dotenv names). Otherwise it's an error naming `services.<svc>.environment.<key>`.
  - Two composite secret names that sanitize to the same string are an error naming both.
  - Output: deterministic YAML (sorted maps, stable order), with ERB-escaped strings.
- **`SecretsFile(p *project.Project) ([]byte, error)`**
  - Names only, never values. Sorted, with no duplicates. No `<SERVICE>_HOST` names. Includes the composite names and `KAMAL_REGISTRY_PASSWORD`.
- **Authz / preconditions:** none. They're pure functions, and their callers (Batch 3) run on the server as `houston`.

### Adversarial AC

1. A value from the repo can't run code on the server. `<%= … %>` in an env value, a domain-like string, or a command is written escaped and arrives literal (spike S4).
2. A secret value can't run code in the Kamal container. `$(…)`, `` `…` ``, `$VAR`, quotes, and newlines inside a value arrive byte-exact (spike S3).
3. YAML structure can't be injected. A clear value with a newline, `: `, `- `, `#`, or `{` round-trips as the same string.
4. Two services' composite secrets can't collide silently.
5. `<SERVICE>_HOST` can never point at another project's container, because it's always resolved to `<name>-<service>` with the project's own name. (The cross-project name collision, `shop` + service `db` vs a project called `shop-db`, is Batch 2's sync rule, because only Mission Control knows the other projects.)
6. An invalid target produces no config at all.

### AC ↔ test map (Batch 1)

Go tests in `internal/kamal/kamal_test.go`, fixtures in `internal/kamal/testdata/`, run with `bin/go test ./internal/kamal/`. The spike script runs on an OrbStack VM installed by `install/test/orbstack.sh`'s steps.

| # | Acceptance criterion | Test | Lens |
|---|---|---|---|
| 1 | Rails + SQLite fixture (equip's shape) → golden `rails-sqlite.deploy.yml`: service/image, registry and dummy user, builder arch, `servers.web.hosts`, proxy (hosts, `ssl: false`, `app_port`, health path, `run.publish: false`), `equip_storage:/rails/storage`, clear env | `TestConfigRailsSQLite` | Contract |
| 2 | Postgres fixture → golden `postgres.deploy.yml`: accessory `db` (image, host, cmd, `<name>_pgdata`, healthcheck → options, no ports), app `DB_HOST` clear `= <name>-db`, `DATABASE_URL` secret aliased `DATABASE_URL:APP__DATABASE_URL`, `POSTGRES_PASSWORD` shared `KEY:VAR` | `TestConfigPostgresAccessory` | Contract, Parity (spec §4 table) |
| 3 | Env classification table: literal; `$$` → `$`; only service hosts (resolved, including `${X_HOST:-other}` for a name that isn't a service); exact `${VAR}` / `${VAR:?msg}` / `${VAR:-d}` → `KEY:VAR`; composite → `KEY:SVC__KEY` | `TestEnvClassification` | Contract |
| 4 | Proxy hosts: `<name>.<base>` first, then `domains` in order, deduped (a domain equal to `<name>.<base>` isn't repeated) | `TestProxyHosts` | Contract |
| 5 | `deploy.resources.limits` → `options.cpus` / `options.memory`; no limits → no `options` key | `TestResourceLimits` | Contract |
| 6 | ERB: `<%` in an env value, command, or healthcheck test is written `<%%`; the output contains no unescaped `<%` | `TestConfigEscapesERB` | Contract (hostile input) |
| 7 | YAML: a clear value with newline, `: `, `- `, `#`, `{` unmarshals back to the identical string | `TestConfigRoundTripsHostileValues` | Contract (hostile input) |
| 8 | Secret env key that isn't a valid name (`my.key`) → error naming `services.app.environment.my.key`, no bytes; two composites sanitizing alike (`a-b`/`X` vs `a_b`/`X`) → error naming both | `TestConfigRejectsUnsafeSecretNames` | Contract |
| 9 | Bad target (short SHA, uppercase hex, empty or invalid base domain, arch `386`) → error, nil bytes | `TestConfigRejectsBadTarget` (table) | Contract, Preconditions |
| 10 | Secrets file: `NAME=$(printenv HOUSTON_S_NAME)` lines, sorted, unique, names only, no `_HOST` names, includes composites and `KAMAL_REGISTRY_PASSWORD`; golden for the Postgres fixture | `TestSecretsFile` | Contract, Honest surface |
| 11 | Deterministic: the same inputs produce byte-identical output across 20 runs (map order) | `TestConfigIsDeterministic` | Contract |
| 12 | **Spike on a VM** (`install/test/kamal-spike.sh`), which deploys the golden Postgres fixture with Kamal 2.12 (`--network host`, the Docker socket, houston's key). Each item is recorded; any "no" changes the design before Batch 2:<br>**S1** `127.0.0.1:5000` with the dummy login pushes and pulls; no `kamal-docker-registry` appears.<br>**S2** SSH `houston@127.0.0.1` works non-interactively (host key handling recorded).<br>**S3** secret values with `$(touch /tmp/pwned)`, `` `id` ``, `$HOME`, `'"`, a newline, and `<%= 1 %>` arrive byte-exact in the app, and `/tmp/pwned` doesn't exist in any container.<br>**S4** the ERB-escaped clear value arrives literal.<br>**S5** the accessory resolves as `<name>-db` from the app.<br>**S6** both proxy hosts route (curl with `Host:` from a container on `kamal`), and host ports 80/443 aren't bound.<br>**S7** a second version deploys while a curl loop sees only 200s.<br>**S8** a one-off `docker run --network kamal <image:new>` reaches `<name>-db` before boot (release hook feasibility).<br>**S9** a killed deploy leaves a Kamal lock; the next deploy's message is recorded, and `kamal lock release` recovers.<br>**S10** `kamal accessory boot` on a running accessory: error or no-op, recorded. | `install/test/kamal-spike.sh` (manual, output recorded here) | Parity (spec §14), Crash & repair |

The spike's fixture app is a tiny busybox `httpd` image (dev/test/production stages from one base), not a language runtime. Its entrypoint writes the env it received, and the result of looking up `$DB_HOST`, into files that the spike reads back.

### Lens run (Batch 1)
- **Authz, Concurrency, At-least-once, Atomicity, Migrate:** N/A. These are pure functions, and the spike is a script. Batch 2 carries the one-deploy-at-a-time and stale-deploy templates; Batch 3 carries the Kamal lock crash gap (S9 feeds it).
- **Signals:** errors name the key path, as `project.Errors` does.
- **Scale:** the input is bounded by `project.Load`'s 1 MiB file limit.
- **Honest surface:** there are no registry, SSH, or image parameters until a second host exists (spec non-goal: multiple hosts). `Target` has three fields, each used.

### Agent loop checkpoints
- Red: rows 1–11 fail for the missing package. Report the RED map, then continue.
- Green rows 1–11 → run the spike (row 12) → record S1–S10 here → adjust the Batch 2–3 design for any "no".
- Batch 1 done → scotty-review with the cold pass.
- Each later batch: re-read this file, write that batch's full map before its code.

### Done (Batch 1)
- **Red:** every row failed against stubs, except `TestConfigRejectsBadTarget`, which passed trivially against an always-error stub until the real code existed. **Green:** 17 tests in `internal/kamal` (`bin/go test -count=1 ./internal/kamal/`), `go vet` clean, and the whole Go suite green after exporting `project.HostVar` for reuse.
- **Mutations, each caught:** no ERB escaping (2 failures); `NAME=$NAME` in the secrets file (2); no `cmd` quoting (1); defaults treated as exact aliases (1); no `${` check on options (1); no secret-name collision check (1); no value-less-env check (1).
- **Cut:** the generator's own anonymous-volume check was unreachable, because the loader already rejects anonymous volumes with a better message.
- **Spike (row 12), `install/test/kamal-spike.sh` on Ubuntu 24.04 + Kamal 2.12.0: SPIKE PASS on the third run.** The first two runs found facts that changed the design:
  - **Kamal only deploys images labelled `service=<name>`** (its own builder adds the label). Houston's `docker build` adds `--label service=<name>` → Batch 3.
  - **Every Kamal command needs `--version`.** The working directory is a generated folder with no `.git` → Batch 3.
  - **Values:** dotenv 3.2 in the Kamal image runs command substitution **then** variable substitution. `printenv`'s output was never executed (S3 held), but `$HOME` inside a value was expanded. So houston deploy passes `kamal.CarrierValue(v)`: a backslash before every `$` not followed by `(` (dotenv drops it; `$(` is never a variable). `TestCarrierValue` and `TestCarrierValueSpikeFixture` pin it, and the spike delivers the hostile value byte-exact.
  - **Values Kamal can't carry:** Kamal writes env through docker env files, which docker reads literally (checked locally), after escaping with Ruby's `String#dump`. A backslash arrives doubled, and tab or newline as `\t` / `\n`. Houston refuses them rather than deploy a different value: `Config` for clear values (`TestConfigRejectsValuesKamalCantCarry`), `CarrierValue` for secrets. Non-ASCII passes untouched. **Mission Control should refuse such secret values at save time (Batch 2/4)**, with the hint to base64-encode.
- **S1–S10:**
  - **S1 yes:** `127.0.0.1:5000` with the dummy login (Kamal runs `docker login`, and the registry accepts it); no `kamal-docker-registry`.
  - **S2 yes:** SSH `houston@127.0.0.1` with the installer's key, with no host-key prompt.
  - **S3 yes**, with the carrier escaping.
  - **S4 yes:** `<%%=` arrives as `<%= 1 + 1 %>`.
  - **S5 yes:** `spike-db` resolves from the app.
  - **S6 yes:** both hosts route through kamal-proxy on the `kamal` network, and nothing listens on host ports 80/443.
  - **S7 yes:** 152 requests during the v2 deploy, all 200.
  - **S8 yes:** a one-off container of the new image on `kamal` resolves the accessory.
  - **S9:** a killed deploy leaves Kamal's lock ("Deploy lock already in place!"); `kamal lock release` recovers → Batch 3 releases a stale lock only when Mission Control says the previous deploy is dead.
  - **S10:** booting again skips any accessory whose container exists ("a container already exists"). So **changed accessory config is never applied** unless Houston reboots it → Batch 3 compares and reboots.

## Batch 2: Mission Control's local API (auth, sync, secrets)

### Design (short)
- **`Api::BaseController < ActionController::API`** (no cookies, CSRF, browser gate or setup redirect).
  - **Auth:** `Authorization: Bearer <token>` compared with `ENV["HOUSTON_RUNNER_TOKEN"]` via `secure_compare`. A blank or unset env token rejects everything.
  - **Never through the tunnel:** any request carrying `Cf-Ray` or `Cf-Connecting-Ip` (Cloudflare always adds them; clients can't remove them) gets 404, before auth, so nothing is revealed.
  - **Setup must be finished:** the API answers 409 "finish setup" until Cloudflare is connected.
- **Where the token comes from:** in production, the installer puts it in `/opt/houston/.env` (Batch 4). Tests set the env var.
- **`Project`** (name unique):
  - `app_service`, `services` (JSON array), `domains` (JSON), `variables` (JSON `[{name, required}]`, secrets only; the CLI never sends `_HOST`s)
  - `health`, `port`, `deploy_rule` (JSON), `synced_at`
- **`ProjectHost`** (`name` unique index, `project_id`): one row per container-name prefix the project owns:
  - its name (Kamal app containers and the `service` label)
  - `<name>-<service>` for each accessory
  - Sync replaces a project's rows in one transaction; the **unique index** makes two projects claiming one name impossible even under concurrent syncs. This closes the leak where `shop` + service `db` and a project called `shop-db` collide: Kamal's accessory boot would find the other's container by its `service` label, skip booting (spike S10), and `DB_HOST` would reach the other project's database.
- **`Secret`** (`project_id`, `key`, encrypted `value`; unique `[project_id, key]`):
  - keys match `^[A-Za-z_][A-Za-z0-9_]*$`
  - values Kamal can't carry (backslash, control characters) are invalid, with the base64 hint; the same rule as `kamal.CarrierValue`
- **`POST /api/projects/sync`** (JSON, body ≤ 64 KiB):
  - Validates the payload: name rules as the CLI's (`^[a-z]([a-z0-9-]{0,61}[a-z0-9])?$`, not `admin`/`hooks`), service names, app service in services, variable names, domains as hostnames, integer port, health path.
  - Upserts the project and its hosts in one transaction.
  - **Then** points DNS: in host-by-host mode it ensures `<name>.<base>` is a proxied CNAME to the tunnel with comment `managed-by:houston project:<name>`. A managed record is updated; a record without the comment is a NO-GO (422, nothing written). In wildcard mode there's no Cloudflare call. This reuses the record helpers extracted from `CloudflareSetup` into `Cloudflare::Records`.
  - **Then** checks variables: required ones with no value → 422 `{missing: [...]}` (HOLD), with the project already saved so the admin can fill them in.
  - Success → 200 `{project, host, dns}`.
- **`GET /api/projects/:name/secrets/:key`:** 200 `text/plain` value; 404 when the project is unknown, the key isn't one of its variables, or there's no value.

### Crash-gap template (sync)
```text
Durable step 1 (primary): the project row + its hosts (one transaction)
Dies before: the DNS record (Cloudflare) / the variable check
Retry / redelivery: houston deploy syncs again; the upsert finds the row and changes nothing
Must still accomplish: the <name>.<base> record; the missing-variables answer
Paths that must not block repair with a hard "already done": none. DNS is ensured on every sync (find → create or update)
After primary commits: the Cloudflare failure is a 502 with Cloudflare's message; the project stays saved
```

### Concurrency template (sync)
```text
Writer A: sync of shop (services app, db)      Writer B: sync of shop-db
Ordering / lock / CAS / uniqueness: unique index on project_hosts.name
Bad interleaving: both check "no clash" before either inserts → both commit → shared name
Expected end state: exactly one of them owns "shop-db"; the other gets 422 naming it
Re-check under the lock: the index is the check; RecordNotUnique → 422
Work under the lock (same store only): project + host rows; Cloudflare is called after commit
```

### AC ↔ test map (Batch 2), `mission_control/test/…`
| # | Acceptance criterion | Test | Lens |
|---|---|---|---|
| 1 | No token, wrong token, or `Bearer ` with a blank configured token → 401, no row | `integration/api_auth_test.rb` `test "the API needs the runner token"` | Authz |
| 2 | A valid token with `Cf-Ray` or `Cf-Connecting-Ip` → 404 on sync and on a secret, no row | `test "the API never answers through the tunnel"` | Authz |
| 3 | Before Cloudflare is connected → 409 "finish setup", no row | `test "the API waits for setup"` | Preconditions |
| 4 | New project → 200 with host `equip.svnmns.com`; the row has every fact; hosts `equip`, `equip-db` | `integration/api_sync_test.rb` `test "sync creates the project"` | Contract |
| 5 | Invalid payloads (table): name `Equip`/`admin`/`hooks`/`-x`, services not an array, app service not in services, variable `my.key`, domain with a scheme, port 0 or `"80"`, health without `/` → 422 naming the field; not JSON → 400; body > 64 KiB → 413; no row | `test "sync rejects what the CLI would never send"` | Contract, Scale |
| 6 | Syncing again updates facts and hosts (a service removed drops its host) without duplicating | `test "sync again updates in place"` | Re-entry |
| 7 | Required variable without a value → 422 `missing: [KEY]` and the project saved; optional ones aren't listed; after the value is set → 200 | `test "sync holds for missing required secrets"` | Preconditions, Signals |
| 8 | Name clashes, both directions and accessory vs accessory (`a`+`b-c` vs `a-b`+`c`) → 422 naming the clash, the other project untouched | `test "sync refuses container names another project owns"` | Contract |
| 9 | The index holds under a race: inserting a `ProjectHost` name another project owns raises `RecordNotUnique` | `models/project_host_test.rb` `test "a host name has one owner"` | Concurrency |
| 10 | Host-by-host: creates the CNAME with the comment; updates Houston's own; a foreign record → 422 NO-GO, no write; wildcard mode → no Cloudflare request | `test "sync points <name>.<base> in host-by-host mode"` (table, WebMock contract stubs) | Contract, Crash & repair |
| 11 | Cloudflare fails on the record → 502 with its message, project saved; the next sync creates the record | `test "a Cloudflare failure during sync can be retried"` | Crash & repair, Signals |
| 12 | Secret: 200 text value; 404 for an unknown project, an unreferenced key, and no value | `integration/api_secrets_test.rb` `test "the runner reads a secret"` | Contract, Authz |
| 13 | `Secret` rejects backslash, line break, tab, and a bad key; stores the value encrypted (raw column ≠ value) | `models/secret_test.rb` `test "secrets Kamal can carry, encrypted"` | Contract |

### Done (Batch 2)
- **Red:** all 13 rows (13 tests) failed for the missing API and models. **Green:** 75 runs, 0 failures (`houston -f mission_control/compose.yml test`). Rubocop is clean on the 17 touched files, and the migration runs down and up.
- **Test fixes on the way (the tests were wrong, not the code):**
  - Two sync tests used a required variable with no value, so the design correctly answered 422 HOLD. They now use optional variables; row 7 covers HOLD.
  - The secret test matched `base64` against "Base64".
- **Mutations, each caught:** no tunnel refusal (row 2); a blank configured token accepted (row 1); a foreign DNS record overwritten (row 10); no HOLD for missing secrets (row 7).
- **Cut after a mutation survived:** the Ruby pre-check for container-name clashes. With it removed, row 8 still passed: the unique index raises, the sync rolls back, and the rescue names the owner. So the index is the only check, which is also what makes the race safe (row 9).
- **Reuse:** the DNS record helpers moved from `CloudflareSetup` into `Cloudflare::Records`, shared by first-run setup and sync. All the Cloudflare setup tests pass unchanged.

### Open questions
None blocking Batch 1. Recorded for Batch 2:
- **What "localhost only" means for the runner's secrets route.** Mission Control runs in a container, so its callers show up as Docker addresses, and cloudflared sits on the same network as the runners.
  - The plan: runner token (256-bit, never leaves the server) + refuse any request carrying `Cf-Ray` or `Cf-Connecting-Ip`, which Cloudflare always adds and clients can't remove.
  - This is a documented divergence from a literal source-address check.
- **CLI personal API tokens and `--server` from a laptop.** Their home is build step 4, with Settings › API tokens. Build step 3's `houston deploy` runs on the server with the runner token.
