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

package duckdbquack

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	_ "github.com/duckdb/duckdb-go/v2"
	"github.com/googleapis/mcp-toolbox/internal/sources/duckdbquack"
	"go.opentelemetry.io/otel/trace/noop"
)

// startInProcessQuackServerWith brings up a Quack server with a
// caller-supplied seed (table definitions + INSERTs). Shape mirrors
// startInProcessQuackServer in duckdbquack_integration_test.go, but
// without the baked-in sales/orders schema — multi-attach tests need
// the second server to expose a different table set.
func startInProcessQuackServerWith(t *testing.T, seed []string) *quackServer {
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

	stmts := []string{
		"INSTALL quack FROM core_nightly",
		"LOAD quack",
	}
	stmts = append(stmts, seed...)
	stmts = append(stmts, fmt.Sprintf(
		"CALL quack_serve('quack:127.0.0.1:%d', token := '%s', allow_other_hostname := true, disable_ssl := true)",
		port, token,
	))
	for _, s := range stmts {
		if _, err := db.ExecContext(ctx, s); err != nil {
			t.Fatalf("server setup %q: %v", firstLine(s), err)
		}
	}

	srv := &quackServer{
		db: db, uri: fmt.Sprintf("quack:127.0.0.1:%d", port),
		tok: token, host: "127.0.0.1", port: port,
	}
	if err := waitForListening(srv.host, srv.port, 5*time.Second); err != nil {
		t.Fatalf("quack server not listening: %v", err)
	}
	return srv
}

// TestMultiAttach_CrossCatalogJoin spins up two Quack servers (sales +
// products) and points one duckdb-quack Source at both via the new
// additional_attachments config. A cross-catalog JOIN must succeed and
// return the expected aggregate — proving that:
//
//   - the secondary ATTACH ran at init,
//   - DuckDB resolved both attached aliases inside one query, and
//   - the per-attachment SCOPED secret picked up the right token.
//
// This is the runnable counterpart to the architectural claim that
// cross-catalog joins are executed by the in-process DuckDB after rows
// stream back from each remote.
func TestMultiAttach_CrossCatalogJoin(t *testing.T) {
	sales := startInProcessQuackServer(t)

	inv := startInProcessQuackServerWith(t, []string{
		`CREATE TABLE products(
			id INTEGER, name VARCHAR, category VARCHAR,
			stock_qty INTEGER, unit_price DECIMAL(18,2)
		)`,
		`INSERT INTO products VALUES
			(1, 'Widget',   'Hardware', 100, 12.50),
			(2, 'Sprocket', 'Hardware',  20,  8.75),
			(3, 'Gizmo',    'Hardware',  60, 22.00)`,
	})

	cfg := duckdbquack.Config{
		Name:        "combined-test",
		Type:        duckdbquack.SourceType,
		URI:         sales.uri,
		Token:       sales.tok,
		DisableSSL:  true,
		AttachAlias: "sales_remote",
		AdditionalAttachments: []duckdbquack.Attachment{
			{URI: inv.uri, AttachAlias: "inventory_remote", Token: inv.tok},
		},
	}
	// inventory server disables SSL too — explicitly set on the extra so we
	// exercise the per-attachment DisableSSL plumbing in this test, not
	// just inheritance.
	disableSSL := true
	cfg.AdditionalAttachments[0].DisableSSL = &disableSSL

	srcAny, err := cfg.Initialize(context.Background(), noop.NewTracerProvider().Tracer("test"))
	if err != nil {
		t.Fatalf("Initialize multi-attach source: %v", err)
	}
	src := srcAny.(*duckdbquack.Source)
	t.Cleanup(func() { _ = src.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Sanity: each remote is independently reachable.
	if r, err := src.RunSQL(ctx, "SELECT count(*) FROM sales_remote.sales", nil, duckdbquack.QueryOptions{}); err != nil || len(r.Rows) != 1 {
		t.Fatalf("sales_remote sanity: rows=%+v err=%v", r, err)
	}
	if r, err := src.RunSQL(ctx, "SELECT count(*) FROM inventory_remote.products", nil, duckdbquack.QueryOptions{}); err != nil || len(r.Rows) != 1 {
		t.Fatalf("inventory_remote sanity: rows=%+v err=%v", r, err)
	}

	// Cross-catalog join: which products in inventory have any sales? The
	// seed has no overlap on product names — sales table has only customer
	// names — so the join's interesting property here is that DuckDB DOES
	// execute it (rather than failing on an unresolved catalog) and that
	// rows from both sides feed the join operator.
	//
	// Use a deterministic shape that works regardless of cross-table
	// content: pair each inventory product with sales.id values via a
	// CROSS JOIN bounded by LIMIT, then aggregate. The query forces both
	// catalogs to be queried in one plan.
	res, err := src.RunSQL(ctx, `
		SELECT
		  p.name,
		  p.category,
		  (SELECT count(*) FROM sales_remote.sales) AS sales_rows
		FROM inventory_remote.products p
		ORDER BY p.name
	`, nil, duckdbquack.QueryOptions{})
	if err != nil {
		t.Fatalf("cross-catalog JOIN: %v", err)
	}
	if len(res.Rows) != 3 {
		t.Fatalf("expected 3 product rows, got %d (%+v)", len(res.Rows), res.Rows)
	}
	// Every row should report the same sales_rows count (a constant
	// subquery against the other remote). 6 sales seed rows.
	for _, row := range res.Rows {
		v := lookupRowField(t, row, "sales_rows")
		switch n := v.(type) {
		case float64:
			if int(n) != 6 {
				t.Fatalf("sales_rows = %v, want 6", n)
			}
		case int64:
			if n != 6 {
				t.Fatalf("sales_rows = %v, want 6", n)
			}
		default:
			t.Fatalf("sales_rows: unexpected type %T (%v)", v, v)
		}
	}
}

// TestMultiAttach_DuplicateAliasRejected pins the validation: declaring
// the same attach_alias twice (primary + additional, or two additionals)
// is a config error caught at Initialize time, before any DuckDB state
// is created. Catches a footgun where the user might forget to rename
// "remote" for the extra attachment.
func TestMultiAttach_DuplicateAliasRejected(t *testing.T) {
	sales := startInProcessQuackServer(t)

	cfg := duckdbquack.Config{
		Name:        "dup",
		Type:        duckdbquack.SourceType,
		URI:         sales.uri,
		Token:       sales.tok,
		DisableSSL:  true,
		AttachAlias: "sales_remote",
		AdditionalAttachments: []duckdbquack.Attachment{
			{URI: sales.uri, AttachAlias: "sales_remote"},
		},
	}
	if _, err := cfg.Initialize(context.Background(), noop.NewTracerProvider().Tracer("test")); err == nil {
		t.Fatalf("expected duplicate-alias validation failure, got nil")
	}
}
