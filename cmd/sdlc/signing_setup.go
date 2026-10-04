package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/tjpeel/sdlc/internal/signing"
)

type signingPrompter interface {
	Read(string, bool) ([]byte, error)
	Close() error
}

type signingPromptFactory func(context.Context) (signingPrompter, error)

const signingSetupInstructions = `Set up a final unattended signing profile. Earlier test vaults/keys are not selected automatically.

1. In 1Password, create a dedicated custom vault containing only automation signing keys.
   For independent GitHub accounts, use separate vaults, keys and Service Accounts where possible.
2. Create an SSH Key item: New Item > SSH Key > Add Private Key > Generate a New Key > Ed25519.
   Save or move it into that dedicated vault. Copy its PUBLIC key and SHA256 fingerprint.
   Register the PUBLIC key in the intended GitHub account as a Signing key, not an Authentication key.
   Do not export or paste the private key into this setup.
3. On 1Password.com, open Developer > Service accounts and create an account.
   Grant Read Items only to that dedicated vault. Disable vault creation and do not grant other vaults,
   Environments, Write Items or Share Items. Save a recovery copy of its token in a separate private vault.
   The grant covers every item in the permitted vault. Selecting one item here does not narrow that grant.
   SDLC cannot audit the grant; verify it in 1Password before continuing.

Vault and item names or IDs below select the key. IDs survive renames and are recommended.
The Service Account token authenticates the resolver; it does not name a vault or log in to GitHub.
SDLC saves the token in an owned 0600 host file and the locator in a separate private profile.
These files stay outside repositories. The token remains plaintext: use disk encryption and private,
unsynchronised storage. Host-user, host-admin or Docker-admin compromise can expose credentials.
During signing, only the short-lived official op resolver receives this token over stdin.
Provider and test containers receive neither this token nor the signing key.

Official instructions:
  https://www.1password.dev/service-accounts/get-started
  https://www.1password.dev/ssh/manage-keys

`

func signingSetup(ctx context.Context, directory string, options signingOptions, output io.Writer, factory signingPromptFactory) (resultErr error) {
	if err := signing.ValidateProvider(options.provider); err != nil {
		return err
	}
	path, err := signing.ProfilePath(directory, options.name)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		return fmt.Errorf("signing profile already exists or cannot be inspected; use signing status, or configure to replace it deliberately")
	}
	bootstrap := options.bootstrap
	if bootstrap == "" {
		bootstrap, err = signing.BootstrapPath(directory, options.name)
		if err != nil {
			return err
		}
		if _, err := os.Lstat(bootstrap); !os.IsNotExist(err) {
			return fmt.Errorf("bootstrap already exists or cannot be inspected; use --bootstrap-file to reuse a safe existing token file")
		}
	}
	prompt, err := factory(ctx)
	if err != nil {
		return err
	}
	defer func() {
		if err := prompt.Close(); err != nil {
			resultErr = errors.Join(resultErr, err)
		}
	}()
	if _, err := io.WriteString(output, signingSetupInstructions); err != nil {
		return err
	}
	read := func(label string) (string, error) {
		data, err := prompt.Read(label, false)
		defer clear(data)
		return strings.TrimSpace(string(data)), err
	}
	vault, err := read("Dedicated vault name or ID: ")
	if err != nil {
		return err
	}
	item, err := read("SSH Key item name or ID within that vault: ")
	if err != nil {
		return err
	}
	public, err := read("Public Ed25519 key (ssh-ed25519 ...; never the private key): ")
	if err != nil {
		return err
	}
	fingerprint, err := read("Expected SHA256 fingerprint from 1Password (Enter to calculate from public key): ")
	if err != nil {
		return err
	}
	profile, err := signing.NewProfile(options.name, vault, item, public, fingerprint, bootstrap)
	if err != nil {
		return err
	}
	if options.provider != "" {
		profile.Provider = options.provider
	}
	if options.bootstrap != "" {
		if err := signing.CheckBootstrap(profile); err != nil {
			return err
		}
	}
	fmt.Fprintf(output, "\nGitHub/signing profile: %s\nSigning secret provider: %s\nPublic signing fingerprint: %s\nPrivate profile: %q\nPlaintext Service Account token file: %q\n", options.name, profile.EffectiveProvider(), profile.Fingerprint, path, bootstrap)
	fmt.Fprintln(output, "The private profile contains an op:// vault/item locator and this token file path; it contains no token or private key.")
	answer, err := read("Save this identity with the described private host storage? [y/N]: ")
	if err != nil {
		return err
	}
	if !strings.EqualFold(answer, "y") && !strings.EqualFold(answer, "yes") {
		return fmt.Errorf("signing setup cancelled; no signing files were saved")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	lock, err := signing.AcquireSetup(directory, options.name)
	if err != nil {
		return err
	}
	defer lock.Close()
	// Preflight is repeated under the per-profile lock before reading a token.
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		return fmt.Errorf("signing profile appeared during setup; inspect it with signing status before retrying")
	}
	if options.bootstrap == "" {
		if _, err := os.Lstat(bootstrap); !os.IsNotExist(err) {
			return fmt.Errorf("bootstrap appeared during setup; inspect private storage before retrying")
		}
	} else if err := signing.CheckBootstrap(profile); err != nil {
		return err
	}
	createdBootstrap := false
	if options.bootstrap == "" {
		token, err := prompt.Read("1Password Service Account token (hidden; never paste into chat): ", true)
		defer clear(token)
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := signing.StoreBootstrap(bootstrap, token); err != nil {
			return err
		}
		createdBootstrap = true
	}
	err = ctx.Err()
	if err == nil {
		err = signing.StoreNew(directory, profile, options.name)
	}
	if err != nil {
		if createdBootstrap {
			// Another profile may deliberately reuse this successful bootstrap.
			// Removing it here would invalidate that independent configuration.
			return fmt.Errorf("signing setup did not complete; the private bootstrap remains at %q; inspect it, reuse with --bootstrap-file, or delete only if unused: %w", bootstrap, err)
		}
		return err
	}
	fmt.Fprintln(output, "Signing setup saved. No 1Password, GitHub or model request was made.")
	fmt.Fprintln(output, "The token is reopened for each verification or publication and sent only to the signing resolver; no desktop approval is required.")
	fmt.Fprintf(output, "Next: sdlc signing status --profile %s\nThen: sdlc signing verify --profile %s (contacts 1Password; needs the runtime and pinned op image).\n", options.name, options.name)
	fmt.Fprintf(output, "Use the same account selection with sdlc auth login --service github --profile %s and sdlc run --github-profile %s.\n", options.name, options.name)
	return nil
}
