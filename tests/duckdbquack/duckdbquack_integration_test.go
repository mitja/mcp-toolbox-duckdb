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

// Package duckdbquack holds the duckdb-quack integration tests. The Quack
// server is brought up in-process as a second DuckDB instance bound to
// localhost on a random free port; the client Source connects to it. This
// avoids any external dependency on Docker or a separate Quack server image.
//
// The first run of these tests downloads the Quack extension from DuckDB's
// core_nightly repository (~once per machine). Subsequent runs hit the
// extension cache.
package duckdbquack

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "github.com/duckdb/duckdb-go/v2"
	"github.com/googleapis/mcp-toolbox/internal/sources/duckdbquack"
	"go.opentelemetry.io/otel/trace/noop"
)

// quackServer is a Quack-serving DuckDB instance hosted in the test process.
type quackServer struct {
	db   *sql.DB
	uri  string // e.g., "quack:127.0.0.1:54321"
	tok  string
	host string
	port int
}

// startInProcessQuackServer brings up a Quack server inside the current Go
// process. It listens on 127.0.0.1 on a random free port, serves a small
// `sales` and `orders` schema, and applies the read-only authorization macro
// from spec §6.2.
func startInProcessQuackServer(t *testing.T) *quackServer {
	t.Helper()

	port := freeTCPPort(t)
	token := "test-token-12345678"

	db, err := sql.Open("duckdb", "")
	if err != nil {
		t.Fatalf("open server duckdb: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Seed data and start the Quack server with default authentication
	// (client TOKEN must equal the bootstrap token). The read-only
	// authorization macro is created here but NOT activated yet: setting
	// quack_authorization_function before any client ATTACH causes ATTACH's
	// internal catalog queries to be rejected. Tests that need
	// authorization enforcement call enableReadOnlyAuthz() after the
	// client is attached.
	stmts := []string{
		"INSTALL quack FROM core_nightly",
		"LOAD quack",
		`CREATE TABLE sales(
			id INTEGER,
			customer VARCHAR,
			amount DECIMAL(18,2),
			order_date DATE
		)`,
		`INSERT INTO sales VALUES
			(1, 'Alice GmbH', 1000.00, DATE '2026-01-01'),
			(2, 'Alice GmbH',  250.50, DATE '2026-01-15'),
			(3, 'Bob Corp',    500.00, DATE '2026-02-01'),
			(4, 'Carol AG',    750.00, DATE '2026-03-10'),
			(5, 'Alice GmbH',  120.00, DATE '2026-04-02'),
			(6, 'Daniel SARL', 980.00, DATE '2026-04-15')`,
		`CREATE OR REPLACE MACRO read_only(sid, query) AS (
			regexp_matches(upper(trim(query)), '^(SELECT|WITH|EXPLAIN|DESCRIBE|SHOW)\b')
		)`,
		fmt.Sprintf(
			"CALL quack_serve('quack:127.0.0.1:%d', token := '%s', allow_other_hostname := true, disable_ssl := true)",
			port, token,
		),
	}
	for _, s := range stmts {
		if _, err := db.ExecContext(ctx, s); err != nil {
			t.Fatalf("server setup %q: %v", firstLine(s), err)
		}
	}

	srv := &quackServer{
		db:   db,
		uri:  fmt.Sprintf("quack:127.0.0.1:%d", port),
		tok:  token,
		host: "127.0.0.1",
		port: port,
	}

	// Give the server a moment to start accepting TCP connections.
	if err := waitForListening(srv.host, srv.port, 5*time.Second); err != nil {
		t.Fatalf("quack server not listening: %v", err)
	}
	return srv
}

// freeTCPPort returns a port that was free at the moment of the call. There
// is an inherent race with anything else binding the same port before the
// caller does, but it is good enough for a local test.
func freeTCPPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for free port: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func waitForListening(host string, port int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	var lastErr error
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		lastErr = err
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("port %s did not start listening within %s: %w", addr, timeout, lastErr)
}

func firstLine(s string) string {
	for i, c := range s {
		if c == '\n' {
			return s[:i]
		}
	}
	return s
}

// newSource builds a duckdb-quack client source pointing at the in-process
// Quack server. The Source has the ATTACH already in place under alias
// "remote". Cleanup is registered with t.Cleanup.
func newSource(t *testing.T, srv *quackServer, policy duckdbquack.Policy) *duckdbquack.Source {
	t.Helper()
	cfg := duckdbquack.Config{
		Name:       "test-source",
		Type:       duckdbquack.SourceType,
		URI:        srv.uri,
		Token:      srv.tok,
		DisableSSL: true,
		Policy:     policy,
	}
	srcAny, err := cfg.Initialize(context.Background(), noop.NewTracerProvider().Tracer("test"))
	if err != nil {
		t.Fatalf("source.Initialize: %v", err)
	}
	src := srcAny.(*duckdbquack.Source)
	t.Cleanup(func() { _ = src.Close() })
	return src
}

// --- Tests -----------------------------------------------------------------

// TestPing exercises the happy path of bringing up the in-process Quack
// server and attaching a client to it. If this fails, nothing else will work.
func TestPing(t *testing.T) {
	srv := startInProcessQuackServer(t)
	src := newSource(t, srv, duckdbquack.Policy{})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	res, err := src.RunSQL(ctx, "SELECT 1 AS one", nil, duckdbquack.QueryOptions{})
	if err != nil {
		t.Fatalf("RunSQL SELECT 1: %v", err)
	}
	if len(res.Rows) != 1 || len(res.Columns) != 1 || res.Columns[0].Name != "one" {
		t.Fatalf("unexpected result: %+v", res)
	}
}

// TestParameterBinding_AcrossAttach is the Day-2 risk-gate test. It runs a
// matrix of bound-parameter types against an attached Quack catalog table.
// If any case fails, the duckdb-sql tool MUST restrict itself to parameterless
// server-side macros (see PLAN.md §"Parameter binding risk").
func TestParameterBinding_AcrossAttach(t *testing.T) {
	srv := startInProcessQuackServer(t)
	src := newSource(t, srv, duckdbquack.Policy{})

	type tc struct {
		desc   string
		stmt   string
		params []any
		want   int // expected row count
	}
	cases := []tc{
		{"T1 string =", "SELECT * FROM remote.sales WHERE customer = ?", []any{"Alice GmbH"}, 3},
		{"T2 float64 >", "SELECT * FROM remote.sales WHERE amount > ?", []any{float64(700)}, 3},
		{"T3 int IN", "SELECT * FROM remote.sales WHERE id IN (?, ?, ?)", []any{int64(1), int64(3), int64(5)}, 3},
		{"T4 date >=", "SELECT * FROM remote.sales WHERE order_date >= ?", []any{time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)}, 3},
		{"T5 ILIKE pattern", "SELECT * FROM remote.sales WHERE customer ILIKE '%' || ? || '%'", []any{"gmbh"}, 3},
		{"T6 $1 named", "SELECT * FROM remote.sales WHERE customer = $1", []any{"Bob Corp"}, 1},
	}

	for _, c := range cases {
		t.Run(c.desc, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			res, err := src.RunSQL(ctx, c.stmt, c.params, duckdbquack.QueryOptions{})
			if err != nil {
				t.Fatalf("RunSQL: %v\nstmt: %s\nparams: %#v", err, c.stmt, c.params)
			}
			if len(res.Rows) != c.want {
				t.Fatalf("row count: got %d, want %d (rows=%+v)", len(res.Rows), c.want, res.Rows)
			}
		})
	}
}

// TestTypedJSONShape verifies that RunSQL captures DuckDB column types so
// the tool layer can produce the spec §7 response shape.
func TestTypedJSONShape(t *testing.T) {
	srv := startInProcessQuackServer(t)
	src := newSource(t, srv, duckdbquack.Policy{})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	res, err := src.RunSQL(ctx,
		"SELECT customer, SUM(amount) AS revenue FROM remote.sales GROUP BY customer ORDER BY revenue DESC",
		nil, duckdbquack.QueryOptions{})
	if err != nil {
		t.Fatalf("RunSQL: %v", err)
	}
	if len(res.Columns) != 2 || res.Columns[0].Name != "customer" || res.Columns[1].Name != "revenue" {
		t.Fatalf("unexpected columns: %+v", res.Columns)
	}
	if res.Columns[0].Type != "VARCHAR" {
		t.Fatalf("customer type: got %q, want VARCHAR", res.Columns[0].Type)
	}
	// SUM of DECIMAL(18,2) is reported as DECIMAL(38,2) by DuckDB; just
	// assert it's a DECIMAL of some flavor so the test isn't brittle to
	// minor DuckDB version changes.
	if got := res.Columns[1].Type; got == "" || got[:7] != "DECIMAL" {
		t.Fatalf("revenue type: got %q, want DECIMAL(...)", got)
	}
	if res.Truncated {
		t.Fatalf("expected Truncated=false for 4-row result")
	}
}

// TestMaxRowsTruncates verifies that the source-level MaxRows cap is honored
// and that Truncated is set when extra rows are dropped.
func TestMaxRowsTruncates(t *testing.T) {
	srv := startInProcessQuackServer(t)
	src := newSource(t, srv, duckdbquack.Policy{})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// remote.sales has 6 rows; cap to 3.
	res, err := src.RunSQL(ctx,
		"SELECT * FROM remote.sales ORDER BY id",
		nil, duckdbquack.QueryOptions{MaxRows: 3})
	if err != nil {
		t.Fatalf("RunSQL: %v", err)
	}
	if len(res.Rows) != 3 {
		t.Fatalf("row count: got %d, want 3", len(res.Rows))
	}
	if !res.Truncated {
		t.Fatalf("expected Truncated=true when MaxRows=3 over 6 rows")
	}
}

// TestTimeoutCancels verifies that a context deadline propagates through to
// the underlying query.
func TestTimeoutCancels(t *testing.T) {
	srv := startInProcessQuackServer(t)
	src := newSource(t, srv, duckdbquack.Policy{})

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	// Cartesian product of two large ranges — guaranteed to overrun 200ms.
	_, err := src.RunSQL(ctx,
		"SELECT count(*) FROM range(1, 10000000) t1, range(1, 10000) t2",
		nil, duckdbquack.QueryOptions{})
	if err == nil {
		t.Fatalf("expected timeout error, got nil")
	}
}

// enableReadOnlyAuthz activates the read_only authorization macro on the
// server side. Must be called AFTER the client has ATTACHed — installing
// authz earlier breaks ATTACH's internal catalog queries.
func enableReadOnlyAuthz(t *testing.T, srv *quackServer) {
	t.Helper()
	if _, err := srv.db.ExecContext(context.Background(),
		"SET GLOBAL quack_authorization_function = 'read_only'"); err != nil {
		t.Fatalf("enable read_only authz: %v", err)
	}
	t.Cleanup(func() {
		_, _ = srv.db.ExecContext(context.Background(),
			"RESET GLOBAL quack_authorization_function")
	})
}

// TestDecimalRenderedAsString proves that DECIMAL cell values come back as
// strings in the QueryResult (spec §7 type rules: preserve precision).
func TestDecimalRenderedAsString(t *testing.T) {
	srv := startInProcessQuackServer(t)
	src := newSource(t, srv, duckdbquack.Policy{})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	res, err := src.RunSQL(ctx,
		"SELECT customer, SUM(amount) AS revenue FROM remote.sales WHERE customer = ? GROUP BY customer",
		[]any{"Alice GmbH"}, duckdbquack.QueryOptions{})
	if err != nil {
		t.Fatalf("RunSQL: %v", err)
	}
	if len(res.Rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(res.Rows))
	}
	revenue := lookupRowField(t, res.Rows[0], "revenue")
	s, ok := revenue.(string)
	if !ok {
		t.Fatalf("revenue rendered as %T (%v), want string", revenue, revenue)
	}
	// Sum of (1000.00 + 250.50 + 120.00) = 1370.50. The duckdb-go Decimal
	// type's String() strips trailing zeros, so the rendered string is
	// "1370.5" — the value is preserved, the cosmetic scale is not.
	// (A future polish could re-format to the column's declared scale.)
	if s != "1370.5" {
		t.Fatalf("revenue value: got %q, want %q", s, "1370.5")
	}
}

// TestBlobRenderedAsSentinel proves that BLOB columns are not exposed
// verbatim to callers; they are replaced by a "<blob: N bytes>" sentinel.
func TestBlobRenderedAsSentinel(t *testing.T) {
	srv := startInProcessQuackServer(t)
	src := newSource(t, srv, duckdbquack.Policy{})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	res, err := src.RunSQL(ctx, "SELECT 'hello'::BLOB AS payload", nil, duckdbquack.QueryOptions{})
	if err != nil {
		t.Fatalf("RunSQL: %v", err)
	}
	if len(res.Columns) != 1 || res.Columns[0].Type != "BLOB" {
		t.Fatalf("expected one BLOB column, got %+v", res.Columns)
	}
	payload := lookupRowField(t, res.Rows[0], "payload")
	s, ok := payload.(string)
	if !ok {
		t.Fatalf("payload rendered as %T (%v), want sentinel string", payload, payload)
	}
	want := "<blob: 5 bytes>"
	if s != want {
		t.Fatalf("blob sentinel: got %q, want %q", s, want)
	}
}

// lookupRowField extracts a named field from an orderedmap.Row. orderedmap
// keeps insertion order so the field is found by a linear walk over the
// JSON-marshaled form. We unmarshal the marshaled output to keep this test
// independent of internal-tree orderedmap APIs.
func lookupRowField(t *testing.T, row any, field string) any {
	t.Helper()
	b, err := json.Marshal(row)
	if err != nil {
		t.Fatalf("marshal row: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal row: %v", err)
	}
	v, ok := m[field]
	if !ok {
		t.Fatalf("field %q not present in row: %+v", field, m)
	}
	return v
}

// TestReAttach_RecoversFromMissingCatalog proves that Source.RunSQL detects
// a lost ATTACH (here simulated by issuing DETACH directly through the
// underlying *sql.DB) and transparently re-bootstraps the conn before
// retrying the user query.
//
// This exercises the same code path that fires when the Quack server
// restarts and the database/sql pool replaces the now-bad TCP connection
// with a fresh one that has no ATTACH state. Detaching is the most
// deterministic way to reproduce the "catalog gone, but the conn still
// works" half of that scenario inside a single-process test.
func TestReAttach_RecoversFromMissingCatalog(t *testing.T) {
	srv := startInProcessQuackServer(t)
	src := newSource(t, srv, duckdbquack.Policy{})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Sanity: query works before we break anything.
	if _, err := src.RunSQL(ctx, "SELECT id FROM remote.sales LIMIT 1", nil, duckdbquack.QueryOptions{}); err != nil {
		t.Fatalf("baseline query: %v", err)
	}

	// Simulate ATTACH loss by detaching directly through the underlying *sql.DB.
	if _, err := src.DuckDBQuackDB().ExecContext(ctx, "DETACH remote"); err != nil {
		t.Fatalf("manual DETACH: %v", err)
	}

	// A raw query through *sql.DB now fails — no reconnect logic there.
	if _, err := src.DuckDBQuackDB().QueryContext(ctx, "SELECT id FROM remote.sales LIMIT 1"); err == nil {
		t.Fatalf("expected raw query to fail after DETACH, got nil")
	}

	// RunSQL must detect the missing catalog and re-attach transparently.
	res, err := src.RunSQL(ctx, "SELECT id FROM remote.sales ORDER BY id LIMIT 1", nil, duckdbquack.QueryOptions{})
	if err != nil {
		t.Fatalf("RunSQL after DETACH should auto-reattach, got: %v", err)
	}
	if len(res.Rows) != 1 {
		t.Fatalf("expected 1 row after reattach, got %d", len(res.Rows))
	}

	// And a subsequent query should also work, confirming the re-attach
	// stuck rather than being one-shot.
	if _, err := src.RunSQL(ctx, "SELECT count(*) FROM remote.sales", nil, duckdbquack.QueryOptions{}); err != nil {
		t.Fatalf("second post-reattach query: %v", err)
	}
}

// TestReAttach_DoesNotMaskUnrelatedErrors confirms that errors which are
// *not* signals of lost ATTACH (here: a syntactically invalid statement) are
// returned verbatim, without going through the retry path.
func TestReAttach_DoesNotMaskUnrelatedErrors(t *testing.T) {
	srv := startInProcessQuackServer(t)
	src := newSource(t, srv, duckdbquack.Policy{})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := src.RunSQL(ctx, "SELECT id FROM remote.sales WHERE", nil, duckdbquack.QueryOptions{})
	if err == nil {
		t.Fatalf("expected syntax error, got nil")
	}
	// A re-attach retry would surface "(re-attach also failed: …)" in the
	// error chain; the original syntax error should pass through cleanly.
	if strings.Contains(err.Error(), "re-attach") {
		t.Fatalf("syntax error should not trigger re-attach path; got %v", err)
	}
}

// TestServerSideAuthz_RejectsInsert proves that the read-only authorization
// macro on the Quack server is the real boundary: even when a destructive
// statement is issued directly through the underlying *sql.DB (bypassing the
// duckdb-sql tool's config-load validation), the server refuses it.
func TestServerSideAuthz_RejectsInsert(t *testing.T) {
	srv := startInProcessQuackServer(t)
	src := newSource(t, srv, duckdbquack.Policy{})
	enableReadOnlyAuthz(t, srv)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := src.DuckDBQuackDB().ExecContext(ctx,
		"INSERT INTO remote.sales VALUES (99, 'X', 1.00, DATE '2026-05-01')")
	if err == nil {
		t.Fatalf("expected server-side authorization rejection, got nil")
	}
}
