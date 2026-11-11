// Package featureguard provides the shared fence for interrupted feature migrations.
package featureguard

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const RuntimeMigrationPending = "runtime-migration.pending.json"

// Check refuses ordinary journal access while a feature runtime transaction is pending.
func Check(root, reference string) error {
	if root == "" || reference == "" {
		return nil
	}
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || filepath.Base(reference) != reference || reference == "." || reference == ".." || strings.ContainsAny(reference, "/\\\x00\n\r") {
		return fmt.Errorf("invalid feature migration fence identity")
	}
	path := filepath.Join(root, ".sdlc", "work", reference, "series", RuntimeMigrationPending)
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return fmt.Errorf("cannot inspect feature runtime migration fence: %w", err)
	}
	return fmt.Errorf("feature runtime migration is pending; repeat the feature migration command to recover before resuming")
}
