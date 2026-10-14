# The tunnel's CPU, and what kamal-proxy costs

## Direction
From the gantry performance work (gantry's `docs/plans/performance.md`, Batch 4): loaded through the whole front door on a real Houston server (8 cores, a Ryzen 9 7950X VM), asked for 10,000 requests a second when the app behind could answer 7,500, cloudflared took 6.5 of the 8 cores and passed next to nothing. Every app on the server shares those cores: one app flooded is all of them slowed. And kamal-proxy costs each request more CPU than the app it fronts. Asked: cap the tunnel, and measure the proxy.

## Evidence
- `install/install.sh` `write_compose`: the cloudflared service has no `cpus`; apps have theirs from their compose files (`deploy.resources.limits`).
- `internal/kamal/kamal.go` `config`: the deploy's `proxy:` sets hosts, the app's port, the health check and `run.publish`; buffering and the logged headers are Kamal's defaults (the deploy runs `kamal-proxy deploy ... --buffer-requests --buffer-responses --log-request-header=Cache-Control --log-request-header=Last-Modified --log-request-header=User-Agent`).
- kamal-proxy v0.9.2 logs every request (no flag turns it off), to Docker's `json-file` log.

## Design
The installer gives cloudflared a quarter of the server's cores, at least one (`cpus` in `/opt/houston/compose.yml`). At 1.5 cores it passed 7,000 requests a second, so 2 of 8 is room for about 9,000 for every app together; past that the tunnel is slow, not the machine. Nothing to set: it's the server's cores, read when the installer runs (an update reruns it).

## AC ↔ test map
| AC | Test |
| --- | --- |
| cloudflared gets a quarter of the cores, at least one (1, 2, 4, 8, 16, 48 cores) | `install/test/install-tunnel-cpus.sh` |
| Nothing else the installer writes gets a limit | `install/test/install-tunnel-cpus.sh` |
| A flood through the tunnel leaves the other apps answering | on the server, with the cap: not yet run (see below) |

## Measured: kamal-proxy (2026-10-14)
twelve-dollar (gantry v0.15.1, 1 CPU), loaded from the server through kamal-proxy by its name at 8,000 requests a second, three 20 s runs each, CPU from each one's cgroup. "Docker's log" is `docker.service` and `containerd.service`: what storing the proxy's request log costs.

| kamal-proxy's route | kamal-proxy | the app | Docker's log | p95 |
| --- | ---: | ---: | ---: | ---: |
| as deployed: buffered, 3 headers logged | 181–186 µs | 124–126 µs | 53 µs | 0.8–1.3 ms |
| not buffered, 3 headers | 196–205 µs | 122–124 µs | 50–51 µs | 1.1–1.6 ms |
| not buffered, no extra headers | 193–199 µs | 124–125 µs | 51–52 µs | 0.9–1.1 ms |

- **Buffering isn't the cost:** without it the proxy costs about 8% more, not less. Kamal's default stays.
- **The extra logged headers cost nothing measurable.**
- **The request log costs Docker about 53 µs a request:** a fifth of what the proxy and its log cost together, a third of a core at 8,000 requests a second. That's the log's storing alone; writing it is inside the proxy's own figure.
- **kamal-proxy costs each request about 1.5 times what the app does**, and no flag of its own changes that: it's two HTTP connections' work a request, and the log.
- Another live app (vccmenu), asked every 6 s from a laptop meanwhile: 32 answers in 50 to 70 ms, one in 4 s, the laptop's wifi's known lost connection attempt (gantry's plan, Batch 4), not the server's.

The route was put back as deployed after (`kamal-proxy list`: running, its target unchanged).

## Measured: the cap on the live server (2026-10-14)
`docker update --cpus 2 houston-cloudflared-1` (what the installer will write), then 10,000 requests a second at twelve-dollar for 20 s, from the server out to Cloudflare and back through the tunnel, gzip asked for as a browser does. Watched every ~3 s: the server's busy cores, each container's CPU, and another live app (vccmenu) through the tunnel and straight to its container.

- The runaway didn't come back: the tunnel peaked at 0.8 of a core (under its cap of 2), kamal-proxy 0.6, the app 0.5, the server at most 3.6 of 8 cores busy.
- vccmenu answered throughout: through the tunnel in 30 to 40 ms (once 114 ms), straight to its container in under half a millisecond.
- 145,658 of the 200,000 requests failed, p50 9.3 s, with the server mostly idle: what held the flood back was outside the server (between it and Cloudflare, or at Cloudflare: not looked into).

So the cap is on and harmed nothing, and whether it holds a runaway is unproven: there was none to hold. It stays on the live tunnel until the container is recreated; the next release's installer writes it for good.

## Measured: storing the proxy's log (2026-10-14)
Not on the server (changing the driver recreates kamal-proxy, a moment's outage for every app): on an M5, in Docker, kamal-proxy v0.9.2 on one core in front of gantry's bench app on another, its route set as Houston's deploys set it, 8,000 requests a second for 20 s, three runs a driver. "Docker" is dockerd, containerd and every container's shim, so it has a floor that isn't the log's.

| The proxy's log driver | kamal-proxy | Docker |
| --- | ---: | ---: |
| `json-file` (now) | 62.5–65.8 µs | 22.3–23.9 µs |
| `local` | 62.4–63.0 µs | 23.8–24.2 µs |
| `none` | 61.2–61.7 µs | 12.3–12.8 µs |

- `local` stores it for no less than `json-file`.
- Not storing it at all saves about 11 µs a request on this machine (some 20 on the server, at its speed): an eighth of what the proxy and Docker cost together. And there'd be no `docker logs kamal-proxy` to look in when something goes wrong. Nothing in Houston reads that log; a person does.
- So it stays as it is. What the proxy costs is the proxy's: no setting of its own, or of Docker's, takes much off it.
