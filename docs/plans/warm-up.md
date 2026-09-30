# Warm-up: a deploy fills Cloudflare's cache before GO

## Direction
From Esther Pictures on gantry (2026-09-30): "Whenever we deploy and I run a page speed test, it's always a little slower as soon as the deploy is done. But if I wait a few minutes, it's faster." Tiered Cache is on in Cloudflare (your change); "Build the warm up".

## Evidence
- Each Esther deploy that day renamed `site.js` (a new fingerprint): Cloudflare's edge had never seen the new name, so the first request for it (PageSpeed's) went through the tunnel to the VM at home. The fonts and images keep their names; the images had been cached 12 hours.
- Cloudflare caches per location; PageSpeed runs from Google's. With Smart Tiered Cache, a location missing a file asks the upper tier near the origin, not the origin: a fetch from the server fills that tier.
- A deploy switches traffic in about a minute (tests 35 s, build and push 25 s, the switch 1 s); the new version serves at each of its hosts (`kamal.Config`'s proxy hosts: `<name>.<base>` and `x-houston.domains`).

## Design
- A **Warm-up** step after the new version serves (after the handover and the post_deploy hook), before GO. For each of the project's hosts it fetches `https://<host>/`; a host that answers anything but a 200 HTML page (www redirecting to the apex) is skipped, as its target is warmed as a host of its own.
- From each page, every file it asks for from its own site: `<link href>` (stylesheets, preloads, icons, the manifest), `<script src>`, `<img src>` and `srcset`, `<source srcset>`, `<video src|poster>`. Every width of a `srcset`: each device fetches another. Not other sites', not `data:`.
- Four at a time, at most 300 files, 30 s in all. It logs, per host, how many it fetched and how many Cloudflare says it had to fetch from here (`cf-cache-status` other than HIT), and any that failed. A warm-up never fails a deploy.
- `Deps.HTTP` is the client; nil (the tests') skips the step.

## Acceptance criteria → tests
| Criterion | Test |
| --- | --- |
| A page's own files are fetched, every srcset width, not another site's nor data: URLs | `TestWarmFetchesThePagesFiles` |
| A host that redirects is skipped; its target, a host too, is warmed | `TestWarmSkipsRedirects` |
| Cold files are counted (`cf-cache-status`), failures logged, the deploy unaffected | `TestWarmReports` |
| It stops at 300 files and at its deadline | `TestWarmLimits` |
| A deploy warms its hosts after it serves, before GO; without a client it doesn't | `TestDeployWarmsUp` |

## Evidence (2026-09-30)
- Tests first: the five above, on a fake web routed in the test's process (no network): the page's own files, every srcset width, not another site's or a data: URL; a redirecting host, a JSON one and one that's down skipped; cold files counted and a 404 named; 300 files at most, and a stop at the deadline; the step after Deploy, before GO, and none without a client.
- Suites: `bin/go vet ./...` and `bin/go test ./...` pass.
- Mutation check: 8 of 9 caught (no step; redirects followed; other sites fetched; one srcset width; HITs counted as cold; no cap; no deadline; 404s unreported). The survivor was a redundant check (a data: URL has no host, so the same-site check drops it): removed.
