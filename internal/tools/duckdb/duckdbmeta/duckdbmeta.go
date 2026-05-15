// Copyright 2026 Mitja Martini
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package duckdbmeta holds the small set of shared building blocks used by
// every duckdb-* tool: the CompatibleSource interface that pins the contract
// a source must satisfy, the Response struct that all tools return (spec §7
// JSON shape), the Invoke helper that applies the source's Policy and shapes
// the response, and identifier validation/quoting helpers for SQL that is
// built at tool-config-load time from operator-supplied scope values.
package duckdbmeta

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/googleapis/mcp-toolbox/internal/sources/duckdbquack"
)

// CompatibleSource is the duck-typed contract a Source must satisfy to back
// any duckdb-* tool. The duckdb-quack source implements it today. The
// interface is intentionally narrow — the tools never reach into the
// underlying *sql.DB except for the type-unique marker method that tells
// the tool registry "this source goes with this tool family".
type CompatibleSource interface {
	// DuckDBQuackDB is the type-unique marker. Its return value is not used
	// by the tools; presence of the method is how tools.GetCompatibleSource
	// distinguishes a duckdb-quack source from any other source.
	DuckDBQuackDB() *sql.DB

	// RunSQL executes a statement against the source's client DuckDB. The
	// duckdb-sql tool uses this path; metadata tools generally prefer
	// QuackQuery because information_schema and similar metadata catalogs
	// do not traverse the ATTACH cleanly.
	RunSQL(ctx context.Context, statement string, params []any, opts duckdbquack.QueryOptions) (*duckdbquack.QueryResult, error)

	// QuackQuery executes a SQL statement directly on the remote Quack
	// server via the `quack_query()` table function. Metadata tools use
	// this path so DESCRIBE, SUMMARIZE, SHOW TABLES, and queries against
	// information_schema run on the remote rather than against the local
	// view of the ATTACHed catalog (which is incomplete).
	QuackQuery(ctx context.Context, remoteSQL string, opts duckdbquack.QueryOptions) (*duckdbquack.QueryResult, error)

	// EffectivePolicy returns the resolved Policy (timeout, max_rows,
	// allowed_statement_kinds, …) after defaults are applied. Tools read
	// it once per Invoke to apply per-invocation limits.
	EffectivePolicy() duckdbquack.Policy
}

// Response is the JSON shape returned by every duckdb-* tool's Invoke. See
// spec §7. The encoded form is the value bound into the MCP `result` field
// (Toolbox's chi router stringifies it once more on the way out).
type Response struct {
	Columns       []duckdbquack.Column `json:"columns"`
	Rows          []any                `json:"rows"`
	RowCount      int                  `json:"row_count"`
	Truncated     bool                 `json:"truncated"`
	Source        string               `json:"source"`
	StatementHash string               `json:"statement_hash"`
}

// Runner is the inner function each tool supplies to Invoke. It returns a
// raw QueryResult; Invoke is responsible for wrapping it in Response and
// applying the per-invocation timeout + max-rows from the source's Policy.
type Runner func(ctx context.Context, opts duckdbquack.QueryOptions) (*duckdbquack.QueryResult, error)

// Invoke applies the source's Policy (timeout via context deadline,
// max_rows via QueryOptions) and shapes the runner's result into a
// Response. Returns the runner's error verbatim — the caller wraps it
// with util.ProcessGeneralError or similar for the Toolbox MCP layer.
func Invoke(ctx context.Context, src CompatibleSource, sourceName, statementHash string, run Runner) (*Response, error) {
	policy := src.EffectivePolicy()
	ctx, cancel := context.WithTimeout(ctx, policy.Timeout)
	defer cancel()

	res, err := run(ctx, duckdbquack.QueryOptions{MaxRows: policy.MaxRows})
	if err != nil {
		return nil, err
	}

	rows := make([]any, len(res.Rows))
	for i := range res.Rows {
		rows[i] = res.Rows[i]
	}
	return &Response{
		Columns:       res.Columns,
		Rows:          rows,
		RowCount:      len(res.Rows),
		Truncated:     res.Truncated,
		Source:        sourceName,
		StatementHash: statementHash,
	}, nil
}

// StatementHash returns "sha256:<hex>" of a whitespace-canonical form of
// the statement. Identical SQL that differs only in whitespace hashes the
// same. Used as a stable, log-safe identifier — the SQL itself never goes
// into Toolbox logs.
func StatementHash(statement string) string {
	canon := canonicalize(statement)
	sum := sha256.Sum256([]byte(canon))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func canonicalize(s string) string {
	var b strings.Builder
	prevSpace := false
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			if !prevSpace {
				b.WriteByte(' ')
				prevSpace = true
			}
			continue
		}
		prevSpace = false
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}

// ValidateIdentifier accepts ASCII identifiers safe to interpolate into
// metadata-tool SQL: a leading letter or underscore, followed by letters,
// digits, or underscores. Used to validate scope fields (catalog, schema,
// table) at tool config-load time.
//
// This is intentionally conservative — DuckDB accepts a broader set of
// identifiers when quoted, but Phase 2 only needs the conventional subset.
// A future tool that needs reserved-word or dotted identifiers can use
// QuoteIdentifier on a pre-validated string.
func ValidateIdentifier(s string) error {
	if s == "" {
		return fmt.Errorf("identifier must not be empty")
	}
	for i, r := range s {
		ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '_' ||
			(i > 0 && r >= '0' && r <= '9')
		if !ok {
			return fmt.Errorf("identifier %q contains invalid character %q at position %d", s, r, i)
		}
	}
	return nil
}

// QuoteIdentifier wraps a validated identifier in DuckDB double quotes so
// it round-trips correctly even when the identifier shadows a reserved
// word. Callers must validate the identifier first (see ValidateIdentifier);
// this function does not.
func QuoteIdentifier(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}
