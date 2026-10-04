# Set up 1Password signing for SDLC

Use this guide to provision a lasting signing profile for `personal`, `work`, or
another named GitHub profile. The earlier [1Password test](1password-test.md)
records disposable access and signing trials; its test vault, shell token and
GitHub App examples are not the final installation procedure.

SDLC's setup wizard saves local configuration. You create the vault, Service
Account and key in 1Password yourself. Setup makes no network request and cannot
confirm the account's grants or whether the referenced item exists. Official
1Password instructions linked below were checked on 4 October 2026.

## 1 Create a dedicated vault and signing key

[Create a custom vault](https://support.1password.com/create-share-vaults/) for this automation, with access limited to its intended
owner or administrators. Keep unrelated secrets out of it. Service Accounts
cannot access built-in Personal, Private, Employee or default Shared vaults.
See [Service Account requirements](https://www.1password.dev/service-accounts/get-started).

In the unlocked 1Password desktop app, select **New Item → SSH Key**, then
**Add Private Key → Generate a New Key**. Choose **Ed25519**, generate the key,
give the item a clear automation name and save it. Confirm the item is in the
dedicated custom vault. If the app initially saves it in a built-in vault, move
it to the custom vault before continuing. Use a new automation key rather than
a personal authentication key. See [SSH key generation](https://www.1password.dev/ssh/manage-keys)
and [moving items](https://support.1password.com/move-copy-items/).

Copy the item's **public key** and note its **SHA256 fingerprint** for setup.
Leave the private key in 1Password: the official CLI retrieves it in OpenSSH
format when signing is needed. The unattended route requires a key it can use
without an interactive passphrase prompt.

## 2 Create the Service Account

On 1Password.com, open the Service Account creation wizard from **Developer**.
Give the account a clear automation name, select only the dedicated vault, and
use that vault's settings icon to grant **Read Items** only. Leave vault creation,
write/share permissions and unrelated vaults or Environments disabled. Save the
Service Account token in a separate private vault when it is shown; it is shown
only once. Keep that recovery copy outside the automation vault.
See [creating a Service Account](https://www.1password.dev/service-accounts/get-started).

**Read Items covers the entire selected vault.** An item reference chooses what
SDLC reads; it does not reduce the token's vault-wide authority. Review the grant
in 1Password yourself. SDLC's wizard and verification command cannot attest that
no other vault was granted.

Vault access and permissions cannot be edited after creation. If they are wrong,
create a replacement Service Account with the correct grant. Token rotation
retains the existing permissions. See [Service Account management](https://www.1password.dev/service-accounts/manage-service-accounts).

## 3 Identify the key field

The wizard asks for the vault and SSH Key item's **names or IDs**. Prefer IDs to
avoid ambiguity after renames. Enter each value directly at its prompt, without
shell quotes. SDLC constructs this field reference:

```text
op://YOUR_VAULT/YOUR_SIGNING_KEY/private key?ssh-format=openssh
```

`private key` selects the SSH Key field; `ssh-format=openssh` requests the format
used by Git's SSH signing. Names and IDs are both supported by 1Password; use IDs
when names contain unsupported reference characters. See [secret references](https://www.1password.dev/cli/secret-references)
and [reference syntax](https://www.1password.dev/cli/secret-reference-syntax).

To inspect the reference in the desktop app, enable its 1Password CLI integration,
open the key item, use the menu beside the private-key field and select
**Copy Secret Reference**. Copy the reference, not the private-key value. The
wizard still takes the separate vault and item names or IDs; it supplies the
field and format suffix itself. See [copying a field reference](https://www.1password.dev/cli/secret-reference-syntax#with-the-1password-desktop-app).

The reference is not a credential by itself. It can reveal vault and item names,
so keep actual references and account configuration private in this public
repository. Placeholder templates are suitable for documentation. Public keys
and fingerprints can be published intentionally; private keys and bearer tokens
cannot.

## 4 Save the local signing profile

Run the wizard in an interactive terminal, choosing the same profile name you
will use for GitHub:

```sh
sdlc signing setup --profile personal --provider 1password
```

`--provider 1password` makes the secret backend explicit; it is also the default
and the only supported value. This option is separate from `--provider codex` or
`claude` on engineering commands. Unsupported signing providers stop before any
token read or provider request. An [open source alternative](proposals/signing-secret-providers.md)
is planned; no alternative product has been selected.

Follow its prompts for the vault, item and public Ed25519 key. Enter the expected
SHA256 fingerprint from 1Password, or leave it blank to calculate it from the
public key. Calculation checks the supplied public key's fingerprint; connected
verification later proves the resolved private key matches it. Review the public
identity and private paths shown, then confirm whether to save them. Paste the Service
Account token only at the hidden token prompt. Do not put it in an argument,
shell command, source file, ticket or chat.

Alternatively, use an already prepared private token file:

```sh
sdlc signing setup --profile personal --provider 1password --bootstrap-file /PATH/TO/YOUR_PRIVATE_BOOTSTRAP
```

That file must be an absolute path outside Git repositories, owned by your user,
with mode `0600`, without symlinks or hard links. It must contain only the token.
The wizard checks the file and uses it as the bootstrap input.

With the hidden prompt, setup creates a `signing-personal-bootstrap` file and
`profiles.personal.local.json` in private installation state. The normal default
locations are:

| Host | Installation directory |
| --- | --- |
| macOS | `~/Library/Application Support/sdlc` |
| Linux | `~/.config/sdlc` (or the configured XDG config directory, followed by `sdlc`) |

Setup prints the exact saved paths locally. The configuration contains
`"provider": "1password"`, the reference, public key/fingerprint and bootstrap
path; the bootstrap contains the actual bearer token. Files are private, owned and mode `0600`; the installation
directory is private. Both stay outside source repositories and build contexts.
Setup never overwrites a bootstrap; `--bootstrap-file` deliberately reuses an
existing safe file. Setup also refuses to overwrite an existing profile. For an existing
profile, inspect it privately and use `signing configure` with an external private
JSON file when a deliberate configuration change is needed.

If bootstrap creation succeeds but saving the profile later fails or is
cancelled, setup retains the private token file and reports its exact path.
Another profile may already reference it. Inspect it locally, reuse it through
`--bootstrap-file`, or delete it only after confirming it is unused. A failed
write inside bootstrap creation is cleaned up before success is reported.

The current CLI accepts older profiles with no `provider` field as 1Password.
New profiles record that field explicitly. Older binaries reject it as an unknown
field, so keep the current CLI when using a profile created by this setup.

**The bootstrap token persists in plaintext on the host.** This is not an OS
credential-store integration. Use private local storage and host disk protection;
a compromised host user or administrator can read it. 1Password recommends
against plaintext token storage; SDLC's current bootstrap is a documented
limitation of this unattended route, not protection against host compromise.
See [Service Account token guidance](https://www.1password.dev/service-accounts/get-started).

The profile fields have different roles:

| Setting | Meaning | Where it belongs |
| --- | --- | --- |
| CLI `--profile personal` | Selects the saved signing profile and matching GitHub login. It is neither a vault name nor a GitHub username. | CLI selection; use `--github-profile personal` for a run. |
| JSON `provider` | Selects the signing-secret implementation; currently `1password`. | Private installation profile. |
| JSON `id` | The wizard sets `personal-signing`, an internal signing identity label. It does not select a vault or key. | Private installation profile. |
| JSON `reference` | Selects the vault, SSH Key item and private-key field, for example `op://YOUR_VAULT_ID/YOUR_ITEM_ID/private key?ssh-format=openssh`. It grants no access on its own. | Private installation profile; keep real locators out of this public repository. |
| JSON `public_key` and `fingerprint` | The expected signer identity checked against the retrieved key. | Saved profile; public values can also be published intentionally. |
| JSON `bootstrap_file` | Absolute path to the host file containing the actual Service Account token. It is not a reference to the token's recovery item in 1Password. | Private installation profile; the referenced bearer-token file stays private and external. |

Signing setup is per GitHub profile, rather than per repository. Reuse the profile
with `--github-profile personal` across repositories that account can publish to.
No bearer token or signing profile belongs in a project's `.sdlc/project.json`.
Placeholder references are suitable for committed examples; actual locators
remain private metadata even though they contain no secret value.

## 5 Inspect and verify

Before the connected check, build the shared runtime and pull the pinned official
`op` image as described in [runtime preparation](github-docker-test.md#1-reinstall-and-rebuild).
Plain signing status does not connect to either service.

```sh
sdlc runtime status --offline --github-profile personal
sdlc signing status --profile personal
sdlc signing status --profile personal --verify
```

`runtime status` includes a local signing summary for the selected GitHub profile;
it does not contact 1Password or expose locator metadata. A missing or unsafe
signing setup does not change the runtime image/dependency check's exit result.
Use the dedicated signing command when its readiness needs its own exit status.

Plain signing `status` is offline. It identifies the saved secret provider and
checks configuration, public identity and bootstrap-file safety; it does not prove vault access. Its normal output omits
vault/item references, storage paths and token contents.

`--verify` explicitly connects the official 1Password CLI to retrieve the selected
key, then checks its public key/fingerprint and signs and verifies a disposable
Git commit in a separate network-disabled container. It makes no GitHub or model
request. Success describes that invocation; SDLC does not save a lasting
“verified” status. `sdlc signing verify --profile personal` remains an alias for
this connected check.

Use this only when you want to inspect private metadata locally:

```sh
sdlc signing status --profile personal --show-config
```

`--show-config` displays the reference, bootstrap path and saved configuration
location. It never displays the token or private key. Keep its output out of
public transcripts. A missing profile or unsafe bootstrap needs local attention;
a denial, key mismatch or timeout during verification stops the signing route.

The token and reference reach the resolver through stdin; only the official `op`
child receives `OP_SERVICE_ACCOUNT_TOKEN` inside its container. The token is not
sent to provider workers, check workers or the GitHub publisher. The resolved key
reaches signing through stdin and temporary memory/tmpfs. Cleanup shortens its
lifetime but does not guarantee erasure from every buffer or host swap.

## 6 Register the public signing key and continue

In the intended GitHub account, open **Settings → SSH and GPG keys → New SSH key**,
choose **Signing key**, and add the dedicated public key. This registration is
separate from an SSH authentication key and from SDLC's GitHub login.
See [adding a GitHub SSH signing key](https://docs.github.com/en/authentication/connecting-to-github-with-ssh/adding-a-new-ssh-key-to-your-github-account).

Authenticate the matching GitHub profile separately and continue with the
[GitHub Docker trial](github-docker-test.md). Local signature verification does
not prove GitHub's Verified attribution or permission to publish a repository.
Repeat provisioning with a separate key and suitable vault/account grant for
`work` when needed.

Revoke a compromised Service Account to stop future reads; this cannot recall a
key already copied. Remove the GitHub signing key and revoke other affected
credentials separately. The [credential boundary](github-credentials.md) explains
what a compromised resolver, publisher or host could reach.
