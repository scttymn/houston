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

## Not yet
- The cap on the live server, and the flood through the tunnel that shows the other apps keep answering.
- The request log: storing it with Docker's `local` driver, or not at all, means recreating kamal-proxy, a moment's outage for every app. Measured first, on a throwaway machine.
