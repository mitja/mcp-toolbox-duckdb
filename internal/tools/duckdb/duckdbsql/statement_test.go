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

package duckdbsql

import (
	"strings"
	"testing"
)

func TestValidateStatement_Accepts(t *testing.T) {
	cases := []struct {
		desc string
		in   string
	}{
		{"simple select", "SELECT 1"},
		{"trailing semicolon", "SELECT 1;"},
		{"trailing whitespace after semicolon", "SELECT 1;\n  "},
		{"with cte", "WITH t AS (SELECT 1) SELECT * FROM t"},
		{"describe", "DESCRIBE remote.sales"},
		{"show tables", "SHOW TABLES"},
		{"explain", "EXPLAIN SELECT 1"},
		{"lowercase keyword", "select 1"},
		{"keyword in string", "SELECT 'do not DROP this'"},
		{"keyword in escape string", `SELECT E'INSERT into'`},
		{"keyword in line comment", "SELECT 1 -- DROP TABLE x\n"},
		{"keyword in block comment", "SELECT 1 /* INSERT INTO */ FROM remote.sales"},
		{"keyword as identifier substring", "SELECT inserted_at FROM remote.sales"},
		{"keyword as ident with underscore", "SELECT CREATE_TIMESTAMP FROM remote.sales"},
		{"escaped single quote", "SELECT 'it''s fine'"},
		{"escaped double quote", `SELECT "col""name" FROM remote.sales`},
		{"multiline statement", "SELECT\n  customer,\n  SUM(amount) AS revenue\nFROM remote.sales\nGROUP BY customer"},
		{"ilike with bound param", "SELECT * FROM remote.sales WHERE customer ILIKE '%' || ? || '%'"},
		{"values", "VALUES (1), (2), (3)"},
	}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			if err := ValidateStatement(tc.in, DefaultAllowedStatementKinds); err != nil {
				t.Fatalf("expected accept, got error: %v\nstatement: %q", err, tc.in)
			}
		})
	}
}

func TestValidateStatement_Rejects(t *testing.T) {
	cases := []struct {
		desc string
		in   string
		want string
	}{
		{"empty", "", "empty"},
		{"whitespace only", "   \n\t", "empty"},
		{"drop table", "DROP TABLE remote.sales", "not in the allowed set"},
		{"insert", "INSERT INTO remote.sales VALUES (1, 'x', 0, '2026-01-01')", "not in the allowed set"},
		{"update", "UPDATE remote.sales SET amount = 0", "not in the allowed set"},
		{"delete", "DELETE FROM remote.sales", "not in the allowed set"},
		{"create table", "CREATE TABLE x (a INT)", "not in the allowed set"},
		{"alter", "ALTER TABLE x ADD COLUMN y INT", "not in the allowed set"},
		{"copy", "COPY remote.sales TO '/tmp/x.csv'", "not in the allowed set"},
		{"install", "INSTALL httpfs", "not in the allowed set"},
		{"load", "LOAD httpfs", "not in the allowed set"},
		{"attach", "ATTACH 'foo.duckdb' AS f", "not in the allowed set"},
		{"detach", "DETACH remote", "not in the allowed set"},
		{"call", "CALL foo()", "not in the allowed set"},
		{"pragma", "PRAGMA database_list", "not in the allowed set"},
		{"set", "SET memory_limit = '1GB'", "not in the allowed set"},
		{"multi statement", "SELECT 1; SELECT 2", "more than one SQL statement"},
		{"multi statement with newline", "SELECT 1;\nSELECT 2", "more than one SQL statement"},
		{"select then drop", "SELECT 1; DROP TABLE x", "more than one SQL statement"},
		{"forbidden token in middle", "SELECT 1 UNION ALL INSERT INTO x VALUES (1)", `forbidden token "INSERT"`},
		{"forbidden create token", "SELECT 1 FROM (CREATE TABLE x AS SELECT 1) y", `forbidden token "CREATE"`},
		{"unterminated string", "SELECT 'abc", "unterminated"},
		{"unterminated block comment", "SELECT 1 /* abc", "unterminated"},
		{"unterminated identifier", `SELECT "abc`, "unterminated"},
		{"from-only", "FROM remote.sales", "not in the allowed set"},
	}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			err := ValidateStatement(tc.in, DefaultAllowedStatementKinds)
			if err == nil {
				t.Fatalf("expected reject, got accept\nstatement: %q", tc.in)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not contain %q\nstatement: %q", err.Error(), tc.want, tc.in)
			}
		})
	}
}

// TestStatementHash moved to duckdbmeta package after StatementHash and
// canonicalize were promoted there (all duckdb-* tools share one hashing
// helper now). See internal/tools/duckdb/duckdbmeta/duckdbmeta_test.go.
