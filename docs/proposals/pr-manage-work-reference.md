# Let pr-manage carry a ticket key or work reference

Adopted upstream in published [skills commit 56e3897](https://github.com/tjpeel/skills/commit/56e38979baf389b058ae91c6812abae8d8dbcafa).
SDLC uses that revision. No local edits to the skills repository were made here.

The previously pinned [pr-manage skill](https://github.com/tjpeel/skills/blob/00a2426379eba03364736cf80fbe880de1b69df5/pr/manage/SKILL.md)
required a tracker ticket key for branches and PR titles. The engineering skills
also support work selected by an opaque work reference. Accept either supplied
identifier, and carry it into every PR produced for that work.

Change only three sections of `pr/manage/SKILL.md`:

1. **Branch naming:** Resolve the identifier from the selected input: a confirmed
   ticket key or an explicitly supplied work reference. Use
   `<branch-safe-identifier>-<short-kebab-description>`. Follow the existing
   engineering branch validation and rendering rules; preserve the original
   identifier. An already prepared branch must match the selected launch context.
2. **PR identity and description wording:** Require the title
   `<EXACT_IDENTIFIER>: <Concise description>`. Include the exact identifier in
   the body's existing equivalent field, or add `Ticket:` or `Work reference:`.
   Apply this to every proposed, created or edited PR for the selected work.
   Preserve a supplied associated work reference in the body when a ticket key
   is the selected title identifier.
3. **Create a PR:** Replace “ticket-formatted branch and title” with a reference
   to the confirmed identifier and branch naming rules above.

If the input has no identifier, disagrees with the supplied launch context, or
offers competing identifiers without selecting one, stop and report it. Never
derive a key from a ticket number, filename, branch or PR number. Treat identifiers
as literal data. If an exact identifier cannot be represented in a title, report
the issue rather than trimming, truncating or changing it.

For example:

| Selected identifier | Branch | PR title | Body field |
| --- | --- | --- | --- |
| Ticket `EX-123` | `EX-123-add-cache-lookup` | `EX-123: Add cache lookup` | `Ticket: EX-123` |
| Work reference `cache-read` | `cache-read-add-cache-lookup` | `cache-read: Add cache lookup` | `Work reference: cache-read` |
| Work reference `cache read` | `cache-read-add-cache-lookup` | `cache read: Add cache lookup` | `Work reference: cache read` |

Several tickets in a stream can share a work reference; their outcome
descriptions distinguish the branches and PRs. Each retains its supplied
predecessor base. Keep the current test, signing, publication-authority, draft,
metadata-edit and history-rewrite safeguards unchanged. Do not add an SDLC-specific
mode or change the catalogue structure.

Before adopting the change, check tracker-backed work still follows its existing
naming, tracker-free work preserves its exact reference, conflicting or missing
references stop before a write, and every stacked PR retains the identifier and
intended base. The published update covers these rules and also preserves the
existing scope of authority when editing PR fields.
