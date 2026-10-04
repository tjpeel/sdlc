package signing

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewProfileCanonicalPublicMetadataOnly(t *testing.T) {
	identity, _ := fixture(t)
	path, err := BootstrapPath(filepath.Join(filepath.Dir(identity.BootstrapFile), "private-bootstrap"), "work")
	if err != nil {
		t.Fatal(err)
	}
	profile, err := NewProfile("work", "Example Vault", "Example Item", identity.PublicKey+" disposable-comment", "", path)
	if err != nil {
		t.Fatal(err)
	}
	if profile.PublicKey != identity.PublicKey || profile.Fingerprint != identity.Fingerprint || profile.ID != "work-signing" || profile.BootstrapFile != path {
		t.Fatal("public metadata canonicalization changed identity")
	}
	if profile.Reference != "op://"+"Example Vault/Example Item/private key?ssh-format=openssh" {
		t.Fatal("ordinary reference spaces were lost")
	}
	supplied, err := NewProfile("work", "stable-vault-id", "stable-item-id", identity.PublicKey, identity.Fingerprint, path)
	if err != nil || supplied.Fingerprint != identity.Fingerprint {
		t.Fatal("matching explicit fingerprint rejected", err)
	}
	if _, err := NewProfile("work", "Example Vault", "Example Item", identity.PublicKey, "SHA256:"+strings.Repeat("a", 43), path); err == nil {
		t.Fatal("mismatched fingerprint accepted")
	}
	token := []byte("offline-disposable-bootstrap")
	if err := StoreBootstrap(path, token); err != nil {
		t.Fatal(err)
	}
	if err := CheckBootstrap(profile); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, token) {
		t.Fatal("bootstrap copied into public profile")
	}
	if !bytes.Equal(token, []byte("offline-disposable-bootstrap")) {
		t.Fatal("caller-owned token input mutated")
	}
}

func TestNewProfileRejectsAmbiguousReferenceSegments(t *testing.T) {
	identity, _ := fixture(t)
	for _, bad := range []string{"", " ", " Example", "Example ", ".", "..", "vault/item", "vault?query", "vault#fragment", "vault%2fitem", "vault\\item", "vault\"item", "vault'item", "vault`item", "vault\nitem", "vault\x00item"} {
		for _, segments := range [][2]string{{bad, "Example Item"}, {"Example Vault", bad}} {
			if _, err := NewProfile("work", segments[0], segments[1], identity.PublicKey, "", identity.BootstrapFile); err == nil {
				t.Fatal("ambiguous segment accepted")
			}
		}
	}
	for _, reference := range []string{"op://" + "/YOUR_ITEM/private key?ssh-format=openssh", "op://YOUR_VAULT//private key?ssh-format=openssh"} {
		profile := identity
		profile.Reference = reference
		if profile.Validate() == nil {
			t.Fatal("existing profile accepted empty reference segment")
		}
	}
}

func TestBootstrapStorageNormalizesPrivatelyAndRefusesOverwrite(t *testing.T) {
	identity, _ := fixture(t)
	directory := filepath.Join(filepath.Dir(identity.BootstrapFile), "new-private", "nested")
	path, err := BootstrapPath(directory, "")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != "signing-default-bootstrap" {
		t.Fatal("default bootstrap name differs")
	}
	supplied := []byte("  offline-disposable-bootstrap\n\t")
	if err := StoreBootstrap(path, supplied); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "offline-disposable-bootstrap\n" {
		t.Fatal("bootstrap normalization failed", err)
	}
	for _, directory := range []string{filepath.Dir(path), filepath.Dir(filepath.Dir(path))} {
		info, err := os.Stat(directory)
		if err != nil || info.Mode().Perm() != 0700 {
			t.Fatal("created parent is not private", err)
		}
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("bootstrap permissions are not private", err)
	}
	if err := StoreBootstrap(path, []byte("different-offline-value")); err == nil {
		t.Fatal("existing bootstrap overwritten")
	}
	retained, _ := os.ReadFile(path)
	if !bytes.Equal(retained, data) {
		t.Fatal("refused overwrite changed stored bytes")
	}
	profile := identity
	profile.BootstrapFile = path
	if err := CheckBootstrap(profile); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if err := CheckBootstrap(profile); err == nil {
		t.Fatal("public bootstrap accepted")
	}
}

func TestBootstrapRejectsUnsafePathsBeforeCreatingChildren(t *testing.T) {
	identity, _ := fixture(t)
	base := filepath.Dir(identity.BootstrapFile)
	target := filepath.Join(base, "outside")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "linked-parent")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(link, "child", "bootstrap")
	if err := StoreBootstrap(path, []byte("offline-data")); err == nil {
		t.Fatal("symlink parent accepted")
	}
	if _, err := os.Stat(filepath.Join(target, "child")); !os.IsNotExist(err) {
		t.Fatal("symlink rejection created a child")
	}
	if _, err := BootstrapPath(link, "work"); err == nil {
		t.Fatal("bootstrap path accepted linked directory")
	}
	repository := filepath.Join(base, "repository")
	if err := os.MkdirAll(filepath.Join(repository, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := StoreBootstrap(filepath.Join(repository, "new", "bootstrap"), []byte("offline-data")); err == nil {
		t.Fatal("Git repository accepted")
	}
	if _, err := os.Stat(filepath.Join(repository, "new")); !os.IsNotExist(err) {
		t.Fatal("Git rejection created a child")
	}
	publicParent := filepath.Join(base, "public-parent")
	if err := os.Mkdir(publicParent, 0755); err != nil {
		t.Fatal(err)
	}
	if err := StoreBootstrap(filepath.Join(publicParent, "bootstrap"), []byte("offline-data")); err == nil {
		t.Fatal("public immediate parent accepted")
	}
	if err := StoreBootstrap("relative-bootstrap", []byte("offline-data")); err == nil {
		t.Fatal("relative storage accepted")
	}
	for _, name := range []string{"../work", "Work", "-work", strings.Repeat("a", 49)} {
		if _, err := BootstrapPath(base, name); err == nil {
			t.Fatal("invalid profile path accepted")
		}
	}
	finalLink := filepath.Join(base, "final-link")
	if err := os.Symlink(identity.BootstrapFile, finalLink); err != nil {
		t.Fatal(err)
	}
	if err := StoreBootstrap(finalLink, []byte("offline-data")); err == nil {
		t.Fatal("final symlink accepted")
	}
	profile := identity
	profile.BootstrapFile = finalLink
	if err := CheckBootstrap(profile); err == nil {
		t.Fatal("bootstrap check followed symlink")
	}
}

func TestBootstrapFramingAndErrorsKeepDisposableDataPrivate(t *testing.T) {
	identity, _ := fixture(t)
	path := filepath.Join(filepath.Dir(identity.BootstrapFile), "new-bootstrap")
	for _, data := range [][]byte{nil, []byte(" \n\t"), []byte("offline-secret-marker\nsecond-line"), []byte("offline-secret-marker\rsecond-line"), []byte("offline-secret-marker\x00"), bytes.Repeat([]byte{'x'}, maximumBootstrap+1)} {
		err := StoreBootstrap(path, data)
		if err == nil || strings.Contains(err.Error(), "offline-secret-marker") {
			t.Fatal("invalid token accepted or included in error", err)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("invalid token created a bootstrap file")
		}
		if len(data) > 0 {
			if err := os.WriteFile(identity.BootstrapFile, data, 0600); err != nil {
				t.Fatal(err)
			}
			err = CheckBootstrap(identity)
			if err == nil || strings.Contains(err.Error(), "offline-secret-marker") {
				t.Fatal("invalid existing bootstrap accepted or leaked", err)
			}
		}
	}
}

func TestStoreNewRefusesExistingProfileAndPreservesBootstrap(t *testing.T) {
	profile, _ := fixture(t)
	directory := filepath.Join(filepath.Dir(profile.BootstrapFile), "exclusive-state")
	if err := StoreNew(directory, profile, "work"); err != nil {
		t.Fatal(err)
	}
	path, err := ProfilePath(directory, "work")
	if err != nil {
		t.Fatal(err)
	}
	initial, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	bootstrap, err := os.ReadFile(profile.BootstrapFile)
	if err != nil {
		t.Fatal(err)
	}
	replacement := profile
	replacement.ID = "different-signing"
	if err := StoreNew(directory, replacement, "work"); err == nil {
		t.Fatal("existing profile overwritten")
	}
	retained, _ := os.ReadFile(path)
	if !bytes.Equal(initial, retained) {
		t.Fatal("refused setup changed existing profile")
	}
	unchanged, _ := os.ReadFile(profile.BootstrapFile)
	if !bytes.Equal(bootstrap, unchanged) {
		t.Fatal("profile storage changed preexisting bootstrap")
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("exclusive profile is not private", err)
	}
	loaded, err := Load(path)
	if err != nil || loaded.ID != profile.ID {
		t.Fatal("exclusive profile not loadable", err)
	}
}

func TestStoreNewConcurrentSetupHasOneWinner(t *testing.T) {
	profile, _ := fixture(t)
	directory := filepath.Join(filepath.Dir(profile.BootstrapFile), "concurrent-state")
	// Precreate the shared parent; the assertion concerns atomic profile creation.
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for range 2 {
		go func() { <-start; results <- StoreNew(directory, profile, "work") }()
	}
	close(start)
	success := 0
	for range 2 {
		if err := <-results; err == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatal("concurrent setup did not have exactly one winner", success)
	}
	path, _ := ProfilePath(directory, "work")
	loaded, err := Load(path)
	if err != nil || loaded != profile {
		t.Fatal("concurrent setup corrupted retained profile", err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 1 {
		t.Fatal("concurrent setup left unexpected files", err)
	}
}

func TestStoreNewRejectsProfileStorageLinksAndRepositoryPaths(t *testing.T) {
	profile, _ := fixture(t)
	base := filepath.Dir(profile.BootstrapFile)
	target := filepath.Join(base, "target-state")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "linked-state")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := StoreNew(filepath.Join(link, "child"), profile, "work"); err == nil {
		t.Fatal("linked ancestor accepted")
	}
	if _, err := os.Stat(filepath.Join(target, "child")); !os.IsNotExist(err) {
		t.Fatal("linked setup created child")
	}
	repository := filepath.Join(base, "source")
	if err := os.MkdirAll(filepath.Join(repository, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := StoreNew(filepath.Join(repository, "state"), profile, "work"); err == nil {
		t.Fatal("repository profile storage accepted")
	}
	if _, err := os.Stat(filepath.Join(repository, "state")); !os.IsNotExist(err) {
		t.Fatal("repository rejection created state")
	}
	path, _ := ProfilePath(target, "work")
	if err := os.Symlink(profile.BootstrapFile, path); err != nil {
		t.Fatal(err)
	}
	if err := StoreNew(target, profile, "work"); err == nil {
		t.Fatal("final profile symlink accepted")
	}
}

func TestStoreReplacementChangesOnlyProfileUnderSetupLease(t *testing.T) {
	profile, path := replacementFixture(t)
	directory := filepath.Dir(path)
	lease, err := AcquireSetup(directory, "")
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	bootstrap, err := os.ReadFile(profile.BootstrapFile)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	replacement := profile
	replacement.Provider = DefaultProvider
	replacement.ID = "replacement-signing"
	replacement.Reference = "op://YOUR_VAULT/YOUR_REPLACEMENT_KEY/private key?ssh-format=openssh"
	if err := StoreReplacement(directory, replacement, profile, ""); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil || loaded != replacement {
		t.Fatal("replacement profile not loadable", err)
	}
	after, err := os.Lstat(path)
	if err != nil || after.Mode().Perm() != 0600 || privateOwner(after) != nil || os.SameFile(before, after) {
		t.Fatal("replacement was not published as a new private file", err)
	}
	unchanged, err := os.ReadFile(profile.BootstrapFile)
	if err != nil || !bytes.Equal(bootstrap, unchanged) {
		t.Fatal("replacement changed bootstrap contents", err)
	}
	assertNoReplacementTemporary(t, directory)
}

func TestStoreReplacementRejectsStaleExpectedMetadataWithoutWriting(t *testing.T) {
	profile, path := replacementFixture(t)
	directory := filepath.Dir(path)
	edited := profile
	edited.ID = "edited-signing"
	data, err := json.Marshal(edited)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	replacement := profile
	replacement.ID = "replacement-signing"
	if err := StoreReplacement(directory, replacement, profile, ""); err == nil {
		t.Fatal("stale wizard metadata replaced an edited profile")
	}
	retained, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(data, retained) {
		t.Fatal("refused replacement changed edited profile", err)
	}
	assertNoReplacementTemporary(t, directory)
}

func TestStoreReplacementRejectsUnsafeOrMissingTarget(t *testing.T) {
	for _, cause := range []string{"absent", "public", "symlink", "hardlink", "directory", "invalid", "oversized", "unknown field"} {
		t.Run(cause, func(t *testing.T) {
			profile, path := replacementFixture(t)
			directory := filepath.Dir(path)
			bootstrap, err := os.ReadFile(profile.BootstrapFile)
			if err != nil {
				t.Fatal(err)
			}
			switch cause {
			case "absent", "symlink", "hardlink", "directory":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			}
			switch cause {
			case "public":
				err = os.Chmod(path, 0644)
			case "symlink":
				err = os.Symlink(profile.BootstrapFile, path)
			case "hardlink":
				err = os.Link(profile.BootstrapFile, path)
			case "directory":
				err = os.Mkdir(path, 0700)
			case "invalid":
				err = os.WriteFile(path, []byte("invalid offline profile"), 0600)
			case "oversized":
				err = os.WriteFile(path, bytes.Repeat([]byte(" "), 8193), 0600)
			case "unknown field":
				data, marshalErr := json.Marshal(profile)
				if marshalErr != nil {
					t.Fatal(marshalErr)
				}
				data = append(data[:len(data)-1], []byte(",\"extra\":true}")...)
				err = os.WriteFile(path, data, 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			before, beforeErr := os.Lstat(path)
			var contents []byte
			if beforeErr == nil && before.Mode().IsRegular() {
				contents, err = os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
			}
			replacement := profile
			replacement.ID = "replacement-signing"
			if err := StoreReplacement(directory, replacement, profile, ""); err == nil {
				t.Fatal("unsafe replacement target accepted")
			}
			after, afterErr := os.Lstat(path)
			if os.IsNotExist(beforeErr) {
				if !os.IsNotExist(afterErr) {
					t.Fatal("replacement created an absent profile")
				}
			} else if afterErr != nil || !os.SameFile(before, after) || before.Mode() != after.Mode() {
				t.Fatal("refused replacement changed target identity or mode", afterErr)
			}
			if contents != nil {
				retained, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(contents, retained) {
					t.Fatal("refused replacement changed target contents", err)
				}
			}
			unchanged, err := os.ReadFile(profile.BootstrapFile)
			if err != nil || !bytes.Equal(bootstrap, unchanged) {
				t.Fatal("refused replacement changed bootstrap target", err)
			}
			assertNoReplacementTemporary(t, directory)
		})
	}
}

func TestStoreReplacementRejectsInvalidMetadataBeforeWriting(t *testing.T) {
	for _, cause := range []string{"new profile", "expected profile", "profile name"} {
		t.Run(cause, func(t *testing.T) {
			profile, path := replacementFixture(t)
			initial, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			replacement, expected := profile, profile
			name := ""
			switch cause {
			case "new profile":
				replacement.Reference = "invalid-locator-with-private-marker"
			case "expected profile":
				expected.Reference = "invalid-locator-with-private-marker"
			case "profile name":
				name = "../private-marker"
			}
			err = StoreReplacement(filepath.Dir(path), replacement, expected, name)
			if err == nil {
				t.Fatal("invalid replacement metadata accepted")
			}
			if strings.Contains(err.Error(), "private-marker") || strings.Contains(err.Error(), profile.BootstrapFile) || strings.Contains(err.Error(), profile.Reference) {
				t.Fatal("replacement error exposed locator contents")
			}
			retained, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(initial, retained) {
				t.Fatal("invalid metadata changed existing profile", err)
			}
			assertNoReplacementTemporary(t, filepath.Dir(path))
		})
	}
}

func TestStoreReplacementRejectsUnsafeStorageWithoutWriting(t *testing.T) {
	for _, cause := range []string{"relative", "noncanonical", "public", "parent symlink", "repository", "missing"} {
		t.Run(cause, func(t *testing.T) {
			profile, fixturePath := replacementFixture(t)
			base := filepath.Dir(fixturePath)
			directory := filepath.Join(base, "replacement-state")
			if err := StoreNew(directory, profile, "work"); err != nil {
				t.Fatal(err)
			}
			path, err := ProfilePath(directory, "work")
			if err != nil {
				t.Fatal(err)
			}
			initial, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			selected := directory
			switch cause {
			case "relative":
				selected = "relative-replacement-state"
			case "noncanonical":
				selected = directory + "/../replacement-state"
			case "public":
				err = os.Chmod(directory, 0755)
			case "parent symlink":
				selected = filepath.Join(base, "linked-state")
				err = os.Symlink(directory, selected)
			case "repository":
				err = os.Mkdir(filepath.Join(directory, ".git"), 0700)
			case "missing":
				selected = filepath.Join(base, "missing-state")
			}
			if err != nil {
				t.Fatal(err)
			}
			replacement := profile
			replacement.ID = "replacement-signing"
			if err := StoreReplacement(selected, replacement, profile, "work"); err == nil {
				t.Fatal("unsafe profile storage accepted")
			}
			retained, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(initial, retained) {
				t.Fatal("unsafe storage changed profile contents", err)
			}
			if cause == "missing" {
				if _, err := os.Lstat(selected); !os.IsNotExist(err) {
					t.Fatal("replacement created missing storage", err)
				}
			}
			assertNoReplacementTemporary(t, directory)
		})
	}
}

func TestStoreReplacementRefusesProfileAndBootstrapPathCollision(t *testing.T) {
	for _, cause := range []string{"new bootstrap", "expected bootstrap"} {
		t.Run(cause, func(t *testing.T) {
			profile, path := replacementFixture(t)
			replacement, expected := profile, profile
			if cause == "new bootstrap" {
				replacement.BootstrapFile = path
			} else {
				expected.BootstrapFile = filepath.Dir(path) + "/child/../" + filepath.Base(path)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := StoreReplacement(filepath.Dir(path), replacement, expected, ""); err == nil {
				t.Fatal("profile destination accepted as bootstrap locator")
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("bootstrap path collision changed profile", err)
			}
			assertNoReplacementTemporary(t, filepath.Dir(path))
		})
	}
}

func TestStoreReplacementDoesNotRequireOrCreateBootstrapFiles(t *testing.T) {
	profile, path := replacementFixture(t)
	directory := filepath.Dir(path)
	profile.BootstrapFile = filepath.Join(directory, "absent-old-bootstrap")
	data, err := json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	replacement := profile
	replacement.ID = "replacement-signing"
	replacement.BootstrapFile = filepath.Join(directory, "absent-new-bootstrap")
	if err := StoreReplacement(directory, replacement, profile, ""); err != nil {
		t.Fatal("profile replacement tried to use bootstrap contents", err)
	}
	for _, bootstrap := range []string{profile.BootstrapFile, replacement.BootstrapFile} {
		if _, err := os.Lstat(bootstrap); !os.IsNotExist(err) {
			t.Fatal("replacement created a bootstrap file", err)
		}
	}
	assertNoReplacementTemporary(t, directory)
}

func TestReplacementPublicationRechecksProfileAndTemporaryIdentity(t *testing.T) {
	for _, cause := range []string{"edited metadata", "new target inode", "new temporary inode", "edited temporary metadata"} {
		t.Run(cause, func(t *testing.T) {
			profile, path := replacementFixture(t)
			directory := filepath.Dir(path)
			root, err := os.OpenRoot(directory)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			directoryInfo, err := os.Lstat(directory)
			if err != nil {
				t.Fatal(err)
			}
			targetInfo, err := matchingReplacementProfile(root, filepath.Base(path), profile)
			if err != nil {
				t.Fatal(err)
			}
			const temporary = ".signing-profile-test-pending"
			file, err := root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if err != nil {
				t.Fatal(err)
			}
			created, err := file.Stat()
			if err != nil {
				file.Close()
				t.Fatal(err)
			}
			replacement := profile
			replacement.ID = "replacement-signing"
			data, err := json.Marshal(replacement)
			if err != nil {
				file.Close()
				t.Fatal(err)
			}
			if _, err := file.Write(data); err != nil {
				file.Close()
				t.Fatal(err)
			}
			if err := file.Sync(); err != nil {
				file.Close()
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
			switch cause {
			case "edited metadata":
				edited := profile
				edited.ID = "edited-signing"
				data, err := json.Marshal(edited)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, data, 0600); err != nil {
					t.Fatal(err)
				}
			case "new target inode":
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				other := filepath.Join(directory, "edited-profile")
				if err := os.WriteFile(other, data, 0600); err != nil {
					t.Fatal(err)
				}
				if err := root.Rename("edited-profile", filepath.Base(path)); err != nil {
					t.Fatal(err)
				}
			case "new temporary inode":
				other := filepath.Join(directory, "foreign-temporary")
				if err := os.WriteFile(other, []byte("offline foreign file"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := root.Rename("foreign-temporary", temporary); err != nil {
					t.Fatal(err)
				}
			case "edited temporary metadata":
				edited := replacement
				edited.ID = "edited-signing"
				data, err := json.Marshal(edited)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(directory, temporary), data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := publishReplacement(root, directory, path, directoryInfo, targetInfo, temporary, created, profile, replacement); err == nil {
				t.Fatal("changed target or temporary published")
			}
			retained, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, retained) {
				t.Fatal("failed publication changed existing profile", err)
			}
			removeCreatedFile(root, temporary, created)
			pending, err := os.ReadFile(filepath.Join(directory, temporary))
			if cause == "new temporary inode" {
				if err != nil || string(pending) != "offline foreign file" {
					t.Fatal("cleanup removed a different temporary inode", err)
				}
			} else if !os.IsNotExist(err) {
				t.Fatal("cleanup retained its own temporary", err)
			}
		})
	}
}

func assertNoReplacementTemporary(t *testing.T, directory string) {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".signing-profile-") {
			t.Fatal("replacement left a temporary profile")
		}
	}
}

func replacementFixture(t *testing.T) (Profile, string) {
	t.Helper()
	profile, path := fixture(t)
	if err := os.Chmod(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	return profile, path
}

func TestAcquireSetupSerializesEntireProfileTransaction(t *testing.T) {
	profile, _ := fixture(t)
	directory := filepath.Join(filepath.Dir(profile.BootstrapFile), "leased-state")
	lease, err := AcquireSetup(directory, "work")
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	if next, err := AcquireSetup(directory, "work"); err == nil {
		next.Close()
		t.Fatal("same-profile setup bypassed transaction lock")
	}
	other, err := AcquireSetup(directory, "personal")
	if err != nil {
		t.Fatal("independent profile setup blocked", err)
	}
	other.Close()
	// Storage completes while the setup lease remains held, including its own
	// retained bootstrap; a competing wizard cannot enter this transaction.
	path, err := BootstrapPath(directory, "work")
	if err != nil {
		t.Fatal(err)
	}
	if err := StoreBootstrap(path, []byte("offline-transaction-data")); err != nil {
		t.Fatal(err)
	}
	profile.BootstrapFile = path
	if err := StoreNew(directory, profile, "work"); err != nil {
		t.Fatal(err)
	}
	if next, err := AcquireSetup(directory, "work"); err == nil {
		next.Close()
		t.Fatal("storage prematurely released setup lock")
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	next, err := AcquireSetup(directory, "work")
	if err != nil {
		t.Fatal("closed lease remained held", err)
	}
	next.Close()
	info, err := os.Stat(filepath.Join(directory, "signing-setup-work.lock"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("retained setup lock not private", err)
	}
}

func TestAcquireSetupRejectsUnsafeLockFiles(t *testing.T) {
	for _, cause := range []string{"symlink", "hardlink", "public", "directory", "parent symlink", "repository"} {
		t.Run(cause, func(t *testing.T) {
			profile, _ := fixture(t)
			directory := filepath.Join(filepath.Dir(profile.BootstrapFile), "setup-state")
			if err := os.Mkdir(directory, 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(directory, "signing-setup-work.lock")
			switch cause {
			case "symlink":
				if err := os.Symlink(profile.BootstrapFile, path); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(profile.BootstrapFile, path); err != nil {
					t.Fatal(err)
				}
			case "public":
				if err := os.WriteFile(path, []byte{}, 0644); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			case "parent symlink":
				link := filepath.Join(filepath.Dir(profile.BootstrapFile), "setup-link")
				if err := os.Symlink(directory, link); err != nil {
					t.Fatal(err)
				}
				directory = filepath.Join(link, "child")
			case "repository":
				if err := os.Mkdir(filepath.Join(directory, ".git"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.ReadFile(profile.BootstrapFile)
			if err != nil {
				t.Fatal(err)
			}
			lease, err := AcquireSetup(directory, "work")
			if err == nil {
				lease.Close()
				t.Fatal("unsafe setup lock accepted")
			}
			after, err := os.ReadFile(profile.BootstrapFile)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("rejected lock changed bootstrap target", err)
			}
		})
	}
}

func TestFailedWriteCleanupRemovesOnlyCreatedFileIdentity(t *testing.T) {
	profile, _ := fixture(t)
	directory := filepath.Join(filepath.Dir(profile.BootstrapFile), "cleanup-state")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	file, err := root.OpenFile("bootstrap", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	created, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	removeCreatedFile(root, "bootstrap", created)
	if _, err := root.Lstat("bootstrap"); !os.IsNotExist(err) {
		t.Fatal("cleanup retained its own created file")
	}
	replacement, err := root.OpenFile("bootstrap", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := replacement.Write([]byte("offline-replacement-data")); err != nil {
		t.Fatal(err)
	}
	replacement.Close()
	removeCreatedFile(root, "bootstrap", created)
	retained, err := root.Open("bootstrap")
	if err != nil {
		t.Fatal(err)
	}
	defer retained.Close()
	data, err := io.ReadAll(retained)
	if err != nil || string(data) != "offline-replacement-data" {
		t.Fatal("cleanup deleted a different file identity", err)
	}
}
