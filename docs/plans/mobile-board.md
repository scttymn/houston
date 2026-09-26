# Plan: the phone flight board and menu, as designed

## Your direction
- "Take note of the mobile design. Implementation doesn't match the design."
- Then: "I've reworked the flight board designs from a mobile, tablet, and desktop perspective": the Claude Design canvas's `Main` (desktop 1440), `TabletFlightBoard` (768), `MobileFlightBoard` (390) and `MobileMenu` artboards.

## Evidence
- **Today, at 720 px and below:**
  - The top bar keeps the desktop nav (Projects, Settings), hides the brand's name and the status (application.css:241, :413), and keeps Sign out.
  - The board's rows collapse to one column of unlabelled cells (`.flight__row`, application.css, the 720 px block).
  - Add says "Add project", and the stats keep their desktop sizes.
- **The design, `MobileFlightBoard`:**
  - A 56 px bar: the brand with its name (a 30 px mark, 19 px HOUSTON, 9 px MISSION CONTROL) and a 44 px menu button (three lines).
  - A 30 px status strip under the bar: TUNNEL, REGISTRY, then the clock on the right. Mono 10 px, #B9B3A4 on ink, with a #2E323B rule.
  - The main area has 22/16/32 px padding and 18 px gaps. The eyebrow is 10 px and the title 46 px, with a 44 px "Add" button (plus icon).
  - The stats box is 2 × 2: 10 px labels, 30 px values, and inner rules.
  - One card per project:
    - A header linking to the project, holding its name (19 px), its services (13 px), the state pill (11 px) and a chevron.
    - Then labelled sections: DOMAINS, a two-column grid (RUNNING SHA, LAST DEPLOY, LAST BACKUP), and RESOURCES (the tick gauges) under a rule.
  - Then the "Add a project…" footnote.
- **The design, `MobileMenu`:**
  - The same bar, with a close (×) button, and the strip.
  - Projects and Settings as 56 px rows with chevrons. The current one is bold, with a 3 px signal bar.
  - A SYSTEMS box: Tunnel, Registry, and the version.
  - A full-width 48 px Sign out at the foot.

## Design (short)
- **The top bar:** on phones it shows the brand's name, a menu button and the strip, and hides the nav, the status and Sign out. On desktop, the menu button, the strip and the menu stay hidden. It's the same header on every signed-in page.
- **The menu:** a full-screen panel in the page (`#mobile-menu`, `hidden` until opened) with the design's rows, SYSTEMS and Sign out. A small Stimulus controller opens and closes it: the × button or Escape, with `aria-expanded` on the button.
- **The board:**
  - The phone cards are their own markup (`.flight-cards`), shown only at 720 px and below, where the desktop table is hidden.
  - The cells both use (domains, last deploy, last backup, resources) are shared partials, so nothing is worked out twice.
  - Each card header links to the project twice: the name, and a chevron with an `aria-label`. The state pill sits between them.
  - The Add button carries both labels, and CSS shows "Add" on phones.
- **Unchanged:** desktop and tablet (720–1100 px) layouts. The `MainTablet` artboard is a separate step.

## AC ↔ test map
| # | Acceptance criterion | Test | Lens |
|---|---|---|---|
| 1 | Every signed-in page's header has the menu button (aria-controls, aria-expanded) and the status strip (tunnel, registry, clock) | `projects_controller_test.rb` `test "the phone header: a menu button and the status strip"` | Contract |
| 2 | The menu: Projects and Settings (the current one marked), SYSTEMS (tunnel, registry, version), Sign out | same test, on the board and on Settings | Contract |
| 3 | A card per project: header links (name and chevron), pill, services; DOMAINS, RUNNING SHA, LAST DEPLOY, LAST BACKUP, RESOURCES with their values; not running: a dash | `test "each project has a phone card"` | Contract |
| 4 | Add has a short label for phones | same test | Contract |
| 5 | Visual check at 390 and 375 px against the artboards, and 1440 px unchanged; the menu opens and closes (button, ×, Escape) | recorded here | Parity |

## As built (all three artboards)
- **Breakpoints:**
  - Desktop, 1100 px and up: the table.
  - Tablet, 641–1099 px: `TabletFlightBoard`.
  - Phone, 640 px and down: `MobileFlightBoard` and `MobileMenu`.
- **Desktop:** the table takes the design's columns (90 / 170 / 80 px / domains 1.2fr / last deploy 1fr / 118 / 280 px, 20 px gaps, 20 × 24 px rows).
  - The status column is at least 90 px and grows to fit STANDBY (the artboard only draws GO).
  - Hostnames shorten with an ellipsis. Names wrap.
  - The gauges use the boards' sizes (10 px label, 34 px columns, 9 px gaps, 14 px amount).
- **Tablet:**
  - A 60 px bar (brand, nav, Sign out) and a 30 px status strip under it.
  - The title is 54 px, and the stats run across the full width.
  - Each card has a header link: name and services on one line, the pill, a chevron.
  - Its body is DOMAINS | RUNNING SHA, LAST DEPLOY, LAST BACKUP stacked | RESOURCES, in columns of 1fr / 190 px / at least 222 px. The last one grows to fit a limit's amount ("230 MB / 256 MB"), which the artboard's 222 px didn't hold.
- **Phone:**
  - A 56 px bar (brand, menu button) and the strip.
  - "Add", the 2 × 2 stats, and the same card, stacked: DOMAINS, a two-column grid, then RESOURCES under a rule.
  - The menu is a full-screen panel: the design's rows, SYSTEMS (tunnel, registry, version) and Sign out.
  - × or Escape closes it. Opening it focuses ×, and closing it focuses the menu button.
- **The eyebrow** names the base domain only, as all three artboards do. The version is in Settings › Releases and the phone menu. The update pills stay beside it (they only appear with news).
- **Shared cells:** domains, running SHA, last deploy, last backup and resources are partials used by the table and the cards. The services line is `services_words`.

## Evidence
- **Tests:** Rails, 419 runs, 0 failures; rubocop clean. The ones for this: `test "the phone header: a menu button and the status strip"` (on the board and Settings), `test "each project has a card for tablets and phones"`, and `test "the flight board's eyebrow names the base domain"`.
- **Visual check,** against the artboards, in a local preview with real stand-in containers:
  - No overflow at 1440, 1024, 768, 390 and 375 px, on the board, Settings and a project page.
  - The menu opens, closes (× and Escape), and moves focus as above.
  - The desktop hides the menu button and the strip.
