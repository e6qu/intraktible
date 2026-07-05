// SPDX-License-Identifier: AGPL-3.0-or-later
// The SQL connector type (pure-Go sqlite) — excluded from js/wasm builds, where
// modernc.org/sqlite cannot compile; defining a "sql" connector there fails
// loudly in the factory instead.

//go:build !js

package connectors

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (CGO-free); registers "sqlite"
)

// maxSQLRows bounds how many rows a SQL connector returns, so a broad query can
// never blow up memory or the recorded event.
const maxSQLRows = 1000

type sqlConfig struct {
	Driver string   `json:"driver"` // database/sql driver name; only "sqlite" is built in
	DSN    string   `json:"dsn"`    // driver-specific data source name
	Query  string   `json:"query"`  // a SELECT with named placeholders (:name)
	Args   []string `json:"args"`   // param names bound positionally from the params object
}

type sqlConnector struct {
	cfg sqlConfig
}

func newSQL(config json.RawMessage) (sqlConnector, error) {
	var cfg sqlConfig
	if len(config) > 0 {
		if err := json.Unmarshal(config, &cfg); err != nil {
			return sqlConnector{}, fmt.Errorf("context-layer: sql connector config: %w", err)
		}
	}
	if cfg.Driver == "" {
		cfg.Driver = "sqlite"
	}
	if cfg.Driver != "sqlite" {
		// Only the pure-Go sqlite driver is compiled in; other drivers need a build
		// that imports them. Fail loudly rather than open a nil driver.
		return sqlConnector{}, fmt.Errorf("context-layer: sql connector driver %q is not available (only \"sqlite\" is built in)", cfg.Driver)
	}
	if cfg.DSN == "" || cfg.Query == "" {
		return sqlConnector{}, fmt.Errorf("context-layer: sql connector needs a dsn and a query")
	}
	dsn, err := resolveSQLiteDSN(cfg.DSN)
	if err != nil {
		return sqlConnector{}, err
	}
	cfg.DSN = dsn
	return sqlConnector{cfg: cfg}, nil
}

// sqliteConnectorDirEnv, when set, confines SQL-connector databases to files under
// that directory — defense in depth against an editor pointing a connector at an
// arbitrary local file (another tenant's database, a secrets file).
const sqliteConnectorDirEnv = "ITK_SQL_CONNECTOR_DIR"

// resolveSQLiteDSN validates a sqlite DSN and returns a hardened, read-only form:
// it rejects non-file (in-memory) DSNs, forces mode=ro so a connector can never
// write, and — when ITK_SQL_CONNECTOR_DIR is set — requires the database file to
// live within that allowlisted directory.
func resolveSQLiteDSN(dsn string) (string, error) {
	raw := strings.TrimPrefix(dsn, "file:")
	path := raw
	params := url.Values{}
	if i := strings.IndexByte(raw, '?'); i >= 0 {
		var err error
		path = raw[:i]
		if params, err = url.ParseQuery(raw[i+1:]); err != nil {
			return "", fmt.Errorf("context-layer: sql connector dsn query: %w", err)
		}
	}
	if path == "" || strings.EqualFold(path, ":memory:") {
		return "", fmt.Errorf("context-layer: sql connector needs a file-backed sqlite database")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("context-layer: sql connector dsn path: %w", err)
	}
	if root := os.Getenv(sqliteConnectorDirEnv); root != "" {
		rootAbs, err := filepath.Abs(root)
		if err != nil {
			return "", fmt.Errorf("context-layer: sql connector dir: %w", err)
		}
		// Resolve symlinks before the containment check: the lexical path can sit under
		// the allowed dir while a symlink (the final component OR a parent) points
		// outside it, which sql.Open would then follow to an arbitrary file (another
		// tenant's DB, a secrets file). Resolving first closes that escape. A read-only
		// connector's file must exist, so a missing path failing here is correct.
		rootReal, err := filepath.EvalSymlinks(rootAbs)
		if err != nil {
			return "", fmt.Errorf("context-layer: sql connector dir: %w", err)
		}
		absReal, err := filepath.EvalSymlinks(abs)
		if err != nil {
			if !os.IsNotExist(err) {
				return "", fmt.Errorf("context-layer: sql connector database %q: %w", abs, err)
			}
			// The file may not exist yet (DSN resolution is separate from open). A
			// non-existent final component can't be a symlink, so resolving the parent
			// dir — which must exist — and rejoining the base closes the parent-symlink
			// escape without requiring the DB file to pre-exist.
			dirReal, derr := filepath.EvalSymlinks(filepath.Dir(abs))
			if derr != nil {
				return "", fmt.Errorf("context-layer: sql connector dir: %w", derr)
			}
			absReal = filepath.Join(dirReal, filepath.Base(abs))
		}
		rel, err := filepath.Rel(rootReal, absReal)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("context-layer: sql connector database %q is outside the allowed directory %q", absReal, rootReal)
		}
		abs = absReal // open the resolved real path
	}
	// Force read-only, dropping any caller-supplied (possibly writable) mode.
	params.Set("mode", "ro")
	return "file:" + abs + "?" + params.Encode(), nil
}

// Fetch opens the configured database, runs the parameterized query (binding the
// declared args from the params object as values — never string-interpolated, so
// caller params cannot inject SQL), and returns {"rows": [...]} as JSON.
func (c sqlConnector) Fetch(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
	db, err := sql.Open(c.cfg.Driver, c.cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("context-layer: sql connector open: %w", err)
	}
	defer func() { _ = db.Close() }()

	args, err := bindArgs(c.cfg.Args, params)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	rows, err := db.QueryContext(ctx, c.cfg.Query, args...)
	if err != nil {
		return nil, fmt.Errorf("context-layer: sql connector query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out, err := scanRows(rows)
	if err != nil {
		return nil, err
	}
	b, err := json.Marshal(map[string]any{"rows": out})
	if err != nil {
		return nil, fmt.Errorf("context-layer: sql connector marshal: %w", err)
	}
	return b, nil
}

// bindArgs maps each declared arg name to a named query parameter, reading its
// value from the params object (a missing name binds to nil).
func bindArgs(names []string, params json.RawMessage) ([]any, error) {
	if len(names) == 0 {
		return nil, nil
	}
	var p map[string]any
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("context-layer: sql connector params: %w", err)
		}
	}
	args := make([]any, 0, len(names))
	for _, name := range names {
		v, ok := p[name]
		if !ok {
			// A declared arg absent from the fetch params would otherwise bind to NULL
			// silently and return wrong/empty rows — fail loudly instead.
			return nil, fmt.Errorf("context-layer: sql connector arg %q not provided in params", name)
		}
		args = append(args, sql.Named(name, v))
	}
	return args, nil
}

// scanRows reads up to maxSQLRows rows into a slice of column→value maps.
func scanRows(rows *sql.Rows) ([]map[string]any, error) {
	cols, err := rows.Columns()
	if err != nil {
		return nil, fmt.Errorf("context-layer: sql connector columns: %w", err)
	}
	var out []map[string]any
	for rows.Next() {
		if len(out) >= maxSQLRows {
			return nil, fmt.Errorf("context-layer: sql connector query returned more than %d rows", maxSQLRows)
		}
		cells := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range cells {
			ptrs[i] = &cells[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, fmt.Errorf("context-layer: sql connector scan: %w", err)
		}
		row := make(map[string]any, len(cols))
		for i, name := range cols {
			// []byte (text/blob) decodes to a JSON string, not a base64 blob.
			if b, ok := cells[i].([]byte); ok {
				row[name] = string(b)
			} else {
				row[name] = cells[i]
			}
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("context-layer: sql connector rows: %w", err)
	}
	return out, nil
}

// --- Mock bureau connector ---
