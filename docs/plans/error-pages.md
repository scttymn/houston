# Plan: Error pages with the patch

## Your direction
- "houston needs a 404 page that has the logo. and the statement 'Houston, we have a problem...'"
- "any other error pages we should do before we do a tag?" Then: "go ahead, keep the problem line on the 404 and 500. The others can use your suggestions."

## Evidence
- **Before:** `mission_control/public/{400,404,406-unsupported-browser,422,500}.html` were Rails' stock pages. Rails serves them on Mission Control's own host: 404 for an unknown path or a missing record, 500 for a crash, 422 for a stale authenticity token, 400 for a malformed request, and 406 for an old browser (`allow_browser versions: :modern`, `application_controller.rb:4`).
- **Other hosts stay as they are.** A project's hostnames reach Mission Control only while the tunnel routes them there for maintenance (`app_host.rb`, `maintenance.rb`). Its blank 404 is only for a stale route, and "Back to the flight board" means nothing to an app's visitors. `hooks.<base>` is deliberately empty (`routes.rb`, `webhooks#not_found`).
- **Precedent:** the default maintenance page inlines `patch.svg` (`MaintenancePage::LOGO`), because it can't load anything.

## Design (short)
- `lib/error_pages.rb` holds one template and each page's line. `bin/rails error_pages` writes all five into `public/`. The pages load nothing (Mission Control may be what's broken), so the patch from `app/assets/images/patch.svg` is inlined, without its comment.
- They use Mission Control's colours: paper ground, ink text, a signal-red code line, the display face for the heading, one sentence, and an ink "Back to the flight board" button to `/`. The 406 has no button, because the browser can't use the board anyway.
- The fonts are fallbacks only (Barlow Condensed if installed, then Arial Narrow). The web fonts have fingerprinted URLs, so the pages can't link them.

| Page | Code line | Heading |
|---|---|---|
| 400 | ERROR 400 · BAD REQUEST | Houston, we have a garbled transmission... |
| 404 | ERROR 404 · NOT FOUND | Houston, we have a problem... |
| 406 | ERROR 406 · BROWSER NOT SUPPORTED | Houston, we're out of range... |
| 422 | ERROR 422 · REQUEST REJECTED | Abort, abort... |
| 500 | ERROR 500 · SOMETHING BROKE | Houston, we have a problem... |

## AC ↔ test map
| # | Acceptance criterion | Test (`error_pages_test.rb`) | Lens |
|---|---|---|---|
| 1 | Each page has its title, code line and heading, the inline patch (seven stars), and no link, script or img | every error page is the patch and its line | Contract |
| 2 | 400, 404, 422 and 500 link back to the board; the 406 has no link | a way back to the flight board | Contract |
| 3 | Each file is exactly what the generator writes, and the generator inlines `patch.svg` as it is, so a new logo or line fails until `bin/rails error_pages` is run | what bin/rails error_pages writes | Parity |
| 4 | An unknown path gets the 404, and an old browser (IE 11) the 406 | an unknown path … an old browser | Contract |
| 5 | No sideways scroll at 375 px | visual check, recorded here | Parity |

## Evidence
- **Tests fail first:** `ErrorPages` was undefined, the stock pages had no link back, and the stock 406 had no h1. Before the page existed, the first 404 test got "Action Controller: Exception caught" (the test env shows detailed exceptions, so the test switches them off as production does).
- **Mutation checks,** each restored afterwards:
  - Changing the 422 heading in `lib/error_pages.rb` without regenerating failed with "422.html is stale".
  - A one-digit colour change in `patch.svg` failed with "400.html is stale".
  - Removing `allow_browser` failed the 406 request test.
- **Tests:** Rails, 520 runs, 0 failures; rubocop clean. Rails and rubocop run with `docker compose run --rm --no-deps -T -e RAILS_ENV=test app sh -c 'bin/rails test && bin/rubocop'`.
- **Visual check:** the 404 was checked at desktop width, and the 404, 500, 400 (the longest heading) and 406 at 375 px. Every page has scrollWidth 375, so there's no overflow.

## After a logo change
Run `docker compose run --rm --no-deps -T app bin/rails error_pages`, then commit `public/*.html`.
