# Implement a ticket stream

Replace the placeholders with an explicit repository root and ordered ticket
paths before submitting this prompt. It uses the existing published skills;
their installed names may include a catalogue prefix. Invoke each named skill
where supported, or read its `SKILL.md` and follow it directly.

This template needs a harness with the repository, selected inputs and required
tools available. The current `sdlc interactive` command opens an empty workspace;
it cannot run this project workflow yet.

```text
Implement this ordered ticket stream in <REPOSITORY_ROOT>:

1. .sdlc/work/<WORK_REFERENCE>/tickets/<FIRST_TICKET>.md
2. .sdlc/work/<WORK_REFERENCE>/tickets/<SECOND_TICKET>.md
<ADD ONLY THE OTHER SELECTED TICKET PATHS, IN ORDER>

Coordinate the stream using the existing engineering-implement skill. Invoke it
once per ticket, sequentially. Follow repository guidance and each skill's
testing, verification, commit and local code-review gates. Do not change the
skills, agents, tickets or specification, or implement unselected work.

For the first ticket, agree and record the updated main starting SHA and create
its ticket branch. For each later ticket, create a separate branch from its
predecessor's verified delivery SHA, with the predecessor branch as its PR base.
Before each launch, supply the repository root, starting SHA, selected ticket
and linked input paths, destination branch, review fixed point and PR base.
Check ticket readiness and blockers; an ordered list does not resolve a blocker.
Do not start the next ticket until the current ticket's required checks, local
code review and implementation commits are complete and verified.

Use pr-draft and pr-manage to publish one draft PR per ticket when publication
is authorised and their requirements are satisfied. Keep the recorded stacked
bases. Never invent a tracker key to satisfy a naming rule. If a skill cannot
support the requested step, report the issue and proposed options before
changing or bypassing it. Do not force-push or rewrite published history without
explicit authority.

Require one published PR for every selected ticket; stop if publication is
blocked. Use pr-monitor to check every stream PR at its current base and head.
Only when the complete stack is published and all required CI checks pass,
return the PR links, branch/base/head revisions and verification results for
independent review by the other selected provider.
Do not merge. Repairs to an earlier ticket make affected descendants stale;
carry an authorised repair through them and reverify before handing off again.

If any agent needs a human answer, stop the whole stream until it is answered.
Also stop on a required-check failure that cannot be resolved within the selected
ticket, an unresolved blocker, missing input or tool,
unsupported provider use, policy refusal or usage limit. Report the affected
ticket, completed commits, exact problem and what is needed to resume. Keep
process inputs and private run output ignored under .sdlc/ and out of commits.
```
