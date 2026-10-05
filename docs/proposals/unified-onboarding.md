# Proposed unified onboarding

Status: design proposal; no `sdlc setup` command exists yet. The current supported
route is the [user guide](../user-guide.md).

## What should change

Onboarding currently asks a new user to understand several independent stores:
the host executable and runtime image, two native provider caches, a GitHub cache
per account, signing profiles, a 1Password bootstrap file, account/key pairs and
per-checkout account selection. Each component has a useful security boundary,
but the user has to assemble the dependencies and distinguish local status from
connected verification. A sequence of separate commands exposes that internal
structure too early.

The manual 1Password work has a smaller irreducible core: create a dedicated
custom vault, generate a signing key, grant a Service Account read access to that
vault, save its token and register the public key with GitHub. Those actions
establish authority outside SDLC. A wizard can guide them and collect the results;
it should not acquire broad vault administration rights merely to automate them.
The existing signing setup already constructs the private profile, takes a token
through a hidden terminal prompt and supports `--replace`. Users should never
need to hand-write signing JSON for ordinary onboarding.

Make a single guided `sdlc setup` the next onboarding feature after validating
the remaining execution/series/concurrency cases. It should be available before
wider adoption. Documentation can fix the route now; a wizard needs its own
tested iteration because incorrect recovery can overwrite working credentials
or select the wrong account. Detached supervision and credential storage
hardening remain separate priorities, not promises a wizard can satisfy.

## Proposed stages

The host executable still needs a short bootstrap installation step. After that,
`sdlc setup` should resume these stages, calling the existing implementations:

| Stage | Guided action | Completion evidence |
| --- | --- | --- |
| Host/runtime | Check Git and Docker, build the image, prepare pinned sidecars; preview updates separately. | Local runtime/image inventory passes. |
| Implementation/review | Select the implementation provider and use each required official native login flow. | Local cache status, clearly distinct from remote/model access. |
| GitHub account | Create or select a named native login profile; explain organisation SSO approval if needed. | Connected native identity matches the intended account. |
| Signing | Choose the secret provider, create or select a signing profile, guide the external vault/key/Service Account steps, enter the token privately. | Valid local public-key identity and bootstrap permissions. |
| Signing verification | Retrieve the selected key once and sign/verify a disposable local commit, without publishing. | Timestamped connected verification outcome. |
| Public-key registration/pair | Guide registration of the public signing key, then pair it with the GitHub profile. | Account identity and registered signing key match. |
| Project | Inspect origin and effective Git name/email, initialise project settings and select the account/key pair for this checkout. | Reviewed checks/input paths and explicit repository selection. |
| First work | Find ticket order, select one ticket and preview the run. | A concrete plan; connected execution is a separate authorised action. |

Only 1Password is implemented today. Preserve the existing configurable secret
provider interface; do not imply an open-source backend is already available.
See the [alternative-provider proposal](signing-secret-providers.md).

## Navigation and recovery

Offer Back, Next, Resume, Cancel and Skip for optional stages. Show completed,
configured locally, verified at a stated time, skipped and needs attention as
different states. A later offline status must not silently turn a historical
verification into a claim of current service access.

Persist versioned, non-secret progress outside the project in private owned
storage. Store profile identifiers and completed-stage results, not tokens,
private keys, OAuth data or transcript copies. Validate saved state before reuse.
Use the same locking and ownership rules as existing profiles. A fresh candidate
configuration should be validated and saved atomically; navigating Back must not
partly replace a working profile.

For an existing profile, offer Reuse, Replace deliberately, or Create another
profile. Show which projects use a pair before changing it. Replacing a public
key requires renewed GitHub pairing and deliberate repository reselection.
Existing runs retain their frozen identities; setup cannot change them in place.

Cancel should stop the wizard and preserve completed setup. It must not log out,
revoke tokens, delete vaults/keys, or roll back external account changes. Explain
any external step that has already taken effect. Do not delete a bootstrap file
still referenced by another profile. After a bootstrap has been durably created,
reuse its private path on navigation or restart without displaying the token.
If a crash occurs before durable creation, re-entry is necessary. If creation
succeeds but profile replacement is interrupted or loses a concurrent update,
retain the unique private file, preserve the old profile, and report recovery.
Do not delete it without establishing that no profile references it.

SSO denial, provider restrictions and exhausted usage should produce a clear
stop. An absent optional review login may be skipped, with an explicit warning
that execution will pause before independent review can complete. Do not mark a
full unattended-to-human-review setup ready without the required review route.

## Reduce the 1Password work safely

Use one dedicated signing vault per intended credential boundary. A Service
Account's read permission covers items in the granted vault; a locator does not
restrict it to one item. Keep unrelated secrets and the backup Service Account
token outside that vault. Separate GitHub identities can use separate signing
vaults/tokens when they need independent revocation and blast radius.

Collect a vault ID and item ID rather than asking the user to construct an `op://`
URI. Accept a copied public key and compute/check its fingerprint locally. Keep
real locators hidden by default. They are not credentials, but can disclose vault
names and secret organisation; they belong in private local configuration, not a
shared project file. Never print a token, put it in a shell argument, or ask for
it in a chat message.

The current resolver reads the private host bootstrap file and supplies its token
over stdin to an isolated official `op` container. The child process needs the
token to authenticate; repository workers do not receive it. Each retrieval still
crosses the vault boundary. Status/navigation should use local metadata and a
previous verification record, with explicit re-verification rather than repeated
vault reads. Audit records and revocation belong in 1Password as well as local
diagnostics. Vault permissions are immutable: changed grants need a new Service
Account; rotating a token preserves the account's existing grants.

The existing bootstrap file is plaintext protected by ownership and mode 0600.
This is a tradeoff for unattended access, and falls short of 1Password's guidance
to avoid plaintext token storage. Host disk encryption helps at rest; it does not
protect against a process running as the same user or a compromised Docker
administrator. A future OS credential-store backend needs explicit unattended
unlock semantics, failure handling and tests. A wizard must not label current
storage encrypted or secure against host compromise. See the
[security boundaries](../../README.md#security-boundary-and-risks) and
[credential hardening proposal](credential-security-hardening.md).

Do not automatically create Service Accounts with an administrator token or give
SDLC access to the user's general vault. Public-key registration through a scoped
official GitHub command could be an opt-in later feature; the first wizard can
keep that one browser step. Private signing keys should never leave the existing
isolated resolver/publisher route.

## Delivery and acceptance

1. Implement the wizard as an adapter around current setup/login/status commands;
   preserve their scripted use and existing profile formats.
2. Add progress/navigation, safe reuse and replacement, and a final concise
   summary. Avoid duplicating authentication logic or a second credential store.
3. Test interruption and restart at every stage using disposable offline data.
   Test invalid references, two accounts, shared bootstrap paths, cancellation,
   stale verification, ownership failures and concurrent configuration changes.
   Exercise crashes before token entry, during persistence, after bootstrap
   creation and during atomic profile replacement; recovery must neither leak a
   token nor overwrite the old profile or a shared bootstrap.
4. Perform an explicitly authorised connected onboarding trial for a fresh user
   profile. Verify the public signing key registration and unattended signing;
   no routine prompt should request 1Password desktop approval.

Acceptance: a user follows one guided route after installation, can return to a
mistake without handwritten JSON or repeating successful logins, and reaches a
correct ticket preview. No secret enters arguments, logs, Git or build contexts.
The final summary states which checks were local, which were connected and when,
what remains incomplete, and that the host controller still needs to stay alive.

## Primary references

- [1Password Service Account setup and immutable grants](https://www.1password.dev/service-accounts/get-started)
- [1Password Service Account management and rotation](https://www.1password.dev/service-accounts/manage-service-accounts)
- [1Password SSH key management](https://www.1password.dev/ssh/manage-keys)
- [GitHub CLI login and credential storage](https://cli.github.com/manual/gh_auth_login)

These sources inform the design; they do not imply endorsement of SDLC or erase
the storage and host-trust limitations above.
