// SPDX-License-Identifier: AGPL-3.0-or-later

package connectors

import (
	"net/url"
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

// FuzzResolveSQLiteDSN asserts the DSN parser is robust against crafted input and
// upholds its two security invariants for every successful resolution: the result
// is always read-only (mode=ro) and always contained within the allowed directory
// (no `..` escape). Anything outside must error, never resolve. The parser must
// never panic on adversarial bytes (embedded NUL, repeated `file:`, percent-encoded
// traversal, a bare `?`, Windows separators).
func FuzzResolveSQLiteDSN(f *testing.F) {
	root := f.TempDir()
	f.Setenv(sqliteConnectorDirEnv, root)
	rootReal, err := filepath.EvalSymlinks(root)
	if err != nil {
		f.Fatal(err)
	}

	f.Add("file:" + filepath.Join(root, "db.sqlite") + "?mode=ro")
	f.Add("file:" + filepath.Join(root, "db.sqlite") + "?mode=rwc")
	f.Add(":memory:")
	f.Add("file:../../../etc/passwd")
	f.Add("file:" + root + "/../escape.db")
	f.Add("db?x=1&y=2")
	f.Add("file:%2e%2e/x")

	f.Fuzz(func(t *testing.T, dsn string) {
		out, err := resolveSQLiteDSN(dsn)
		if err != nil {
			return // rejected — loud, not a crash
		}
		if !strings.HasPrefix(out, "file:") {
			t.Fatalf("resolved DSN is not file-backed: %q", out)
		}
		body := strings.TrimPrefix(out, "file:")
		path, query := body, ""
		if i := strings.IndexByte(body, '?'); i >= 0 {
			path, query = body[:i], body[i+1:]
		}
		q, perr := url.ParseQuery(query)
		if perr != nil {
			t.Fatalf("resolved DSN query does not parse: %q", out)
		}
		if q.Get("mode") != "ro" {
			t.Fatalf("resolved DSN is not read-only: %q (mode=%q)", out, q.Get("mode"))
		}
		rel, rerr := filepath.Rel(rootReal, path)
		if rerr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			t.Fatalf("resolved path %q escaped the allowed root %q (rel=%q)", path, rootReal, rel)
		}
	})
}
