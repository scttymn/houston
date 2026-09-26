# Plan: Settings on tablets and phones, with a pinned menu

## Your direction
- "I've updated settings style": the Claude Design canvas's `SettingsTablet` (768) and `SettingsMobile` (390) artboards.
- "Start with style only. Also as you scroll, the menu should be pinned so it's easier to navigate."

## Evidence
- **Today:** Settings is a 200 px title-and-menu column beside the sections (`settings/_nav.html.erb`, `.settings` in application.css). Below 960 px the column stacked above the sections and scrolled away with the page.
- **The design, `SettingsTablet`:** a 40 px title, then the sections as a row of tabs with a 2 px signal underline on the current one, then the panels (24 px titles). Storage and token rows stack as items: name and action, then the location, then labelled facts (HOLDS, USED BY, LAST WRITE).
- **The design, `SettingsMobile`:** a 36 px title, the sections as chips (the current one ink-filled), 20 px panel titles, facts in one column, and "Add" instead of "Add storage location".
- The artboards also draw content Settings doesn't have yet (Account, Export DNS, token rotate). That's left for later: this is style only.

## Design (short)
- **Desktop (1100 px and up):** unchanged, except the title-and-menu column is sticky, so the menu stays in reach.
- **Tablet (641–1099 px):** the side column dissolves (`display: contents`). The title scrolls away; the menu is a full-width row of tabs, sticky at the top, with the page's ground behind it. Sections leave room for it when jumped to (`scroll-margin-top`).
- **Phone (640 px and down):** the same pinned row, as chips. It scrolls sideways, and the current section's chip is kept in view as you scroll (`section_nav_controller#reveal`).
- **Rows as items:** CSS grid areas on the existing storage and token rows. The labels come from `data-label` on the cells, and the location's kind from `data-kind`, so the markup is shared by every width.
- **Add:** both labels in the button, CSS shows "Add" on phones (as the board does).

## AC ↔ test map
| # | Acceptance criterion | Test | Lens |
|---|---|---|---|
| 1 | The title and menu sit in `.settings__side`: the h1, then the labelled section nav with no h1 of its own | `general_controller_test.rb` (the Settings page structure test) | Contract |
| 2 | Storage rows label their facts (HOLDS, USED BY, LAST WRITE) and carry the location's kind | `storage_controller_test.rb` | Contract |
| 3 | The menu stays pinned while scrolling at 768 and 390 px; the current section is marked and in view; the desktop column is sticky | visual check, recorded here | Parity |
| 4 | No sideways page scroll at 1440, 768, 390 and 375 px | visual check, recorded here | Parity |

## Evidence
- **Tests:** Rails, 419 runs, 0 failures; rubocop clean (`docker compose run --rm --no-deps -T -e RAILS_ENV=test -e DISABLE_BOOTSNAP=1 app sh -c 'bin/rails test && bin/rubocop'`).
- **Visual check,** in the local preview:
  - 1440 px: as before, with the side column `position: sticky`; the page is 1440 wide.
  - 768 px: after scrolling, the tab row's top is 0 and it spans the page; Releases is underlined as its section comes up. Storage shows as an item with its labels. The page is 768 wide.
  - 390 px: the chips stay pinned and scroll themselves to the current section (Time zone at the end). "local" shows alone when a location has no path. The button says "Add". The page is 390 wide.
