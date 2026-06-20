// SPDX-License-Identifier: AGPL-3.0-or-later

package connectors

import (
	"os"
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

// A symlink placed INSIDE the allowed dir but pointing OUTSIDE it must be rejected:
// the lexical path sits under the root, but sql.Open would follow the link to an
// arbitrary file. Containment resolves symlinks before checking.
func TestResolveSQLiteDSNRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	t.Setenv(sqliteConnectorDirEnv, root)

	target := filepath.Join(outside, "secret.db")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "innocent.db")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unsupported on this platform: %v", err)
	}
	if _, err := resolveSQLiteDSN("file:" + link); err == nil {
		t.Fatal("a symlink inside the allowed dir pointing outside it must be rejected")
	}
}
