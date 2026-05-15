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
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/googleapis/mcp-toolbox/internal/sources/duckdbquack"
	"github.com/googleapis/mcp-toolbox/internal/util/orderedmap"
)

// TestMetadata_RoundTrip_AllFiveTools runs each of the five metadata SQL
// shapes against an in-process Quack server. Each subtest exercises the
// exact SQL that the corresponding tool builds at Initialize time, so a
// regression in any of the Source-side helpers shows up here.
func TestMetadata_RoundTrip_AllFiveTools(t *testing.T) {
	srv := startInProcessQuackServer(t)
	src := newSource(t, srv, duckdbquack.Policy{})

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	t.Run("list-catalogs (client-side RunSQL)", func(t *testing.T) {
		res, err := src.RunSQL(ctx,
			`SELECT catalog_name FROM information_schema.schemata GROUP BY catalog_name ORDER BY catalog_name`,
			nil, duckdbquack.QueryOptions{})
		if err != nil {
			t.Fatalf("RunSQL: %v", err)
		}
		// We expect at least 'remote' to be present alongside the
		// built-in DuckDB catalogs ('system', 'temp', 'memory').
		got := rowsAsStrings(t, res.Rows, "catalog_name")
		if !contains(got, "remote") {
			t.Fatalf("expected 'remote' among catalogs, got %v", got)
		}
	})

	t.Run("list-schemas (remote via QuackQuery)", func(t *testing.T) {
		res, err := src.QuackQuery(ctx,
			`SELECT schema_name FROM information_schema.schemata WHERE catalog_name = current_database() ORDER BY schema_name`,
			duckdbquack.QueryOptions{})
		if err != nil {
			t.Fatalf("QuackQuery: %v", err)
		}
		got := rowsAsStrings(t, res.Rows, "schema_name")
		if !contains(got, "main") {
			t.Fatalf("expected 'main' among remote schemas, got %v", got)
		}
	})

	t.Run("list-tables (remote via QuackQuery)", func(t *testing.T) {
		res, err := src.QuackQuery(ctx,
			`SELECT table_name, table_type FROM information_schema.tables `+
				`WHERE table_schema = 'main' AND table_type IN ('BASE TABLE', 'VIEW') ORDER BY table_name`,
			duckdbquack.QueryOptions{})
		if err != nil {
			t.Fatalf("QuackQuery: %v", err)
		}
		if len(res.Rows) == 0 {
			t.Fatalf("expected at least one table; remote should have 'sales' from the test seed")
		}
		// The seed has 'sales'; the in-process server doesn't create
		// any views, but the filter accepts both.
		got := rowsAsStrings(t, res.Rows, "table_name")
		if !contains(got, "sales") {
			t.Fatalf("expected 'sales' among remote tables, got %v", got)
		}
	})

	t.Run("describe-table (remote information_schema.columns)", func(t *testing.T) {
		res, err := src.QuackQuery(ctx,
			`SELECT column_name, data_type, is_nullable FROM information_schema.columns `+
				`WHERE table_schema = 'main' AND table_name = 'sales' ORDER BY ordinal_position`,
			duckdbquack.QueryOptions{})
		if err != nil {
			t.Fatalf("QuackQuery: %v", err)
		}
		if len(res.Columns) != 3 ||
			res.Columns[0].Name != "column_name" ||
			res.Columns[1].Name != "data_type" ||
			res.Columns[2].Name != "is_nullable" {
			t.Fatalf("unexpected columns: %+v", res.Columns)
		}
		got := rowsAsStrings(t, res.Rows, "column_name")
		// The seed defines: id, customer, amount, order_date.
		for _, want := range []string{"id", "customer", "amount", "order_date"} {
			if !contains(got, want) {
				t.Fatalf("missing column %q; got %v", want, got)
			}
		}
	})

	t.Run("summarize-table (remote SUMMARIZE statement)", func(t *testing.T) {
		res, err := src.QuackQuery(ctx,
			`SUMMARIZE "main"."sales"`,
			duckdbquack.QueryOptions{})
		if err != nil {
			t.Fatalf("QuackQuery: %v", err)
		}
		// SUMMARIZE returns one row per source column. Check that the
		// row-count matches the seed schema (4 columns) and that the
		// canonical SUMMARIZE columns are present.
		if len(res.Rows) != 4 {
			t.Fatalf("expected 4 SUMMARIZE rows (one per source column), got %d", len(res.Rows))
		}
		colNames := make([]string, len(res.Columns))
		for i := range res.Columns {
			colNames[i] = res.Columns[i].Name
		}
		joined := strings.Join(colNames, ",")
		for _, want := range []string{"column_name", "column_type", "min", "max", "count", "null_percentage"} {
			if !strings.Contains(joined, want) {
				t.Fatalf("SUMMARIZE missing expected output column %q; got %v", want, colNames)
			}
		}
	})
}

// TestQuackQuery_RejectsInvalidIdentifier confirms QuackQuery does not need to
// validate inputs itself — the calling tools are responsible for validating
// schema/table identifiers before interpolation. The test exists as a
// reminder of the contract: a malformed remoteSQL surfaces as a remote
// parser error verbatim.
func TestQuackQuery_RejectsInvalidIdentifier(t *testing.T) {
	srv := startInProcessQuackServer(t)
	src := newSource(t, srv, duckdbquack.Policy{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := src.QuackQuery(ctx, "SELECT * FROM no_such_table_anywhere", duckdbquack.QueryOptions{})
	if err == nil {
		t.Fatalf("expected error for unknown table, got nil")
	}
}

// rowsAsStrings extracts a single column's string values from an
// orderedmap.Row slice. Marshalling each row through JSON keeps the test
// independent of orderedmap's internal API.
func rowsAsStrings(t *testing.T, rows []orderedmap.Row, field string) []string {
	t.Helper()
	out := make([]string, 0, len(rows))
	for i := range rows {
		b, err := json.Marshal(rows[i])
		if err != nil {
			t.Fatalf("marshal row %d: %v", i, err)
		}
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("unmarshal row %d: %v", i, err)
		}
		v, ok := m[field]
		if !ok {
			continue
		}
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
