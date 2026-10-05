# Protecting public commits

This is a public repository. Review source, documentation, examples, filenames
and test fixtures before committing. Use `YOUR_*` placeholders, paths such as
`/ABSOLUTE/PATH/TO/PROJECT`, and addresses at `example.invalid`. Public source
citations and generic container paths such as `/home/node` are allowed.

Keep actual profiles in ignored `profiles.local.json` files. Keep credentials in
ignored `.secrets/` directories or an external secret store. Keep authentication
state, supplied tickets, job output, logs and host settings outside tracked
source. Tests must generate disposable keys and use clearly fake tokens.

## Install the pre-commit hook

From the repository root, check for existing hooks before setting the path:

```sh
git config --get core.hooksPath
git config --local core.hooksPath .githooks
```

If you already use another hooks directory, call this repository's
`.githooks/pre-commit` from your existing pre-commit hook instead. Do not replace
other checks. Each clone needs its own hook installation. The hook needs only
Git and Python 3; it requires no network access or credentials.

The hook scans **every staged file from the Git index**, including unchanged and
force-added files. An unstaged cleanup cannot hide sensitive staged content.
It rejects personal home/drive/share paths, non-example emails, recognised token
formats, private keys, literal credential assignments, concrete vault references
and local artifact filenames. Binary files and submodules require a separate
review; the check blocks them rather than silently skipping them. It reports
filenames, line numbers and categories without printing matched contents.

## Review before publishing

```sh
python3 scripts/check_sensitive.py --worktree
git diff --cached --stat
git diff --cached
python3 scripts/check_sensitive.py --staged
python3 scripts/check_sensitive.py --history
```

The working-tree check covers tracked and non-ignored pending files. Ignored
local files stay outside its scope unless staged. The history check covers all
reachable commits, including deleted files and unpublished ancestors. Git author
and committer identities are public commit metadata and are outside this content
check. Review your Git identity separately before creating commits.

History scanning also recognises the exact public Dependabot signing attribution
in a final commit-trailer paragraph. Only that line's email finding is exempt;
other emails and sensitive patterns remain checked. This exception never applies
to file contents, the index or the working tree.

The history check has one exact-content baseline for reviewed, illustrative vault
examples already published in an old `docs/options.md` version. It applies only
to that complete file's SHA-256 and finding category. It cannot exempt changed
content, other findings, staged files or the working tree. Current vault examples
use explicit placeholders.

The same index check runs in GitHub validation. Hooks can be bypassed, and pattern
checks cannot establish that a document is safe: manually inspect private
repository names, account settings, internal service details, work-ticket
content, screenshots and encoded data. Review the actual staged files even when
the check passes. Keep diagnostic output outside tracked source.

If a check reports a match, remove the private content or replace it with an
explicit placeholder. Add detection rules and focused tests when a new exposure
pattern appears. Do not add a broad exclusion for a file containing real data.
For fake test tokens, use a `fake-` or `test-` prefix; generate private keys at
runtime instead of committing key material.

If sensitive content is already committed locally, clean the unpublished commit
before pushing; a later deletion still publishes its earlier version. If a live
credential has reached a remote, revoke or rotate it first, then arrange any
history cleanup with repository maintainers. Do not force-push shared history
without agreement.
