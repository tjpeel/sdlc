package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/tjpeel/sdlc/internal/githubauth"
	"github.com/tjpeel/sdlc/internal/githubprofile"
	"github.com/tjpeel/sdlc/internal/runtimeimage"
	"github.com/tjpeel/sdlc/internal/signing"
	"github.com/tjpeel/sdlc/internal/workrun"
)

const githubUsage = `Usage:
  sdlc github pair [--profile NAME] [--signing-profile NAME] [--replace]
  sdlc github list
  sdlc github use [--profile NAME] [--repo OWNER/REPO]
  sdlc github status [--profile NAME | --repo OWNER/REPO] [--verify]

pair checks the named native GitHub login and its registered public signing key.
It saves the account/key pairing outside repositories. The signing profile defaults to the GitHub profile name.
list shows configured native profiles and public pairings; login validity remains unverified.
use checks the selected native login, signing key and repository push access before saving this checkout's selection.
Omit --profile on use to reuse the saved selection or the sole configured profile.
status is local by default; --verify checks the selected account, registered key and repository push access.
Omit --profile on status to resolve this checkout's saved selection or unique owner match or sole pairing.
Neither pairing nor status retrieves a vault secret or writes to GitHub.
`

type githubOptions struct {
	action, profile, signingProfile, repository string
	replace, verify                             bool
}

func parseGitHubOptions(args []string, output io.Writer) (githubOptions, error) {
	var options githubOptions
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		_, err := io.WriteString(output, githubUsage)
		return options, err
	}
	if args[0] != "pair" && args[0] != "use" && args[0] != "status" && args[0] != "list" {
		return options, fmt.Errorf("use sdlc github pair, list, use or status")
	}
	flags := flag.NewFlagSet("github "+args[0], flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&options.profile, "profile", "", "named native GitHub login")
	flags.StringVar(&options.signingProfile, "signing-profile", "", "pair only: configured signing profile")
	flags.StringVar(&options.repository, "repo", "", "repository mode: GitHub OWNER/REPO; defaults to saved SDLC identity, then origin")
	flags.BoolVar(&options.replace, "replace", false, "pair only: deliberately change account/key binding")
	flags.BoolVar(&options.verify, "verify", false, "status only: connected account/key/repository checks")
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, err = io.WriteString(output, githubUsage)
		}
		return options, err
	}
	invalid := flags.NArg() != 0
	flags.Visit(func(f *flag.Flag) {
		invalid = invalid || args[0] == "list" || (f.Name == "signing-profile" || f.Name == "replace") && args[0] != "pair" || f.Name == "verify" && args[0] != "status" || f.Name == "repo" && args[0] == "pair" || (f.Name == "profile" || f.Name == "signing-profile") && f.Value.String() == ""
	})
	if invalid || args[0] == "status" && options.profile != "" && options.repository != "" {
		return options, fmt.Errorf("select the documented options for sdlc github %s; see --help", args[0])
	}
	if args[0] == "pair" && options.profile == "" {
		options.profile = "default"
	}
	if args[0] == "pair" && options.signingProfile == "" {
		options.signingProfile = options.profile
	}
	for _, name := range []string{options.profile, options.signingProfile} {
		if githubauth.ValidateProfile(name) != nil {
			return options, fmt.Errorf("invalid GitHub or signing profile name")
		}
	}
	options.action = args[0]
	return options, nil
}

type githubIdentitySession interface {
	Identity(context.Context) (githubauth.Identity, error)
	Repository(context.Context, string) (githubauth.RepositoryIdentity, error)
	SigningKeys(context.Context, string) ([]string, error)
	Close() error
}

func registeredSigningKey(ctx context.Context, session githubIdentitySession, account githubauth.Identity, publicKey string) error {
	keys, err := session.SigningKeys(ctx, account.Login)
	if err != nil {
		return err
	}
	for _, key := range keys {
		fields := strings.Fields(key)
		if len(fields) >= 2 && fields[0]+" "+fields[1] == publicKey {
			return nil
		}
	}
	return fmt.Errorf("configured public key is not registered as a signing key on the selected GitHub account; register it in that account's SSH signing keys, then retry")
}

const githubPairTimeout = 5 * time.Minute

func pairGitHub(ctx context.Context, directory string, options githubOptions, acquire func(context.Context) (githubIdentitySession, error)) (result githubprofile.Pair, resultErr error) {
	ctx, cancel := context.WithTimeout(ctx, githubPairTimeout)
	defer cancel()
	defer func() {
		if resultErr != nil && ctx.Err() != nil && !errors.Is(resultErr, ctx.Err()) {
			resultErr = errors.Join(resultErr, ctx.Err())
		}
		if errors.Is(resultErr, context.DeadlineExceeded) {
			resultErr = fmt.Errorf("GitHub pairing timed out before completion; if another GitHub operation or runtime build is active, let it finish before retrying: %w", resultErr)
		}
	}()
	path, err := signing.ProfilePath(directory, options.signingProfile)
	if err != nil {
		return result, err
	}
	profile, err := signing.Load(path)
	if err != nil {
		return result, fmt.Errorf("configure the selected signing profile with sdlc signing setup before pairing: %w", err)
	}
	session, err := acquire(ctx)
	if err != nil {
		return result, err
	}
	defer func() {
		if session.Close() != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("GitHub pairing cleanup failed"))
		}
	}()
	account, err := session.Identity(ctx)
	if err != nil {
		return result, err
	}
	if err := registeredSigningKey(ctx, session, account, profile.PublicKey); err != nil {
		return result, err
	}
	result = githubprofile.Pair{Version: 1, GitHubProfile: options.profile, AccountID: account.ID, Login: account.Login, SigningProfile: options.signingProfile, SigningID: profile.ID, PublicKey: profile.PublicKey, Fingerprint: profile.Fingerprint}
	// Refuse an identity/key changed while the connected check ran.
	current, err := signing.Load(path)
	if err != nil {
		return result, err
	}
	if err := result.CheckSigning(current); err != nil {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	return result, githubprofile.Store(directory, result, options.replace)
}

func checkPair(ctx context.Context, pair githubprofile.Pair, session githubIdentitySession, repository string) error {
	account, err := session.Identity(ctx)
	if err != nil {
		return err
	}
	if account.ID != pair.AccountID || account.Login != pair.Login {
		return fmt.Errorf("GitHub login differs from the paired account; restore it or deliberately use github pair --replace")
	}
	if err := registeredSigningKey(ctx, session, account, pair.PublicKey); err != nil {
		return err
	}
	if repository != "" {
		_, err = session.Repository(ctx, repository)
	}
	return err
}

func checkFrozenGitHub(ctx context.Context, plan workrun.Plan, session githubIdentitySession) error {
	frozen := plan.PublicationIdentity
	if frozen == nil {
		return fmt.Errorf("run lacks frozen publication identity")
	}
	account, err := session.Identity(ctx)
	if err != nil {
		return err
	}
	if account.ID != frozen.GitHubID || account.Login != frozen.GitHubLogin {
		return fmt.Errorf("GitHub login differs from the recorded run; restore the approved account before resuming")
	}
	if err := registeredSigningKey(ctx, session, account, frozen.SSHPublicKey); err != nil {
		return err
	}
	repository, err := session.Repository(ctx, plan.Repository)
	if err != nil {
		return err
	}
	if repository.ID != frozen.RepositoryID || !strings.EqualFold(repository.Name, frozen.RepositoryName) {
		return fmt.Errorf("GitHub repository differs from the recorded run")
	}
	return nil
}

func resumeGitHubPreflight(ctx context.Context, runtime runtimeimage.Manager, journal workrun.Journal) (resultErr error) {
	manager := githubauth.New(runtime)
	manager.Profile = journal.Plan.GitHubProfile
	session, err := manager.Acquire(ctx)
	if err != nil {
		return err
	}
	defer func() {
		if session.Close() != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("GitHub resume preflight cleanup failed"))
		}
	}()
	// The pair may have changed while waiting for the native auth lease.
	if _, err := runSigningProfile(runtime, journal.Plan); err != nil {
		return err
	}
	frozen := journal.Plan.PublicationIdentity
	frozenProfile, frozenErr := githubauth.NormalizeProfile(frozen.GitHubProfile)
	planProfile, planErr := githubauth.NormalizeProfile(journal.Plan.GitHubProfile)
	if frozenErr != nil || planErr != nil || planProfile != frozenProfile || session.ImageID != journal.ImageID || session.Volume != frozen.GitHubVolume || session.Profile != frozenProfile {
		return fmt.Errorf("GitHub cache or runtime differs from the recorded run")
	}
	return checkFrozenGitHub(ctx, journal.Plan, session)
}

func checkoutRepository(ctx context.Context, directory, repository string) (string, string, error) {
	command := exec.CommandContext(ctx, "git", "rev-parse", "--show-toplevel")
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(strings.SplitN(entry, "=", 2)[0], "GIT_") {
			command.Env = append(command.Env, entry)
		}
	}
	data, err := command.Output()
	if err != nil {
		return "", "", fmt.Errorf("run this command from a project checkout")
	}
	root, err := filepath.EvalSymlinks(strings.TrimSpace(string(data)))
	if err != nil {
		return "", "", fmt.Errorf("cannot resolve checkout")
	}
	if repository == "" {
		repository, err = resolveRepository(ctx, directory, root, repository)
	}
	return root, repository, err
}

func printPair(output io.Writer, pair githubprofile.Pair, repository string) {
	fmt.Fprintf(output, "GitHub profile: %s\nAccount: %s (ID %d)\nSigning profile: %s\nSigning fingerprint: %s\n", pair.GitHubProfile, pair.Login, pair.AccountID, pair.SigningProfile, pair.Fingerprint)
	if repository != "" {
		fmt.Fprintf(output, "Repository: %s\n", repository)
	}
}

func githubCommand(ctx context.Context, args []string, output io.Writer) error {
	options, err := parseGitHubOptions(args, output)
	if err != nil || options.action == "" {
		return err
	}
	runtime, err := runtimeimage.New(output, io.Discard)
	if err != nil {
		return err
	}
	if options.action == "list" {
		profiles, err := githubprofile.ListConfigured(runtime.Directory)
		if err != nil {
			return err
		}
		for _, profile := range profiles {
			native := "native cache metadata absent"
			if profile.Native {
				native = "native cache configured"
			}
			if profile.Pair == nil {
				fmt.Fprintf(output, "GitHub profile: %s (%s); account/signer: unpaired\n", profile.Name, native)
			} else {
				fmt.Fprintf(output, "GitHub profile: %s (%s); account/signer: paired; account: %s (ID %d); signing profile: %s; fingerprint: %s\n", profile.Name, native, profile.Pair.Login, profile.Pair.AccountID, profile.Pair.SigningProfile, profile.Pair.Fingerprint)
			}
		}
		if len(profiles) == 0 {
			fmt.Fprintln(output, "No configured GitHub profiles.")
		}
		fmt.Fprintln(output, "Login validity, repository access and current key registration are unverified.")
		return nil
	}
	manager := githubauth.New(runtime)
	manager.Profile = options.profile
	if options.action == "pair" {
		manager.OnWait = func(_, reason string) {
			if reason == "runtime_busy" {
				fmt.Fprintln(output, "GitHub pairing is waiting for the runtime build to finish.")
				return
			}
			fmt.Fprintln(output, "GitHub pairing is waiting for another GitHub operation to release the selected account cache.")
		}
		manager.OnAcquired = func(_ string) {
			fmt.Fprintln(output, "GitHub pairing access acquired; checking the selected login and public signing-key registration.")
		}

		fmt.Fprintln(output, "Checking the selected native GitHub login and public signing-key registration...")
		pair, err := pairGitHub(ctx, runtime.Directory, options, func(ctx context.Context) (githubIdentitySession, error) { return manager.Acquire(ctx) })
		if err != nil {
			return err
		}
		printPair(output, pair, "")
		fmt.Fprintln(output, "Pairing saved in private host state. In each organisation checkout, run sdlc github use --profile "+pair.GitHubProfile+".")
		return nil
	}
	var pair githubprofile.Pair
	root, repository := "", ""
	if options.action == "use" || options.profile == "" {
		root, repository, err = checkoutRepository(ctx, runtime.Directory, options.repository)
		if err != nil {
			return err
		}
	}
	if options.action == "use" {
		pair, err = useGitHub(ctx, runtime, root, repository, options.profile, func(ctx context.Context, name string) (githubIdentitySession, error) {
			manager.Profile = name
			return manager.Acquire(ctx)
		})
		if err != nil {
			return err
		}
		printPair(output, pair, repository)
		fmt.Fprintln(output, "Repository access checked and account selection saved privately on the host.")
		return nil
	}
	if options.profile != "" {
		pair, err = githubprofile.Load(runtime.Directory, options.profile)
	} else {
		pair, err = githubprofile.Select(runtime.Directory, root, repository, "")
	}
	if err != nil {
		return fmt.Errorf("GitHub pairing/selection needs attention: %w", err)
	}
	profile, err := signingProfile(runtime, pair.SigningProfile)
	if err != nil {
		return err
	}
	if err := pair.CheckSigning(profile); err != nil {
		return err
	}

	printPair(output, pair, repository)
	if !options.verify {
		fmt.Fprintln(output, "Local pairing matches configured signing metadata. Login validity, repository access and current key registration are unverified; use --verify. Vault access and GitHub Verified attribution require their own checks.")
		return nil
	}
	manager.Profile = pair.GitHubProfile
	session, err := manager.Acquire(ctx)
	if err != nil {
		return err
	}
	checkErr := checkPair(ctx, pair, session, repository)
	closeErr := session.Close()
	if closeErr != nil {
		closeErr = fmt.Errorf("GitHub status cleanup failed")
	}
	if err := errors.Join(checkErr, closeErr); err != nil {
		return err
	}
	fmt.Fprintln(output, "PASS: paired GitHub account and registered public signing key.")
	if repository != "" {
		fmt.Fprintln(output, "PASS: selected account has repository push access.")
	}
	fmt.Fprintln(output, "No vault secret was read. Commit email attribution and GitHub Verified status are checked at publication.")
	return nil
}

// Legacy journals keep their frozen same-name signer. New journals require the
// durable pairing and refuse replacement, even if a repository is rebound later.
func runSigningProfile(runtime runtimeimage.Manager, plan workrun.Plan) (signing.Profile, error) {
	name := plan.SigningProfile
	if name == "" {
		name = plan.GitHubProfile
	}
	profile, err := signingProfile(runtime, name)
	if err != nil {
		return profile, err
	}
	frozen := plan.PublicationIdentity
	if frozen == nil {
		return profile, fmt.Errorf("run lacks frozen publication identity")
	}
	if profile.ID != frozen.ProfileID || profile.PublicKey != frozen.SSHPublicKey || profile.Fingerprint != frozen.SSHFingerprint {
		return profile, fmt.Errorf("signing profile differs from the recorded run; restore the approved profile before resuming")
	}
	if plan.SigningProfile != "" {
		pair, err := githubprofile.Load(runtime.Directory, plan.GitHubProfile)
		if err != nil {
			return profile, err
		}
		if pair.SigningProfile != name || pair.GitHubProfile != frozen.GitHubProfile || pair.AccountID != frozen.GitHubID || pair.Login != frozen.GitHubLogin {
			return profile, fmt.Errorf("paired account differs from the recorded run; restore its approved pairing")
		}
		if err := pair.CheckSigning(profile); err != nil {
			return profile, err
		}
	}
	return profile, nil
}

// resolveRepository uses private SDLC identity before inspecting legacy origin metadata.
func resolveRepository(ctx context.Context, directory, root, explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	selection, err := githubprofile.LoadSelection(directory, root)
	if err == nil {
		return selection.Repository, nil
	}
	if !os.IsNotExist(err) {
		return "", err
	}
	repository, err := githubprofile.LoadRepository(directory, root)
	if err == nil {
		return repository, nil
	}
	if !os.IsNotExist(err) {
		return "", err
	}
	return originRepository(ctx, root)
}

func useGitHub(ctx context.Context, runtime runtimeimage.Manager, root, repository, explicit string, acquire func(context.Context, string) (githubIdentitySession, error)) (result githubprofile.Pair, resultErr error) {
	name := explicit
	if name == "" {
		// Select validates saved bindings before considering configured profiles.
		_, err := githubprofile.LoadSelection(runtime.Directory, root)
		if err == nil {
			pair, err := githubprofile.Select(runtime.Directory, root, repository, "")
			if err != nil {
				return result, err
			}
			name = pair.GitHubProfile
		} else if !os.IsNotExist(err) {
			return result, err
		} else {
			profiles, err := githubprofile.ListConfigured(runtime.Directory)
			if err != nil {
				return result, err
			}
			if len(profiles) != 1 {
				return result, fmt.Errorf("choose a configured GitHub profile with sdlc github use --profile NAME; see sdlc github list")
			}
			name = profiles[0].Name
		}
	}
	pair, pairErr := githubprofile.Load(runtime.Directory, name)
	if pairErr != nil && !os.IsNotExist(pairErr) {
		return result, pairErr
	}
	if pairErr == nil {
		profile, err := signingProfile(runtime, pair.SigningProfile)
		if err != nil {
			return result, err
		}
		if err := pair.CheckSigning(profile); err != nil {
			return result, err
		}
	}
	session, err := acquire(ctx, name)
	if err != nil {
		return result, err
	}
	// Cleanup must succeed before persisting a checked selection.
	if pairErr != nil {
		_, identityErr := session.Identity(ctx)
		var accessErr error
		if identityErr == nil {
			_, accessErr = session.Repository(ctx, repository)
		}
		closeErr := session.Close()
		if err := errors.Join(identityErr, accessErr, closeErr); err != nil {
			return result, err
		}
		return result, fmt.Errorf("GitHub profile %s has repository access but its account/signer is unpaired; configure sdlc signing setup and run sdlc github pair --profile %s --signing-profile NAME, then retry", name, name)
	}
	current, currentErr := githubprofile.Load(runtime.Directory, name)
	if currentErr != nil || current != pair {
		return result, errors.Join(fmt.Errorf("GitHub pairing changed while acquiring the selected native cache; retry deliberate selection"), session.Close())
	}
	profile, signingErr := signingProfile(runtime, pair.SigningProfile)
	if signingErr == nil {
		signingErr = pair.CheckSigning(profile)
	}
	if signingErr != nil {
		return result, errors.Join(signingErr, session.Close())
	}
	checkErr := checkPair(ctx, pair, session, repository)
	closeErr := session.Close()
	if err := errors.Join(checkErr, closeErr); err != nil {
		return result, err
	}
	profile, err = signingProfile(runtime, pair.SigningProfile)
	if err != nil {
		return result, err
	}
	if err := pair.CheckSigning(profile); err != nil {
		return result, err
	}
	return pair, githubprofile.SaveSelection(runtime.Directory, root, repository, pair)
}
