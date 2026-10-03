# Provider usage rules

SDLC must respect provider terms, supported authentication routes and usage
limits. Review the current official rules when changing provider integration;
this document records the boundary reviewed on 3 October 2026. No implementation
can guarantee that a provider will never restrict an account.

## Codex

This iteration invokes the unmodified Codex CLI for device login and offline login
status. The authentication guide documents file-backed login caching and moving
that cache into Docker. SDLC stores the cache privately and gives it only to the
same user's local Codex container. See [Codex authentication](https://learn.chatgpt.com/docs/auth).

Login does not authorise every later use of the account. For ticket automation,
review the chosen account, repository trust and execution mode against
[non-interactive Codex guidance](https://learn.chatgpt.com/docs/non-interactive-mode).
API keys are the documented default for automation. Any account-based exception
must fit the documented conditions; its credentials must stay out of public or
untrusted execution environments.

Respect account access controls, usage policies and rate limits. SDLC must not
change identities or restart work to bypass a denial or usage cap. See
[OpenAI terms](https://openai.com/policies/eu-terms-of-use/) and
[usage policies](https://openai.com/policies/usage-policies/).

## Claude

Claude login is the next implementation slice. Anthropic explicitly permits an
end user to sign into the unmodified Claude Code binary with their own subscription,
including in a hosted container. It restricts third-party credential collection
and using subscription credentials to intermediate service access for other users.
See [Claude Code legal and compliance](https://code.claude.com/docs/en/legal-and-compliance).

Anthropic's [container guide](https://code.claude.com/docs/en/devcontainer#persist-authentication-and-settings-across-rebuilds)
documents a named volume for Claude Code's configuration and authentication cache.
SDLC will use this pattern: the official CLI reads, writes and refreshes its own
cache. SDLC will not copy, parse or export Claude tokens. Signing in again for
every container is unnecessary; re-authenticate when the provider requires it.

Account login and cache persistence do not approve every unattended use. Before
implementing ticket execution, review the account type and execution mode against
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

Future workers must stop on exhausted usage, unsupported credentials, access
denials, account holds and policy refusals. Any retry of a transient failure must
remain within the provider's documented limits. Credentials belong to one
authorised user; SDLC must not pool subscriptions or make their usage available
to other users.
