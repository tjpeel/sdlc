# Synthetic with OpenCode

Synthetic is an implementation provider alongside Codex and Claude. SDLC uses
the unmodified, pinned OpenCode CLI inside its Docker runtime. Inference goes
directly to Synthetic; LiteLLM is not required. Codex independently reviews
Synthetic implementations using the configured Codex review model.

## Install and authenticate

Build the updated runtime before selecting Synthetic. It includes OpenCode
1.19.3, skills and agent definitions from the pinned catalogue revisions.
Older recorded runtimes and saved runs remain readable, but do not gain the
OpenCode executable without a rebuild.

```sh
sdlc runtime build
sdlc auth login --provider synthetic
sdlc auth status --provider synthetic
```

Create an API key in your own Synthetic account, then paste it into OpenCode's
native terminal prompt. Do not put it in a shell command, model settings,
repository file or chat. The native client owns credential interpretation.
SDLC retains its opaque native cache in a private, provider-specific Docker
volume and creates a private snapshot for each invocation. Status checks run
without network access; finding a stored key does not prove account access,
quota or permission for a connected job.

```sh
sdlc auth logout --provider synthetic
```

Logout removes the local native Synthetic entry without contacting Synthetic.
Revoke a compromised key through Synthetic as well. No Codex or Claude login
is copied into an OpenCode session.

## Select a model

```sh
sdlc run --reference FEATURE --ticket 01-change.md --provider synthetic
sdlc interactive --provider synthetic
```

The initial Synthetic model is `hf:moonshotai/Kimi-K3`. Override it with
`--model`, or set the optional private `models.json` beside the runtime record:

```json
{
  "version": 1,
  "synthetic": {
    "implementation": {
      "provider": "synthetic",
      "model": "hf:moonshotai/Kimi-K3",
      "effort": "default"
    },
    "review": {
      "provider": "codex",
      "model": "gpt-6.1-sol",
      "effort": "high"
    }
  }
}
```

Preserve any existing Codex/Claude settings. Older model files inherit the new
Synthetic defaults. `default` leaves reasoning to the selected native model;
SDLC does not translate Codex effort levels into invented Kimi settings.
Synthetic supports normal speed only. `--alternate-providers` remains a
Codex/Claude feature. Interactive OpenCode permissions default to `allow`
inside the disposable Docker workspace; `--permission-mode ask` requests
native prompts instead.

Use an exact model identifier when repeatability matters. Synthetic's `syn:`
aliases can move to a different model; they do not establish a fixed served
identity. Model availability can also change. An unknown model or a reported
selection mismatch stops the job; SDLC does not substitute another model or
account. See [Synthetic's model catalogue](https://dev.synthetic.new/docs/api/models).

Synthetic implementation and its Codex review use a direct route. The saved
Headroom address continues to govern ordinary Codex/Claude runs. Saved runs
retain their recorded execution settings.

## Instructions, sessions and accounting

Each OpenCode invocation discovers its pinned skills and agent definitions
through an isolated native configuration directory. Repository OpenCode
settings, external plugins, MCP servers and alternate account configurations
are excluded. Installed delegation guidance contains the common rules and
only the selected CLI's details. OpenCode delegate bands describe task sizing;
they do not imply different models or unsupported reasoning settings.

SDLC uses OpenCode's authenticated loopback server API for JSON-schema output.
Only the native client sends authenticated inference requests to Synthetic.
Repairs resume the original native session and private database. Independent
Codex review uses a fresh session and its own login.

Usage comes from native assistant events, deduplicated by message ID and summed
across tool steps. Reports distinguish invocation totals from cumulative
session checkpoints. Root-session compactions are observed; delegate usage
coverage and current context occupancy can remain unknown. Catalogue byte
sizes describe available instructions, not tokens loaded by the model.

## Account terms and validation

Synthetic documents API-key use with
[OpenCode](https://dev.synthetic.new/docs/guides/opencode). Its current
[Terms of Service](https://synthetic.new/policies/terms-of-service), section
4.1(iv), also contain broad wording about programmatic extraction of output.
That wording is difficult to reconcile with the API guide; confirm the terms
applicable to your account and intended use before connecting it. SDLC does
not resolve that contractual ambiguity or approve an account's execution mode.

Development tests use disposable fake data and offline native fixtures. They
do not establish real account access, Synthetic-hosted model quality or
equivalence to the Codex implementor. A connected coding trial requires the
account owner's separate authorisation.
