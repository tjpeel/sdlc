# Test 1Password machine access and signing

Use this to prove dedicated-vault access and Git SSH signing in disposable Docker
containers while the desktop app is locked or closed. It is a manual prerequisite
test, not the unimplemented SDLC publisher. The host keeps a test Service Account
token in shell memory and sends it through stdin; the token is not saved in Docker
configuration, an image or a host file. It does not prove durable bootstrap after
host/controller restart. Checked against official documentation on 4 October 2026.

For ongoing GitHub access, prefer a GitHub App that issues short-lived installation
tokens. You do not need to create a PAT to test vault access or local signing here.
App tokens and commit signatures use different keys.

## 1 Create a dedicated test vault

In 1Password, create a custom vault such as `SDLC Test`, visible only to the
intended owner/admins. Service Accounts grant vault-level access, not a permission
on one arbitrary existing item. If a token should see one item, put only that item
in its granted vault. A separate human user is not required. Built-in Personal,
Private, Employee and default Shared vaults cannot be granted to Service Accounts.
See [Service Account scope](https://www.1password.dev/service-accounts/get-started).

Sharing an item by link creates a browser-accessible copy, not live CLI access to
the original item. It is not an unattended authentication route. See
[item sharing](https://support.1password.com/share-items/).

Add a Password item to the test vault with the password `SDLC_VAULT_OK`. This is a
disposable canary, not a real account password. Add a second canary to a different
vault with any disposable value; confirm in the app that it exists. The second
item tests denied access without attempting to read an unrelated real secret.

## 2 Generate a separate signing key

Generate a new Ed25519 key: **New Item → SSH Key → Add Private Key → Generate a
New Key → Ed25519 → Generate**. Name it for this test, save it and move the newly
created item into the test vault if it was created in your built-in vault. Record
its public key and SHA-256 fingerprint. See
[key generation](https://www.1password.dev/ssh/manage-keys).

Duplicating your existing SSH item copies the same key and fingerprint. Exporting
that copy would give the container the existing key's authority wherever it is
accepted. A fresh key can be revoked independently. Use it only for commit signing;
HTTPS cloning/pushing will use a GitHub token instead.

In the intended GitHub account, open **Settings → SSH and GPG keys → New SSH key**.
Select **Signing key**, give it a test-specific title and paste only the public
key. This registration is needed for GitHub's eventual Verified status; the local
test below can run before registration. See
[GitHub signing-key registration](https://docs.github.com/en/authentication/connecting-to-github-with-ssh/adding-a-new-ssh-key-to-your-github-account).

## 3 Create a read-only Service Account

On 1Password.com, open **Developer → Service accounts → Create service account**
or the linked creation wizard. Select only the test vault, grant **Read Items**
(`read_items`), and disable vault creation. Do not grant write/share authority or
access to personal/production vaults. Save the displayed token in your owner-only
vault, outside the vault granted to the Service Account. It is shown once. Grants
cannot be edited later; recreate the account to change its scope. See
[provisioning](https://www.1password.dev/service-accounts/get-started).

Use a bounded test lifetime if the creation flow offers it; CLI creation supports
`--expires-in`. Revoke the test token at the end. Keep the token out of this public
checkout and do not paste it into chat or a shell command.

## 4 Build a disposable probe image without credentials

In a new host terminal running zsh, create an empty directory outside repositories:

```sh
umask 077
SDLC_CREDENTIAL_TEST_DIR="$(mktemp -d "${TMPDIR:-/tmp}/sdlc-credential-test.XXXXXX")"
chmod 700 "$SDLC_CREDENTIAL_TEST_DIR"
cd "$SDLC_CREDENTIAL_TEST_DIR"
```

Create the following Dockerfile. The base is the
[official 1Password CLI image](https://hub.docker.com/r/1password/op), pinned to
version 2.39.0 and its inspected manifest digest. Nothing in this build requires
your token or key:

```dockerfile
FROM 1password/op:2.39.0@sha256:3cd5a1febc662c93d46b944b983b301e710da5c016ef63be9d436cf2b1ed30d5
USER root
RUN apt-get update && apt-get install -y --no-install-recommends \
    git openssh-client ca-certificates \
    && rm -rf /var/lib/apt/lists/*
COPY probe.sh /usr/local/bin/credential-probe
RUN chmod 0755 /usr/local/bin/credential-probe
USER opuser
ENV HOME=/tmp/sdlc-probe-user OP_CONFIG_DIR=/tmp/sdlc-probe-user/.op \
    GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null
ENTRYPOINT ["/usr/local/bin/credential-probe"]
```

Save this as `probe.sh` in that same directory. Its output contains only fixed
stage labels, not secret fields or account metadata. The official CLI exports
the private key into the container's tmpfs for this test; this is separate from
desktop SSH-agent signing. See [op read](https://www.1password.dev/cli/reference/commands/read).

```sh
#!/bin/sh
set -eu
umask 077
fail() { printf 'FAIL: %s\n' "$1" >&2; exit 1; }
IFS= read -r OP_SERVICE_ACCOUNT_TOKEN || fail 'missing bootstrap input'
test -n "$OP_SERVICE_ACCOUNT_TOKEN" || fail 'empty bootstrap input'
export OP_SERVICE_ACCOUNT_TOKEN
unset OP_CONNECT_HOST OP_CONNECT_TOKEN SSH_AUTH_SOCK
mkdir -p "$HOME" "$OP_CONFIG_DIR" /tmp/probe
trap 'rm -f /tmp/probe/key' EXIT

read_canary() {
    SDLC_CANARY="$(timeout 30 op read "$SDLC_SENTINEL_REF" 2>/dev/null)" \
        || fail 'test-vault read'
    test "$SDLC_CANARY" = SDLC_VAULT_OK || fail 'canary value'
}
read_canary
printf 'PASS: test-vault read\n'

SDLC_DENIED_STATUS=0
timeout 30 op read "$SDLC_DENIED_REF" >/dev/null 2>/tmp/probe/denied-error \
    || SDLC_DENIED_STATUS=$?
test "$SDLC_DENIED_STATUS" -ne 0 || fail 'unexpected outside-vault access'
test "$SDLC_DENIED_STATUS" -eq 1 || fail 'outside-vault operational failure'
if grep -Eqi 'network|connection|timeout|timed out|TLS|DNS|dial|lookup|rate.limit|quota|too many requests|HTTP.*(429|5[0-9][0-9])' \
    /tmp/probe/denied-error; then
    fail 'outside-vault operational failure'
fi
grep -Eqi 'unauthorized|forbidden|access denied|not authorized|not permitted|vault.*(not found|could not be found|not.*account|not.*access|does not exist)' \
    /tmp/probe/denied-error || fail 'outside-vault response needs private inspection'
read_canary
printf 'PASS: outside-vault canary unreadable; granted canary still readable\n'

timeout 30 op read "$SDLC_PRIVATE_KEY_REF" --out-file /tmp/probe/key \
    --file-mode 0600 >/dev/null 2>&1 || fail 'private-key retrieval'
unset OP_SERVICE_ACCOUNT_TOKEN
ssh-keygen -y -P '' -f /tmp/probe/key > /tmp/probe/public 2>/dev/null \
    || fail 'noninteractive key loading'
SDLC_FINGERPRINT="$(ssh-keygen -lf /tmp/probe/public -E sha256 | awk '{print $2}')"
test "$SDLC_FINGERPRINT" = "$SDLC_EXPECTED_FINGERPRINT" \
    || fail 'unexpected signing key'
printf 'PASS: expected key retrieved\n'

git init -q /tmp/probe/repo
cd /tmp/probe/repo
git config user.name "$SDLC_GIT_NAME"
git config user.email "$SDLC_GIT_EMAIL"
git config core.hooksPath /dev/null
git config gpg.format ssh
git config gpg.ssh.program /usr/bin/ssh-keygen
git config user.signingKey /tmp/probe/key
git config gpg.ssh.allowedSignersFile /tmp/probe/allowed-signers
printf '%s namespaces="git" %s\n' "$SDLC_GIT_EMAIL" \
    "$(cat /tmp/probe/public)" > /tmp/probe/allowed-signers
git commit --allow-empty -S -m 'Test unattended container signing' \
    >/dev/null 2>&1 </dev/null || fail 'signed commit'
git verify-commit HEAD >/dev/null 2>&1 || fail 'signature verification'
printf 'PASS: signed commit verified\n'
```

Build and inspect its public tool versions:

```sh
docker build -t sdlc-credential-probe:local .
docker run --rm --network none --entrypoint sh sdlc-credential-probe:local \
  -c 'op --version; git --version; ssh -V'
SDLC_CREDENTIAL_PROBE_IMAGE="$(docker image inspect sdlc-credential-probe:local --format '{{.Id}}')"
```

Use that immutable local image ID for the rest of the test.

## 5 Set private references and approved identity

Replace the placeholders below with the test vault/item IDs, signing fingerprint
and effective Git identity from the project where SDLC will run. IDs avoid
ambiguous names. The private key field reference needs `ssh-format=openssh`.
Keep these actual settings private; do not commit them here.

```sh
SDLC_SENTINEL_REF='op://YOUR_TEST_VAULT_ID/YOUR_CANARY_ITEM_ID/password'
SDLC_DENIED_REF='op://YOUR_OTHER_VAULT_ID/YOUR_OTHER_CANARY_ITEM_ID/password'
SDLC_PRIVATE_KEY_REF='op://YOUR_TEST_VAULT_ID/YOUR_SIGNING_ITEM_ID/private key?ssh-format=openssh'
SDLC_EXPECTED_FINGERPRINT='SHA256:YOUR_PUBLIC_KEY_FINGERPRINT'
SDLC_GIT_NAME='YOUR APPROVED NAME'
SDLC_GIT_EMAIL='YOUR VERIFIED OR ACTUAL NOREPLY EMAIL'
```

For the project's effective name/email, use `git -C /PATH/TO/PROJECT config --get
user.name` and the equivalent `user.email` command. Confirm that the outside-vault
canary really exists; a misspelled reference also fails and cannot prove the scope.

## 6 Load the token once and close the desktop app

Disable shell tracing, then enter the test token through zsh's hidden prompt:

```sh
set +x
read -rs 'SDLC_TEST_TOKEN?Paste the test Service Account token, then press Enter: '
printf '\n'
```

Do not export `SDLC_TEST_TOKEN`. Clear the clipboard after pasting. Lock or quit
desktop 1Password. Leave this host shell open: it holds the temporary bootstrap
token in memory. No `op signin` or desktop SSH-agent forwarding is used.

## 7 Run twice in fresh containers

Define and run this zsh function:

```sh
sdlc_credential_test() {
  printf '%s\n' "$SDLC_TEST_TOKEN" | docker run --rm -i --pull never \
    --read-only --cap-drop ALL --security-opt no-new-privileges \
    --log-driver none --pids-limit 128 --memory 256m --cpus 1 \
    --tmpfs /tmp:rw,nosuid,nodev,noexec,size=64m,mode=1777 \
    --env "SDLC_SENTINEL_REF=$SDLC_SENTINEL_REF" \
    --env "SDLC_DENIED_REF=$SDLC_DENIED_REF" \
    --env "SDLC_PRIVATE_KEY_REF=$SDLC_PRIVATE_KEY_REF" \
    --env "SDLC_EXPECTED_FINGERPRINT=$SDLC_EXPECTED_FINGERPRINT" \
    --env "SDLC_GIT_NAME=$SDLC_GIT_NAME" \
    --env "SDLC_GIT_EMAIL=$SDLC_GIT_EMAIL" \
    "$SDLC_CREDENTIAL_PROBE_IMAGE"
}
sdlc_credential_test
sdlc_credential_test
```

Both runs must print four PASS lines and require no desktop approval. The probe
rejects timeouts/network errors and requires a recognised denial/unavailable-vault
response. An unknown error wording stops for private inspection rather than
passing. If it reports that state, repeat only the denied read locally:

```sh
printf '%s\n' "$SDLC_TEST_TOKEN" | docker run --rm -i --pull never \
  --read-only --cap-drop ALL --security-opt no-new-privileges --log-driver none \
  --tmpfs /tmp:rw,nosuid,nodev,noexec,size=64m,mode=1777 \
  --env "SDLC_DENIED_REF=$SDLC_DENIED_REF" --entrypoint sh \
  "$SDLC_CREDENTIAL_PROBE_IMAGE" -c '
    IFS= read -r OP_SERVICE_ACCOUNT_TOKEN
    export OP_SERVICE_ACCOUNT_TOKEN
    mkdir -p "$HOME" "$OP_CONFIG_DIR"
    timeout 30 op read "$SDLC_DENIED_REF" >/dev/null
  '
```

Confirm that the error concerns the known outside-vault canary's access, not a
network, authentication or rate-limit failure. Do not share the raw diagnostics.
This manually inspected result does not turn an unknown automatic result into a
PASS. Record the wording privately for updating the probe's conservative matcher.

The containers receive no repository, host home, provider cache, Docker socket or
desktop agent. Internet access is required for the real 1Password reads; the
signed commits stay inside temporary storage and are never pushed.

The token is sent through stdin, not `docker run -e OP_SERVICE_ACCOUNT_TOKEN` or
an env file. It remains readable to sufficiently privileged host/container
processes. Tmpfs can reach host swap; host disk/swap protection still matters.
See [Docker tmpfs](https://docs.docker.com/engine/storage/tmpfs/).

## 8 Prove revocation and clean up

On 1Password.com, revoke this test Service Account token. Run
`sdlc_credential_test` again: a new container must fail at the test-vault read
without opening a login/unlock prompt. Use a fresh container so an earlier
process/cache is not the basis of the result. See
[revocation](https://www.1password.dev/service-accounts/manage-service-accounts).

Then remove the in-memory token and function:

```sh
unset SDLC_TEST_TOKEN
unfunction sdlc_credential_test
cd /tmp
docker image rm sdlc-credential-probe:local
```

Delete the disposable build directory after confirming its path, and remove the
test public signing key from GitHub if it will not be retained. Keep only PASS/
FAIL labels as the test record; do not share private keys, tokens or vault exports.

## 9 Use on-demand GitHub tokens for the next integration

GitHub documents fine-grained PAT creation through the user's settings form; no
unattended GitHub.com PAT-creation API was found in the reviewed documentation.
GitHub Apps provide the supported on-demand route instead. See
[PAT creation](https://docs.github.com/en/authentication/keeping-your-account-and-data-secure/managing-your-personal-access-tokens)
and [installation authentication](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/authenticating-as-a-github-app-installation).

Provision once:

1. Register a private GitHub App under the intended account/organisation. Use a
   unique name, the owner's GitHub URL as Homepage URL, no user OAuth/device flow,
   and inactive webhooks for this polling workflow.
2. Give repository permissions: Contents read/write, Pull requests read/write,
   Checks read and Commit statuses read. Leave unrelated and workflow-write
   permissions disabled unless a specific ticket needs them.
3. Install it on **only** the disposable repository initially. Organisation
   approval may be required. Record the App client ID and installation ID.
4. Generate the App's RSA private key on GitHub and put it in a concealed field
   in the dedicated 1Password vault. This is separate from the Ed25519 commit
   signing key. Remove the downloaded copy after securely storing it; ordinary
   deletion does not guarantee erasure. Do not copy it into a build directory.

See [App registration](https://docs.github.com/en/apps/creating-github-apps/registering-a-github-app/registering-a-github-app)
and [App key handling](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/managing-private-keys-for-github-apps).

The planned trusted publisher then reads the App key, signs a short-lived RS256
App JWT and requests an installation token restricted to the exact repository
and required permissions. GitHub returns the token and expiry; it expires after
one hour. Mint a fresh token before expiry as needed, keep it out of logs and
stored Git URLs, and use it for Git HTTPS and PR/CI requests. No human login is
needed for each token. Validate the actual `gh` endpoints and PR/CI permissions
in the disposable repository before adopting this path. This integration is not
implemented by the signing probe or current SDLC CLI. See
[App JWT](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/generating-a-json-web-token-jwt-for-a-github-app)
and [token issuance](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/generating-an-installation-access-token-for-a-github-app).

## CI identity and 1Password federation

GitHub Actions receives a job-bound `GITHUB_TOKEN`; a local shell does not inherit
that issuance mechanism. Cloud machine identities or OIDC can instead authorise
a credential broker or signing service. GitHub's documented App flow still needs
App signing authority. A sign-only key store can retain the App key while letting
a trusted broker sign the JWT; it does not automatically solve Git SSH commit
signing. See [Actions token lifecycle](https://docs.github.com/en/actions/concepts/security/github_token)
and [App key protection](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/managing-private-keys-for-github-apps).

1Password Credential Broker offers federated access in Business public preview.
Its documented flow grants access to approved Environment variables, using a
policy-matching OIDC identity plus an `OP_INTEGRATION_KEY`. Custom OIDC requires
an existing trusted issuer, its JWKS and claims, and currently the JavaScript SDK.
Plain Mac/Docker provides none of those automatically. This is worth evaluating
with an eligible account and established workload identity, but is not required
for the local dedicated-vault test above. It does not change Service Account
vault permissions or make retrieved upstream credentials expire automatically.
See [Broker setup](https://www.1password.dev/brokered-access),
[custom OIDC](https://www.1password.dev/brokered-access/custom-workflow)
and [credential lifetime limits](https://1password.com/blog/introducing-1password-credential-broker).

Passing this guide proves fresh-container vault access, denied outside-vault
access, the approved local signature and revocation without desktop prompts.
Durable bootstrap, on-demand App tokens, isolated publication and detached
controller recovery remain in the
[unattended Docker delivery plan](proposals/unattended-docker-delivery.md).

Preparation validation built the documented image on Linux/ARM64 with official
CLI 2.39.0, Git 2.39.5 and OpenSSH 9.2p1. A fake resolver and disposable keys
passed repeated fresh-container signing and stopped on invalid bootstrap, wrong
signer, accessible outside-vault canary, encrypted key, timeout, network failure
and unknown denial wording. Fake values stayed out of Docker configuration and
output. Every fixture container had networking disabled. Real vault access,
revocation and GitHub App operations remain the joint connected checks.
