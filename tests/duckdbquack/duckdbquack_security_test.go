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
	"strings"
	"testing"
	"time"

	"github.com/googleapis/mcp-toolbox/internal/sources"
	"github.com/googleapis/mcp-toolbox/internal/sources/duckdbquack"
	"github.com/googleapis/mcp-toolbox/internal/tools"
	"github.com/googleapis/mcp-toolbox/internal/tools/duckdb/duckdbexecutesql"
	"github.com/googleapis/mcp-toolbox/internal/util/parameters"
)

// sourceMap is a tiny tools.SourceProvider that wraps a single source so the
// security tests can call Tool.Invoke directly without standing up Toolbox.
type sourceMap map[string]sources.Source

func (m sourceMap) GetSource(name string) (sources.Source, bool) {
	s, ok := m[name]
	return s, ok
}

// newExecuteTool builds a registered duckdb-execute-sql Tool bound to the
// in-process Quack server. The tool is enabled at config-load (the
// production-hardened path); each test then calls Invoke with a different
// sql parameter to exercise the validator.
func newExecuteTool(t *testing.T, srv *quackServer, policy duckdbquack.Policy) (tools.Tool, sourceMap) {
	t.Helper()
	src := newSource(t, srv, policy)
	srcs := sourceMap{"sales-quack": src}

	cfg := duckdbexecutesql.Config{
		Name:        "dev_execute_sql",
		Type:        "duckdb-execute-sql",
		Source:      "sales-quack",
		Description: "Dev-only ad-hoc SQL.",
		Enabled:     boolPtr(true),
	}
	tool, err := cfg.Initialize(map[string]sources.Source{"sales-quack": src})
	if err != nil {
		t.Fatalf("duckdb-execute-sql Initialize: %v", err)
	}
	return tool, srcs
}

func boolPtr(b bool) *bool { return &b }

// invoke is a thin wrapper that constructs the {sql: <value>} ParamValues
// and runs Tool.Invoke. Returns the response and the error string (empty
// if the call succeeded).
func invoke(t *testing.T, tool tools.Tool, srcs sourceMap, sql string) (any, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pv := parameters.ParamValues{{Name: "sql", Value: sql}}
	res, err := tool.Invoke(ctx, srcs, pv, "")
	if err != nil {
		return nil, err.Error()
	}
	return res, ""
}

func TestExecuteSQL_HappyPath(t *testing.T) {
	srv := startInProcessQuackServer(t)
	tool, srcs := newExecuteTool(t, srv, duckdbquack.Policy{})

	res, errMsg := invoke(t, tool, srcs, "SELECT count(*) AS n FROM remote.sales")
	if errMsg != "" {
		t.Fatalf("valid SELECT should succeed, got error: %s", errMsg)
	}
	if res == nil {
		t.Fatalf("expected non-nil response")
	}
}

// TestExecuteSQL_RejectsDestructiveStatements exercises the same validator
// path that duckdb-sql uses at config-load — proves duckdb-execute-sql's
// per-invocation validation honors the deny list.
func TestExecuteSQL_RejectsDestructiveStatements(t *testing.T) {
	srv := startInProcessQuackServer(t)
	tool, srcs := newExecuteTool(t, srv, duckdbquack.Policy{})

	cases := []struct {
		desc, sql, wantSubstr string
	}{
		{"drop table", "DROP TABLE remote.sales", "rejected by policy"},
		{"insert", "INSERT INTO remote.sales VALUES (1, 'x', 1.00, DATE '2026-01-01')", "rejected by policy"},
		{"update", "UPDATE remote.sales SET amount = 0", "rejected by policy"},
		{"delete", "DELETE FROM remote.sales", "rejected by policy"},
		{"alter", "ALTER TABLE remote.sales ADD COLUMN x INT", "rejected by policy"},
		{"create table", "CREATE TABLE x (a INT)", "rejected by policy"},
		{"copy", "COPY remote.sales TO '/tmp/x.csv'", "rejected by policy"},
		{"install", "INSTALL httpfs", "rejected by policy"},
		{"attach", "ATTACH 'foo.duckdb' AS f", "rejected by policy"},
		{"call", "CALL foo()", "rejected by policy"},
		{"pragma", "PRAGMA database_list", "rejected by policy"},
		{"set", "SET memory_limit = '1GB'", "rejected by policy"},
		{"multi statement", "SELECT 1; SELECT 2", "rejected by policy"},
		{"select then drop", "SELECT 1; DROP TABLE x", "rejected by policy"},
		{"forbidden token in middle", "SELECT 1 UNION ALL INSERT INTO x VALUES (1)", "rejected by policy"},
	}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			_, errMsg := invoke(t, tool, srcs, tc.sql)
			if errMsg == "" {
				t.Fatalf("expected agent error, got success for %q", tc.sql)
			}
			if !strings.Contains(errMsg, tc.wantSubstr) {
				t.Fatalf("error %q does not contain %q", errMsg, tc.wantSubstr)
			}
		})
	}
}

// TestExecuteSQL_HonorsForbiddenPatterns confirms that an operator-supplied
// extension to the deny list (Source.Policy.ForbiddenPatterns) propagates
// to the per-invocation check.
func TestExecuteSQL_HonorsForbiddenPatterns(t *testing.T) {
	srv := startInProcessQuackServer(t)
	policy := duckdbquack.Policy{
		ForbiddenPatterns: []string{"EXPORT", "IMPORT"},
	}
	tool, srcs := newExecuteTool(t, srv, policy)

	// Baseline: the built-in deny list does NOT include EXPORT, so without
	// ForbiddenPatterns this would reach the database. With the extra
	// pattern in policy, the tool rejects it.
	_, errMsg := invoke(t, tool, srcs, "SELECT 1 EXPORT")
	if errMsg == "" {
		t.Fatalf("expected reject for EXPORT in ForbiddenPatterns")
	}
	if !strings.Contains(errMsg, "EXPORT") {
		t.Fatalf("error should mention EXPORT, got: %s", errMsg)
	}

	// Plain SELECT still passes.
	if _, e := invoke(t, tool, srcs, "SELECT 1"); e != "" {
		t.Fatalf("plain SELECT should pass even with ForbiddenPatterns set, got: %s", e)
	}
}

// TestExecuteSQL_HonorsCustomAllowedStatementKinds verifies that an
// operator who narrows the allowlist (e.g., only `SELECT` and `WITH`) gets
// further-restricted behavior from the dev tool.
func TestExecuteSQL_HonorsCustomAllowedStatementKinds(t *testing.T) {
	srv := startInProcessQuackServer(t)
	policy := duckdbquack.Policy{AllowedStatementKinds: []string{"SELECT"}}
	tool, srcs := newExecuteTool(t, srv, policy)

	// DESCRIBE is in the built-in default allowlist but not in the narrowed
	// policy.AllowedStatementKinds, so it must be rejected.
	if _, errMsg := invoke(t, tool, srcs, "DESCRIBE remote.sales"); errMsg == "" {
		t.Fatalf("narrowed allowlist should reject DESCRIBE")
	}

	// SELECT still passes.
	if _, errMsg := invoke(t, tool, srcs, "SELECT 1"); errMsg != "" {
		t.Fatalf("SELECT should still pass, got: %s", errMsg)
	}
}
