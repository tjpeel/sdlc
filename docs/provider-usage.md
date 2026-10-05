# Provider usage rules

SDLC must respect provider terms, supported authentication routes and usage
limits. Review the current official rules when changing provider integration;
this document records the boundary reviewed on 3 October 2026. No implementation
can guarantee that a provider will never restrict an account.

## Codex

SDLC invokes the unmodified Codex CLI for device login, offline login status and
interactive terminal sessions and non-interactive ticket sessions through
`codex exec`. The authentication guide documents file-backed login caching and
moving that cache into Docker. SDLC stores the cache privately and gives it only to the
same user's local Codex container. See [Codex authentication](https://learn.chatgpt.com/docs/auth).

Interactive sessions use the ordinary [Codex CLI](https://learn.chatgpt.com/docs/codex/cli).
The client receives prompts directly from the account owner. Docker provides the
outer isolation boundary; native approvals default to `never` and can be set to
`on-request` for the session, following the
[container security guidance](https://learn.chatgpt.com/docs/agent-approvals-security).
No custom API client, credential broker or subscription sharing is involved.

Login does not authorise every later use of the account. Ticket runs are limited
to the account owner's local single-user native CLI job, using trusted inputs.
Review the chosen account, repository trust and execution mode against
[non-interactive Codex guidance](https://learn.chatgpt.com/docs/non-interactive-mode).
API keys are the documented default for automation. Any account-based exception
must fit the documented conditions; its credentials must stay out of public or
untrusted execution environments.

Respect account access controls, usage policies and rate limits. SDLC must not
change identities or restart work to bypass a denial or usage cap. See
[OpenAI terms](https://openai.com/policies/eu-terms-of-use/) and
[usage policies](https://openai.com/policies/usage-policies/).

## Claude

Claude login invokes the unmodified official client. Anthropic explicitly permits an
end user to sign into the unmodified Claude Code binary with their own subscription,
including in a hosted container. It restricts third-party credential collection
and using subscription credentials to intermediate service access for other users.
See [Claude Code legal and compliance](https://code.claude.com/docs/en/legal-and-compliance).

Anthropic's [container guide](https://code.claude.com/docs/en/devcontainer#persist-authentication-and-settings-across-rebuilds)
documents a named volume for Claude Code's configuration and authentication cache.
SDLC uses this pattern: the official CLI reads, writes and refreshes its own
cache. Interactive sessions invoke the ordinary
[Claude terminal CLI](https://code.claude.com/docs/en/cli-reference) with a selected
native permission mode, defaulting to `bypassPermissions` inside the isolated
container. See [Claude permission modes](https://code.claude.com/docs/en/permission-modes).
Ticket runs submit the account owner's selected work through native `claude -p`;
SDLC does not override managed restrictions.
SDLC does not extract token fields or use them for custom provider requests.
When the native client atomically refreshes its cache, SDLC preserves that opaque
file in the same user's private login volume, without decoding its contents.
Persistence works across Docker mounts; a failed transfer retains the private
session copy for recovery. Recovery refuses to overwrite a cache changed by a
later login or session; it preserves both generations for inspection. Signing in
again for every container is unnecessary;
re-authenticate when the provider requires it.

Account login and cache persistence do not approve every unattended use. Before
running a ticket, review the account type and execution mode against
the [Consumer Terms](https://www.anthropic.com/legal/consumer-terms),
[Commercial Terms](https://www.anthropic.com/legal/commercial-terms) and
[`claude -p` guidance](https://code.claude.com/docs/en/headless). API or supported
cloud authentication is the route for integrations outside permitted native-client
use. Ordinary end-user CLI login does not itself require an API key.

The locally built image already contains the published Claude CLI. Its presence
does not approve the archived prototype's subscription automation design.

## Development and execution

Use offline fixtures to test storage and status handling. Fake tokens must never
leave network-disabled test containers. Real login requires the account owner's
action through the official provider flow; SDLC does not automate browser sign-in
or implement token refresh itself.

Ticket workers must stop on exhausted usage, unsupported credentials, access
denials, account holds and policy refusals. Any retry of a transient failure must
remain within the provider's documented limits. Credentials belong to one
authorised user; SDLC must not pool subscriptions or make their usage available
to other users.

## Local ticket execution

`sdlc run` invokes the unmodified official clients in their documented headless
modes: [Codex non-interactive mode](https://learn.chatgpt.com/docs/non-interactive-mode)
and [Claude print mode](https://code.claude.com/docs/en/headless). It uses stored
native account login for the same user's local job. SDLC rejects CI execution;
this account route is not a service for other users, shared subscription broker
or general unattended deployment route. Use API or supported cloud authentication
for integrations outside the documented native-client account conditions.
Explicitly authorised supervised private trials have exercised this local route
with both implementation providers. Those results do not authorise other
accounts, execution modes or future connected tests.

The selected implementer must have login. Missing opposite-reviewer login allows
draft PR publication and CI before pausing at `awaiting_reviewer`. Review never
uses the implementation account as a substitute. Repairs resume the original
native implementation session; each independent review starts fresh. A reported
model mismatch stops execution; a missing model report cannot establish the
requested identity. Effort is a request that providers may cap.

Native provider caches remain accessible inside authenticated workers. Separate
check workers have no provider caches or host publication credentials, but a
prompt cannot prevent the provider's shell from running repository code or
reading its own cache. Use trusted repositories and selected inputs; network
destinations are not restricted. Private JSONL, diagnostics and native session
storage may retain sensitive material. Stop on policy refusals, access denials,
usage exhaustion and unsupported account routes; resume only after resolving the
cause without identity switching or limit evasion.
