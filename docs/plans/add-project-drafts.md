# Plan: Add project starts clean, and says when it's updating a project

## Your direction
- "If I start to add a project, then cancel, and then add a project again, the previous entries are still visible."
- "Add project allows you to add the same repo more than once. I think that's fine; how does that work with the automatic subdomains?"

## What's there (evidence)
- **The draft outlives the page.** Check access creates a `RepoLink` draft (a URL plus a fresh deploy key) and keeps its id in the session (`session[:repo_link_id]`, `ProjectLinksController#access`). `GET /link` renders whatever draft the session points at. Cancel is only a link to the board (`new.html.erb:119`), so the draft stays. The next Add project shows its URL, key, branch and config path. Only Save clears it, and old drafts are dropped after a day (`RepoLink.start!`).
- **The same repo twice is one project, not two.** A project is keyed by its compose `name:`. Save runs `ProjectSync#save!`, which does `Project.find_or_initialize_by(name:)`. So linking equip's repo again with the same compose.yml **updates the existing equip project**, and its subdomains don't change. It replaces the project's branch, config path and deploy key with the draft's, but keeps the webhook secret (`ProjectLinking#webhook_secret`).
  - Nothing breaks: Read only succeeds if git fetched through the new key, so that key works. The old key is left unused on the git host.
  - The page doesn't say any of this: it reads "Add project" to the end.
- **Subdomains in the other cases:**
  - A different compose file in the same repo with a different `name:` (a staging file, say) is a separate project with its own subdomain.
  - A different repo that uses a name already taken is refused ("equip is already linked to …").
  - Two projects that claim the same domain are refused by the host index (`claim_hosts`).

## Goal
- Add project always opens empty, and Cancel throws the draft away.
- After Read, when the name is already a project, the page says Save updates it and doesn't create a new one.

## Design (short)
- **Cancel** becomes a small form: `DELETE /link` destroys the draft (it holds a private key) and clears the session, then goes to the board. It's idempotent: with no draft, it just goes to the board.
- **`GET /link` starts fresh:** it drops the session's draft, like Cancel. The draft lives only while you're on the page, stepping through its forms (those are POSTs that render in place).
  - The cost: leave the page midway and come back, and you get a new deploy key. If you'd already added the old one to the repo, you add the new one.
- **After Read**, if `Project.find_by(name:)` exists, a notice above Save says: "**equip** is already a project. Save updates it: this branch, config path and deploy key replace the current ones. Its subdomains and webhook stay as they are." The Save and Deploy buttons are unchanged.

## AC ↔ test map
| # | Acceptance criterion | Test (`test/controllers/project_links_test.rb`) | Lens |
|---|---|---|---|
| 1 | Cancel destroys the draft and clears the session; the next Add project is empty (no URL, no key) | `test "Cancel throws the draft away"` | Contract |
| 2 | Cancel with no draft (a second click, another tab) just goes to the board | same test, second `delete link_path` | Preconditions |
| 3 | Opening Add project again (`GET /link`) after Check access starts empty, and the old draft is gone | `test "Add project always starts empty"` | Contract |
| 4 | The in-page steps still carry the draft: Check access → Read → Save, as today | the existing `"saving links the project"` and `"reading compose.yml"` stay green | Whole batch |
| 5 | Reading a repo whose name is already a project says Save updates it, and names the project | `test "reading a project that exists says Save updates it"` | Honest surface |
| 6 | A new name shows no such notice | same test, the negative | Contract |
| 7 | Cancel needs the admin: signed out, nothing is destroyed | extend `test "linking needs the admin"` | Authz |
| 8 | Checked in a browser: start, Cancel, Add project shows an empty form; re-reading equip's repo shows the notice | the visual check, recorded here | Parity |

## Later (named, not built)
- Removing the old deploy key from the git host is the admin's job (Houston doesn't manage keys on the host).

## Evidence
- The suite: 313 runs, 0 failures; rubocop clean.
- Mutation check: keeping the draft on `GET /link`, forgetting it without destroying it, showing the notice for any project, and the wrong wording each fail a test.
  - At first, "any project" survived, because the negative case had no other project. The test now adds one (equip) first.
  - Leaving the session id after destroying the draft survives. That mutant is equivalent: the id then finds nothing.
- The visual check (rendered pages; 8):
  - Fresh `GET /link` shows only step 01, empty, with no Cancel.
  - After re-reading an existing project, the HOLD notice sits above Save, with the project name linking to its page. Cancel, Save and Deploy are unchanged, and Cancel is the same width as the old link.
  - At 375px there's no overflow, and the notice stays inside the 16px gutter.
  - A pre-existing wrap: at 1280px, Deploy falls to a second line beside the long "Required secrets…" note. It does the same with the old Cancel link.
