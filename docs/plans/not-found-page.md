# Plan: A 404 page with the patch

## Your direction
- "houston needs a 404 page that has the logo. and the statement 'Houston, we have a problem...'"

## Evidence
- **Today:** `mission_control/public/404.html` was Rails' stock page, a big grey "404" and "Not Found" in red. Rails serves it for an unknown path or a missing record on Mission Control's own host.
- **Other hosts stay as they are:** a project's hostnames get an empty 404 when the project isn't in maintenance (`maintenance_pages_controller.rb`), and `hooks.<base>` answers only the webhook and the ping (`routes.rb`, `webhooks#not_found`). Both are deliberately empty.
- **Precedent:** the default maintenance page inlines `patch.svg` (`MaintenancePage::LOGO`), because it can't load anything.

## Design (short)
- `public/404.html` is static and loads nothing: no stylesheet, script or image. The patch from `app/assets/images/patch.svg` is inlined, without its comment.
- It uses Mission Control's colours: paper ground, ink text, a signal-red `ERROR 404 · NOT FOUND` line, the display face for "Houston, we have a problem...", one sentence, and an ink "Back to the flight board" button to `/`.
- The fonts are fallbacks only (Barlow Condensed if installed, then Arial Narrow). The web fonts have fingerprinted URLs, so the page can't link them.

## AC ↔ test map
| # | Acceptance criterion | Test | Lens |
|---|---|---|---|
| 1 | An unknown path answers 404 with the title, the h1 "Houston, we have a problem...", the inline patch (seven stars) and a link to `/` | `not_found_page_test.rb` (an unknown path) | Contract |
| 2 | The inlined patch is exactly `patch.svg`, so a new logo fails the test until the page is regenerated, and the page links nothing | `not_found_page_test.rb` (inlines the patch) | Parity |
| 3 | No sideways scroll at 375 px | visual check, recorded here | Parity |

## Evidence
- **Tests fail first:** before the page, test 1 got "Action Controller: Exception caught" (the test env shows detailed exceptions, so the test switches them off as production does), and test 2 didn't find the patch.
- **Mutation check:** "Page not found" in place of the h1 failed test 1; a one-digit colour change in `patch.svg` failed test 2. Both were restored.
- **Tests:** Rails, 518 runs, 0 failures; rubocop clean (`docker compose run --rm --no-deps -T -e RAILS_ENV=test app sh -c 'bin/rails test && bin/rubocop'`).
- **Visual check:** rendered at desktop width and at 375 px. At 375 px, scrollWidth is 375, so there's no overflow. The patch, the heading and the button fit in one screen.

## Regenerating the page after a logo change
Replace the `<svg …>…</svg>` in `public/404.html` with `patch.svg`, dropping its comment and adding `role="img" aria-label="Houston" class="patch"` to the opening tag.
