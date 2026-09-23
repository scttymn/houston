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
4. **`houston deploy`: the flow.** Ref vs deploy rule, sync, start, secrets, build and push, accessories, release hook, Kamal deploy, post_deploy, report; every failure exit.
5. **`houston deploy` under pressure:** taken over mid-run (stop), an overall deadline (the stuck-alive home from Batch 3), heartbeat and chunked log streaming, and accessory config drift (a label with the accessory's config hash; reboot when it differs, spike S10).
6. **The installer and an end-to-end deploy on a VM:** git, the `houston` CLI, the runner token, and the Kamal image on the server; then `houston deploy` of the spike fixture through a real Mission Control.
7. **Mission Control pages:** project detail (facts, write-only secrets with Generate, deploy history), the deploy page (steps, log), and projects on the flight board.
8. **Real run on svnmns.com:**
   - equip and a Postgres fixture at `<name>.svnmns.com` through the tunnel
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

## Batch 3: deploy records

### Design (short)
- **`Deploy`:**
  - `project_id`, `number` (per project), `sha` (40 hex), `ref` (≤ 255)
  - `status` (`in_flight` | `go` | `no_go`), `step` (≤ 100), `log` (text, capped), `error` (≤ 1000)
  - `token_digest` (SHA-256 of a random token)
  - `heartbeat_at`, `finished_at`, timestamps
  - Indexes: unique `[project_id, number]`, and a **partial unique index on `project_id WHERE status = 'in_flight'`**.
- **`POST /api/projects/:name/deploys`** `{sha, ref}` → 201 `{id, number, token, took_over}`. One transaction (Rails 8.1's SQLite transactions are `BEGIN IMMEDIATE`, so this read-check-write is serialized):
  - A fresh in-flight deploy (heartbeat within **2 minutes**) → 409 naming it.
  - A stale one → marked `no_go` with "abandoned: no word from houston deploy since …", and returned as `took_over: <number>`. Batch 4 releases Kamal's lock only then (spike S9).
  - Number = max + 1. The indexes back the transaction up: `RecordNotUnique` → 409.
- **`PATCH /api/deploys/:id`** with `X-Houston-Deploy-Token` and `{step?, log?, status?, error?}`:
  - **Ownership:** the token must match (else 403, nothing written), and the deploy must still be `in_flight` (else 409 "no longer in flight", nothing written). Both are checked in the same transaction as the write. This is the synchronous ownership proof at finalize: a process whose deploy was taken over can't append to it or finish it.
  - Every accepted PATCH moves `heartbeat_at`.
  - `status` `go` / `no_go` sets `finished_at`. After that, the record never changes.
  - **Log:** a chunk > 256 KiB → 413. The total is capped at 4 MiB; the chunk that crosses it is cut and followed by one "[log truncated …]" line, and later chunks are dropped, while step and status still apply.
- **Stuck-alive:** houston deploy heartbeats while it works, so a hung Kamal would stay IN FLIGHT forever. Named home: Batch 4, where houston deploy has an overall deadline (it stops Kamal and reports `no_go`). The deploy page (Batch 5) shows the elapsed time.

### At-least-once / ownership template
```text
Enqueue site: POST /api/projects/:name/deploys (houston deploy; runners in build step 4)
Exclusive claim: the in_flight row itself, one per project (IMMEDIATE transaction + partial unique index)
Enter preconditions: no in_flight deploy for the project, or only a stale one (heartbeat older than 2 min)
Duplicate delivery: two houston deploys at once → one 201, one 409; the effect (a Kamal deploy) runs once
Process kill mid-work: heartbeat stops → the next deploy takes over after 2 min and releases Kamal's lock
Re-check before finalize: PATCH checks token + in_flight in the same transaction as the write
TOCTOU: A is taken over while still running → A's next PATCH gets 409, and A stops (Batch 4)
Repair when finalize is refused: nothing to repair; the new owner's deploy is the record
Replacement only after release: takeover finalizes the old row (no_go) in the same transaction that creates the new one
Ownership token: random 32 bytes, SHA-256 digest stored, compared in constant time
Stuck-alive: Batch 4's deadline in houston deploy
```

### AC ↔ test map (Batch 3), `mission_control/test/…`
| # | Acceptance criterion | Test | Lens |
|---|---|---|---|
| 1 | Create → 201, number 1, a token (≥ 43 chars), a digest not equal to the token; the next deploy after it finishes is number 2 | `integration/api_deploys_test.rb` `test "a deploy gets the next number and a token"` | Contract |
| 2 | Unknown project → 404; sha not 40 lowercase hex, or a blank or 256-char ref → 422; no row | `test "a deploy needs a synced project and a real sha"` | Contract, Preconditions |
| 3 | A second deploy while the first is fresh → 409 naming #1, no row | `test "one deploy in flight per project"` | Preconditions, At-least-once |
| 4 | First silent for 3 min → the second is created; the first is `no_go` with "abandoned", `finished_at` set; `took_over: 1` | `test "a silent deploy is taken over"` | Crash & repair |
| 5 | Indexes: a second `in_flight` row for a project, or a duplicate number, raises `RecordNotUnique`; `in_flight` rows for different projects are fine | `models/deploy_test.rb` `test "the database allows one deploy in flight and unique numbers"` | Concurrency |
| 6 | PATCH with no/wrong token or another deploy's token → 403, no change; with `Cf-Ray` → 404 | `test "only the deploy's owner reports on it"` | Authz |
| 7 | PATCH step and two log chunks → appended in order; `heartbeat_at` moves | `test "progress appends to the log"` | Contract |
| 8 | PATCH `status: go` → finished; a later PATCH → 409 and nothing changes (log, step, status) | `test "a finished deploy doesn't change"` | Preconditions |
| 9 | After a takeover, the old deploy's PATCH with its own token → 409 "taken over", nothing written | `test "a taken-over deploy can't finish"` | At-least-once & ownership |
| 10 | Chunk > 256 KiB → 413; the total over 4 MiB is cut, with one marker; status still applies after the cap | `test "the log is capped"` | Scale |
| 11 | Invalid status (`done`), a step > 100 chars, or an error > 1000 → 422, no change | `test "progress is validated"` | Contract |

### Done (Batch 3)
- **Red:** all 11 rows failed for the missing model and endpoints. **Green:** 86 runs, 0 failures. Rubocop is clean on the 10 touched files, and the migration runs down and up.
- **Found on the way:** the API's 64 KiB body limit would have refused a 250 KiB log chunk. `json_body` now takes a per-endpoint limit (deploy PATCH: 256 KiB + 4 KiB).
- **Mutations, each caught:** any token owns the deploy (row 6); finished or taken-over deploys still writable (rows 8, 9); a live deploy taken over (row 3); no log cap (row 10).
- **Checked, not assumed:** Rails 8.1's SQLite adapter uses `BEGIN IMMEDIATE` by default (`sqlite3_adapter.rb:162`), so `Deploy.start!`'s check-then-insert is serialized. The partial unique index (row 5) is the backstop.
- The log is appended in SQL (`log = log || ?`), with the size read via `length(CAST(log AS BLOB))`, so a 4 MiB log isn't loaded on every chunk.

## Batch 4: `houston deploy`, the flow

### Design (short)
- **`internal/mission`:** a small client for the local API: `Sync`, `Secret`, `StartDeploy`, `Report`.
  - Base URL: `HOUSTON_URL`, default `http://127.0.0.1:3000`.
  - Token: `HOUSTON_TOKEN`, else `~/.config/houston/runner-token`, which the installer writes in Batch 6.
- **`internal/deploy`:** `Run(ctx, Options, Deps) int`; the CLI command `houston deploy [--ref <ref>]` wires it up. Its dependencies are interfaces (docker, git, the mission client), so the whole flow is tested with fakes and an `httptest` Mission Control.
- **Docker streaming:** `docker.Runner` gains `Stream(ctx, dir, env, out, args...)`, so each step's output reaches both the terminal and the deploy's log.
- **Steps, in order** (each is a `step` in Mission Control):
  1. **Check** (no Mission Control call, no docker):
     - the compose file loads
     - the worktree is clean (`git status --porcelain`, `.houston/` ignores itself)
     - the ref (`--ref`, else the current branch) resolves to `HEAD`
     - the deploy rule allows the ref: `commit` → `refs/heads/<branch>`; `tag` → `refs/tags/<t>` with `t` matching the glob
     - anything else exits 2
  2. **Sync:** HOLD → print the missing names, exit 1, and no deploy is started.
  3. **Start:** a 409 exits 1 with "deploy #N is in flight". `took_over` → `kamal lock release --version <sha>` first (spike S9).
  4. **Secrets:**
     - `kamal.ResolveSecrets(p, lookup)` fetches each referenced secret variable (a 404 is unset) and resolves composites with compose's own substitution plus the service hosts
     - each value goes through `kamal.CarrierValue` into `HOUSTON_S_<NAME>` in the environment of the Kamal container's `docker` process, never in its arguments
     - an unresolvable or uncarriable value → `no_go` before anything is built
  5. **Build:** `docker build --target production --label service=<name> -t 127.0.0.1:5000/<name>:<sha> -f <dockerfile> <context>` (the app service's `build`), then `docker push`.
  6. **Kamal files:** `.houston/kamal/config/deploy.yml` and `.houston/kamal/.kamal/secrets` from `kamal.Config` / `SecretsFile` (directory 0700, files 0600).
  7. **Accessories:** `kamal accessory boot all --version <sha>` (skips existing containers).
  8. **Release** (if `hooks.release`): `docker run --rm --name <name>-release-<sha7> --network kamal --env-file <0600 file> -v <volumes> <image> sh -c <cmd>`.
     - The env file is `kamal.AppEnv(p, values)`: exactly the app's environment.
     - It's deleted afterwards.
     - A failure → `no_go` "release hook failed (exit N)", and Kamal never runs.
  9. **Deploy:** `kamal deploy --skip-push --version <sha>`. A failure → `no_go`; Kamal leaves the old version serving.
  10. **post_deploy** (if set): `docker exec <name>-web-<sha> sh -c <cmd>`. A failure is logged, and the deploy is still GO.
  11. **Report** `go`.
- **The Kamal container:** `docker run --rm --name houston-kamal-<name> --network host -v <dir>/.houston/kamal:/workdir -v /var/run/docker.sock:/var/run/docker.sock -v $HOME/.ssh:/ssh:ro -e HOUSTON_S_… ghcr.io/basecamp/kamal:v2.12.0 <args>`.
- **Secret values never** appear in a command's arguments or in the log sent to Mission Control.

### AC ↔ test map (Batch 4)
| # | Acceptance criterion | Test | Lens |
|---|---|---|---|
| 1 | Happy path: sync → start → build (label, target, tag) → push → accessory boot → release → kamal deploy (`--skip-push --version <sha>`, the Kamal container's mounts) → post_deploy → report `go`; generated files 0600 in a 0700 directory | `internal/deploy/deploy_test.go` `TestDeployHappyPath` | Contract |
| 2 | Dirty worktree; a ref the rule doesn't allow (branch, tag glob); a ref not at `HEAD` → exit 2, no Mission Control request, no docker | `TestDeployChecksBeforeAnything` (table) | Preconditions |
| 3 | Sync HOLD → exit 1 naming the missing variables; no deploy started, nothing built | `TestDeployHoldsForMissingSecrets` | Preconditions, Signals |
| 4 | Start 409 → exit 1 "deploy #N is in flight"; nothing built | `TestDeployWhileAnotherIsInFlight` | At-least-once |
| 5 | `took_over` → `kamal lock release --version <sha>` before any other Kamal command | `TestDeployAfterTakeoverReleasesKamalsLock` | Crash & repair |
| 6 | Secrets reach Kamal only as `HOUSTON_S_<NAME>` env of the docker process (carrier-escaped); composite resolved; no value in any argument or reported log | `TestDeploySecretsTravelAsEnvironmentOnly` | Contract, Signals |
| 7 | Release hook exits 3 → `no_go` "release hook failed (exit 3)"; no `kamal deploy` | `TestDeployStopsWhenTheReleaseHookFails` | Crash & repair |
| 8 | `kamal deploy` fails → `no_go`; the report says the old version keeps serving | `TestDeployReportsAFailedKamalDeploy` | Signals |
| 9 | post_deploy fails → logged, report `go` | `TestDeployPostDeployFailureIsOnlyLogged` | Signals |
| 10 | No token anywhere → exit 1 naming `HOUSTON_TOKEN` and the file; a 401 → exit 1 "the runner token was refused" | `TestDeployNeedsTheRunnerToken` | Contract, Authz |
| 11 | `ResolveSecrets`: an exact variable shared, composites resolved with hosts, optional unset → "", `${X:?msg}` unset → an error naming X | `internal/kamal` `TestResolveSecrets` | Contract |
| 12 | `AppEnv` = the clear env plus each secret key's resolved value (aliases followed) | `internal/kamal` `TestAppEnv` | Contract, Parity |

### Done (Batch 4)
- **Red → green:**
  - rows 11–12 (`TestResolveSecrets`, `TestAppEnv`, plus `AppVolumes` in the same test)
  - the Mission Control client's contract (`internal/mission`: `TestClientTalksToMissionControl`, `TestClientErrors`, `TestFromEnvironment`), written against the Rails API's exact JSON
  - rows 1–10 (`internal/deploy`, plus `internal/cli` `TestDeployNeedsTheRunnerToken`)
  - The whole Go suite is green, and `go vet` is clean.
- **Found by the tests:**
  - compose-go uses a mapping's value even when the mapping says "unset", so `ResolveSecrets` returns "" for anything Mission Control has no value for.
  - My first leak check for a refused secret (`back\slash`) searched for "slash", which the (correct) message "a backslash" also contains. It now searches for the value itself.
- **Mutations, each caught:** dirty worktree allowed; release failure ignored; secrets passed in `docker run` arguments; a post_deploy failure failing the deploy; no lock release after a takeover; no carrier escaping.
  - The first attempt at the dirty-worktree mutation didn't compile (an unused variable) and falsely read as "0 failing". Mutations must compile to count.
- **`docker.Runner.Stream`** tees a command's output to the terminal and the deploy log. **`houston deploy [--ref]`** is wired into the CLI.

## Batch 5: `houston deploy` under pressure

### Design (short)
- **One run context.** Every docker command runs under it. It ends on:
  - **taken over:** any report answered 409, meaning another deploy owns the record
  - **lost touch:** no report has succeeded for **60 s** (`FenceAfter`)
  - **the deadline:** `--timeout`, default 30 min
- **Heartbeat:** every **15 s** (`HeartbeatEvery`), the reporter sends whatever log has built up, or an empty progress report, so Mission Control's `heartbeat_at` moves during a long step.
  - Log chunks are ≤ 200 KiB (Mission Control takes up to 256 KiB).
  - The reporter is safe for the step output and the heartbeat to use at once (a mutex).
- **Self-fencing** closes the race where Mission Control is unreachable but the deploy is still working. A takeover needs **120 s** of silence (`Deploy::STALE_AFTER`); the old process stops itself at **60 s** of failed reports. So the new owner's `kamal lock release` never frees a lock that a live Kamal still holds. A test pins `FenceAfter < 120 s`.
- **Stopping:** cancelling kills the docker CLI process, but not its container. So houston removes its own containers (`houston-kamal-<name>`, `<name>-release-<sha7>`), with a fresh context. Then, by cause:
  - **taken over:** exit 1 with "taken over"; no final report (it isn't ours) and no lock release (the new owner releases it).
  - **lost touch / deadline:** `kamal lock release` (we held it), a best-effort final `no_go` report, and exit 1.
- **Accessory drift (spike S10):** the generator labels each accessory `houston.config=<sha256 of its config>`, covering image, cmd, env (clear values and secret names), volumes, and options.
  - After `kamal accessory boot all`, houston inspects each accessory's label. A mismatch → `kamal accessory reboot <svc> --version <sha>` (its volumes are kept), logged.
  - A changed secret value alone doesn't reboot a database; changing a database's password needs work inside the database anyway.

### AC ↔ test map (Batch 5)
| # | Acceptance criterion | Test | Lens |
|---|---|---|---|
| 1 | A report answered 409 during Build → the run stops: no later step, its containers removed, exit 1 "taken over", no final report, no lock release | `internal/deploy` `TestDeployStopsWhenTakenOver` | At-least-once & ownership |
| 2 | Reports failing for longer than `FenceAfter` → the run stops: containers removed, `kamal lock release`, exit 1 "lost touch with Mission Control"; `FenceAfter` < 120 s | `TestDeployFencesItselfWhenMissionControlIsGone` | At-least-once (TOCTOU) |
| 3 | The deadline passes during `kamal deploy` → the Kamal container is removed, `kamal lock release`, final `no_go` "timed out after …", exit 1 | `TestDeployDeadline` | Stuck-alive |
| 4 | During a long step, heartbeats reach Mission Control at the interval | `TestDeployHeartbeats` | Contract |
| 5 | 700 KiB of step output → every report's log ≤ 200 KiB, and together they equal the output | `TestDeployLogChunks` | Scale |
| 6 | The generator labels each accessory with its config hash: present, changes with the image, unchanged by an app-only change | `internal/kamal` `TestAccessoryConfigLabel` | Re-entry |
| 7 | A drifted label → `kamal accessory reboot db --version <sha>` after boot; a matching label → no reboot | `TestDeployRebootsAChangedAccessory` | Re-entry |
| 8 | The reporter under `-race`, with step output and the heartbeat writing at once | `TestDeployHeartbeats` run with `go test -race` | Concurrency |

### Done (Batch 5)
- **Red:** row 6 failed against a stub. The blocking tests (rows 1–3) hung until the 10-minute test timeout, which is a red that shows nothing cancels a run. **Green:** the whole Go suite.
- **Row 8:** `go test -race -count=3 ./internal/deploy/ ./internal/mission/` is clean (CGO in the toolchain container). The timing-based tests passed 30 runs in a row.
- **Mutations, each caught:** a 409 not stopping the run; no self-fencing; containers left running after a stop (3 failures); no heartbeat (3); drift never rebooted; a taken-over deploy still unlocking and reporting.
  - The first round caught three of these only by hanging until the test timeout. The blocking tests now carry their own 2 s deadline, so they fail in seconds.
- **The expected configs** (`phoenix.deploy.yml`, `spike.deploy.yml`) now include each accessory's `houston.config` label.

## Batch 6: the installer, and an end-to-end deploy on a VM

### Design (short)
- **Installer** (`install/install.sh`, rerunnable):
  - installs `git` if it's missing
  - builds the CLI from `HOUSTON_SOURCE` (`docker build --target release`, which reuses the toolchain Dockerfile) and installs `/usr/local/bin/houston` with a `hou` link
  - generates `HOUSTON_RUNNER_TOKEN` into `/opt/houston/.env` once (appended on a rerun of an older install), and writes it to `~houston/.config/houston/runner-token` (0600, owned by houston) on every run
  - pulls the pinned Kamal image
  - Mission Control already reads `.env`.
- **`houston deploy` writes `.houston/.gitignore` (`*`)**, as `houston dev` does. Without it, the first deploy's `.houston/kamal` would make the checkout "dirty" and refuse the second deploy. (Found while planning this batch.)
- **`install/test/deploy-e2e.sh`** runs on an OrbStack VM with a real Mission Control. It marks Cloudflare connected in wildcard mode with base `houston.test`, since real Cloudflare is Batch 8. Then, as the houston user, in a git checkout of the spike fixture plus hooks:
  - **HOLD:** the first `houston deploy` exits 1, naming the missing secrets; the project now exists
  - **GO:** after setting the secrets, a deploy succeeds; the values arrive byte-exact through kamal-proxy; the release hook wrote to the data volume; post_deploy ran
  - **Zero downtime:** a new commit redeploys while a poller sees only 200s
  - **A failing release** (`exit 3`) → exit 1, and Mission Control has `no_go` "release hook failed (exit 3)". The previous version keeps serving (`KAMAL_VERSION`).
  - **Two deploys at once** → one refused with "in flight", the other GO
  - **Mission Control's records:** numbered, statuses as above, logs containing the step output

### AC ↔ test map (Batch 6)
| # | Acceptance criterion | Test | Lens |
|---|---|---|---|
| 1 | Installer: `git` and `houston --version` work; the token is ≥ 43 chars in `.env` and identical in houston's file (0600, houston:houston); a rerun keeps it | `install/test/orbstack.sh` (new checks) | Contract, Re-entry |
| 2 | Deploy writes `.houston/.gitignore` with `*` | `internal/deploy` `TestDeployKeepsItsFilesOutOfGit` | Re-entry |
| 3 | HOLD, then GO with byte-exact values, the release hook's volume write, and post_deploy | `install/test/deploy-e2e.sh` | Parity |
| 4 | Zero-downtime redeploy | same | Parity |
| 5 | A failing release → NO-GO, the old version serving | same | Crash & repair |
| 6 | Two concurrent deploys → one refused | same | At-least-once |
| 7 | Mission Control's deploy records match | same | Signals |

### Done (Batch 6)
- **Row 2:** `TestDeployKeepsItsFilesOutOfGit` went red, then green.
- **Rows 3–7, `install/test/deploy-e2e.sh` on Ubuntu 24.04 with a real Mission Control:**
  - HOLD named HOSTILE and POSTGRES_PASSWORD, and the project existed afterwards.
  - After the secrets were set (HOSTILE is the hostile value, `$(…)`, backticks, `$HOME`, `<%= %>`, café), deploy #1 was GO: every value arrived byte-exact through kamal-proxy; `spike-db` resolved; the release hook wrote to the data volume; post_deploy ran in the new container; the checkout stayed clean.
  - The redeploy (#2) saw 124 requests, all 200, and `KAMAL_VERSION` moved to the new commit.
  - The failing release (#3) was NO-GO "release hook failed (exit 3)", and `KAMAL_VERSION` stayed on #2's commit.
  - Mission Control's records were go, go, no_go, go.
- **The concurrency check failed first, for a script reason.** In `cd ~/spike && (a) & (b) & wait`, the `&` splits the list, so the second deploy ran in the Mac's working directory. It found this repo's own `compose.yml` and refused it (exit 2). Grouped with `{ …; }`, the same step on the kept VM gave exits 0 and 1, "deploy #5 is in flight".
- **Row 1** (installer checks in `install/test/orbstack.sh`) runs with Batch 8's full VM check. The e2e VM itself shows the installer works: the CLI, the token file, and Mission Control accepting the token.

## Batch 7: Mission Control pages

From the design's Main (flight board), ProjectDetail and DeployLog boards. Snapshots, the backup plan, Back up now, and the console hint belong to build step 5 (backups) and later. The design's runner name and step list follow spec §13's follow-ups.

### Design (short)
- **Flight board with projects:**
  - rows: STATUS / PROJECT (+ accessories) / RUNNING SHA / DOMAINS / LAST DEPLOY
  - status from the latest deploy: IN FLIGHT, NO-GO, GO, or **STANDBY** for a synced project that has never deployed (spec §13's proposal)
  - running SHA = the latest GO deploy's
  - the stats count projects, in flight, and NO-GO
  - The empty state is unchanged.
- **Project page** `/projects/:name`:
  - name + status chip, `<name>.<base>` and domains, facts (running SHA, deploy rule in words, services)
  - deploy history, newest first, 10 per page
  - **secrets:** one row per variable the file references, required first
    - set → `•••••••• · set <date>` with Replace and Remove
    - unset → a value field with Save, plus **Generate**, which saves 32 random bytes as base64url and never shows them
    - a HOLD notice while a required one is unset
  - No value is ever rendered. `Secret` validation (backslash, control characters) is shown inline.
- **Deploy page** `/projects/:name/deploys/:number`:
  - DEPLOY #N, status, `sha7 · ref`, the duration (or T+ while in flight), "X is still serving" while in flight
  - the steps (Secrets, Build, Accessories, Release, Deploy, Post-deploy), each done, current or failed, or pending
  - the log, HTML-escaped
  - While in flight, the page refreshes itself every 3 s (the existing refresh controller). Live streaming comes in build step 4.

### AC ↔ test map (Batch 7), `mission_control/test/…`
| # | Acceptance criterion | Test | Lens |
|---|---|---|---|
| 1 | Board rows: GO (running SHA = latest go), IN FLIGHT, NO-GO, STANDBY; stats count them; the empty state is unchanged with no projects | `controllers/projects_controller_test.rb` `test "the flight board lists projects by their latest deploy"` | Contract |
| 2 | Project page: facts, domains, history newest first and paged at 10; an unknown project → 404 | `controllers/project_pages_test.rb` `test "a project's page"` | Contract |
| 3 | Secrets: a set value is never in the HTML; set/unset rows; HOLD while a required one is unset | `test "secrets are write-only"` | Authz, Contract |
| 4 | Save: stored encrypted; a backslash value → 422 with the message and nothing stored; a key the file doesn't reference → 404; signed out → sign-in, nothing stored | `test "saving a secret"` | Contract, Authz |
| 5 | Generate: stores ≥ 43 random characters, not shown in the response; Remove deletes | `test "generate and remove"` | Contract |
| 6 | Deploy page: steps, status, log; in flight → refresher; finished → none; an unknown number → 404 | `controllers/deploy_pages_test.rb` `test "a deploy's page"` | Contract |
| 7 | A log containing `<script>` is escaped | `test "the log is text, not HTML"` | Contract (hostile input) |

### Done (Batch 7)
- **Red:** the 7 rows failed (routes, views and controllers missing). **Green:** 93 runs, 0 failures. Rubocop is clean on the 13 touched Ruby files.
- **Found on the way:**
  - A failed save of a new secret left the unsaved record in the association's cache, so the page took it for a set secret. The page now reads secrets fresh from the database, so only saved values count.
  - The design's Replace action was missing from my first secrets view. It's now a disclosure with an empty field.
  - My `.steps` class collided with the setup header's step indicator, which centered it. It's renamed `.deploy-steps`.
- **Mutations, each caught:** a secret's value rendered (2 failures); any key settable (1); board status ignoring the latest deploy (1).
- **Visual check:** the three pages were rendered with sample data by a throwaway test (deleted afterwards) and viewed at 1440 px: the flight board's rows, the project page with HOLD / Save / Generate / Replace / Remove, and the deploy page with its steps and log. A real browser session needs a sign-in, which I don't do with a password.
- Lists read deploys through `Deploy.summary`, which leaves out the log (up to 4 MiB each).

## Batch 8: real run on svnmns.com

### Design (short)
- **The full check (`install/test/orbstack.sh`) gains a deploy stage** after the flight board check. It runs on the real tunnel, in host-by-host mode, as the houston user:
  - **The spike fixture as `houston-spike-test`:** HOLD, then set secrets, then GO. Then, **through Cloudflare** from the Mac, `https://houston-spike-test.svnmns.com/env/HOSTILE` arrives byte-exact.
  - **A copy of equip as `houston-equip-test`** (`EQUIP_SOURCE`, a Houston-ready copy; your repo is never touched): `RAILS_MASTER_KEY` comes from the copy's `config/master.key` and is never printed. Then GO, with `/up` 200 through Cloudflare.
    - A redeploy while the Mac polls `https://houston-equip-test.svnmns.com/up` sees only 200s.
    - A commit that breaks the health path is NO-GO, and the old version keeps answering 200.
- **Guard:** `equip.svnmns.com` is live on your current server (200), so the test never uses a real app's name. Each test name must return the other server's 404 before its first deploy, or the stage stops.
- **Cleanup:** `cloudflare.sh cleanup` deletes every record whose comment starts `managed-by:houston` and that points at the test tunnel. That covers `admin.`/`hooks.` and the projects' own records, and nothing without the comment or pointing elsewhere.

### AC ↔ test map (Batch 8)
| # | Acceptance criterion | Test | Lens |
|---|---|---|---|
| 1 | The installer checks from Batch 6 row 1 | `install/test/orbstack.sh` | Contract |
| 2 | Spike: HOLD → GO; `https://houston-spike-test.svnmns.com/env/HOSTILE` byte-exact through Cloudflare | same, deploy stage | Parity |
| 3 | Equip: GO; `/up` 200 through Cloudflare | same | Parity |
| 4 | Equip redeploy: every poll through Cloudflare 200 | same | Parity |
| 5 | Broken health path → NO-GO; the old version still 200 | same | Crash & repair |
| 6 | Cleanup leaves svnmns.com as it was: the preflight matches the one before | `cloudflare.sh preflight` after | Crash & repair |

### Done (Batch 8)
- **`install/test/orbstack.sh ubuntu:noble` with `EQUIP_SOURCE`: PASS, 39 checks**, on the real svnmns.com tunnel in host-by-host mode:
  - **Row 1:** git, the `houston` CLI (`houston` and `hou`), the runner token (in `.env`, and 0600 houston:houston in the user's file, identical), and the Kamal image. A rerun keeps `.env` byte-for-byte.
  - **Row 2:** `houston-spike-test` held for HOSTILE and POSTGRES_PASSWORD, then went GO. `https://houston-spike-test.svnmns.com/env/HOSTILE` arrived byte-exact through Cloudflare.
  - **Row 3:** `houston-equip-test` (a copy of equip; `RAILS_MASTER_KEY` staged 0600 in `.houston/e2e`, read inside the VM, deleted, never printed) held, then went GO: `/up` 200 through Cloudflare after 11 s of edge lag.
  - **Row 4:** the redeploy saw 18 polls through Cloudflare, all 200.
  - **Row 5:** a broken health path was NO-GO "kamal deploy failed", and the previous version still answered 200. Mission Control's records: equip #1 go, #2 go, #3 no_go; spike #1 go.
  - **Row 6:** cleanup deleted `admin.`, `hooks.`, `houston-equip-test.` and `houston-spike-test.` (each managed-by:houston and pointing at the test tunnel) and the tunnel. The preflight afterwards matched the one before, and the live `equip.svnmns.com` still answered 200.
- **Found by the first real run (a test-script bug, not Houston):**
  - The staged equip copy excluded `storage/` entirely, `.keep` included. The image then had no `/rails/storage`, the volume's mount point was root's, and Rails (uid 1000) couldn't open SQLite ("unable to open database file" in the release hook). A real checkout keeps `storage/.keep` (Rails' `.gitignore`), so the script now copies the `.keep` files.
  - The failure itself behaved as designed: NO-GO "release hook failed (exit 1)", nothing switched.
- **Guard added before running:** `equip.svnmns.com` is a live app on the other server (200). Host-by-host mode would have made an explicit record for a test project named `equip` and taken that name over. The test names are `houston-*-test`, and the stage refuses to deploy a name that doesn't answer the other server's 404 first.

## Review (build step 3, scotty-review over `7b7f79f..HEAD`)
- **Cut:** `Deploy#label` and `Deploy::LABELS` had no caller (the view helper labels states). The Ruby pre-check for host clashes was cut earlier (Batch 2).
- **Cold pass:**
  - 4a (sync, deploy start and pages called twice) is covered by re-entry tests.
  - 4b: the dead API above is cut. Every Go export has a production caller in `internal/deploy`.
  - 4c: no inert flags.
  - 4d (stuck-alive) is covered by the deadline (Batch 5).
  - 4e: finalize ownership is checked in the writing transaction (Batch 3).
- **Follow-up (not blocking):** `houston deploy` assumes it runs as the houston user (SSH key at `~/.ssh`). Run as root, Kamal's SSH would fail with Kamal's own message. A check with a clear message belongs with the runners in build step 4, which run as houston by construction.

### Open questions
None blocking Batch 1. Recorded for Batch 2:
- **What "localhost only" means for the runner's secrets route.** Mission Control runs in a container, so its callers show up as Docker addresses, and cloudflared sits on the same network as the runners.
  - The plan: runner token (256-bit, never leaves the server) + refuse any request carrying `Cf-Ray` or `Cf-Connecting-Ip`, which Cloudflare always adds and clients can't remove.
  - This is a documented divergence from a literal source-address check.
- **CLI personal API tokens and `--server` from a laptop.** Their home is build step 4, with Settings › API tokens. Build step 3's `houston deploy` runs on the server with the runner token.
