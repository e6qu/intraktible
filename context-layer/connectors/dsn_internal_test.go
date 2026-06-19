// SPDX-License-Identifier: AGPL-3.0-or-later

package connectors

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveSQLiteDSNForcesReadOnly(t *testing.T) {
	got, err := resolveSQLiteDSN("file:/data/bureau.db?mode=rwc&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "mode=ro") || strings.Contains(got, "mode=rwc") {
		t.Fatalf("expected a read-only dsn, got %q", got)
	}
	if !strings.Contains(got, "cache=shared") {
		t.Fatalf("expected unrelated params preserved, got %q", got)
	}
}

func TestResolveSQLiteDSNRejectsInMemory(t *testing.T) {
	for _, dsn := range []string{":memory:", "file::memory:", ""} {
		if _, err := resolveSQLiteDSN(dsn); err == nil {
			t.Errorf("expected %q to be rejected", dsn)
		}
	}
}

func TestResolveSQLiteDSNAllowlist(t *testing.T) {
	root := t.TempDir()
	t.Setenv(sqliteConnectorDirEnv, root)

	// A file inside the allowed directory passes.
	inside := filepath.Join(root, "ok.db")
	if _, err := resolveSQLiteDSN("file:" + inside); err != nil {
		t.Fatalf("file inside allowed dir should pass: %v", err)
	}
	// A file outside it is rejected, including a traversal attempt.
	for _, dsn := range []string{"file:/etc/passwd.db", "file:" + filepath.Join(root, "..", "escape.db")} {
		if _, err := resolveSQLiteDSN(dsn); err == nil {
			t.Errorf("expected %q (outside %q) to be rejected", dsn, root)
		}
	}
}
