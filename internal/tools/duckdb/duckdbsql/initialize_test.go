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
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/googleapis/mcp-toolbox/internal/sources"
	"github.com/googleapis/mcp-toolbox/internal/sources/duckdbquack"
)

// fakeSource implements sources.Source AND the compatibleSource interface used
// by the duckdb-sql tool, so Config.Initialize can run end-to-end without a
// real Quack server.
type fakeSource struct {
	policy duckdbquack.Policy
}

func (f *fakeSource) SourceType() string             { return duckdbquack.SourceType }
func (f *fakeSource) ToConfig() sources.SourceConfig { return duckdbquack.Config{} }
func (f *fakeSource) DuckDBQuackDB() *sql.DB         { return nil }
func (f *fakeSource) RunSQL(_ context.Context, _ string, _ []any, _ duckdbquack.QueryOptions) (*duckdbquack.QueryResult, error) {
	return &duckdbquack.QueryResult{}, nil
}
func (f *fakeSource) QuackQuery(_ context.Context, _ string, _ duckdbquack.QueryOptions) (*duckdbquack.QueryResult, error) {
	return &duckdbquack.QueryResult{}, nil
}
func (f *fakeSource) EffectivePolicy() duckdbquack.Policy { return f.policy }

func TestInitialize_AcceptsValidStatement(t *testing.T) {
	src := &fakeSource{}
	cfg := Config{
		Name:        "ok",
		Type:        "duckdb-sql",
		Source:      "s",
		Description: "d",
		Statement:   "SELECT 1",
	}
	tool, err := cfg.Initialize(map[string]sources.Source{"s": src})
	if err != nil {
		t.Fatalf("expected accept, got: %v", err)
	}
	if tool == nil {
		t.Fatalf("expected non-nil Tool")
	}
}

func TestInitialize_RejectsBadStatements(t *testing.T) {
	cases := []struct {
		desc      string
		statement string
		wantSub   string
	}{
		{"drop table", "DROP TABLE remote.sales", "not in the allowed set"},
		{"insert", "INSERT INTO remote.sales VALUES (1)", "not in the allowed set"},
		{"multi statement", "SELECT 1; SELECT 2", "more than one SQL statement"},
		{"forbidden create inside select", "SELECT 1 FROM (CREATE TABLE x AS SELECT 1)", `forbidden token "CREATE"`},
		{"unterminated string", "SELECT 'oops", "unterminated"},
	}
	src := &fakeSource{}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			cfg := Config{
				Name:        "t",
				Type:        "duckdb-sql",
				Source:      "s",
				Description: "d",
				Statement:   tc.statement,
			}
			_, err := cfg.Initialize(map[string]sources.Source{"s": src})
			if err == nil {
				t.Fatalf("expected reject, got accept for %q", tc.statement)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.wantSub)
			}
		})
	}
}

func TestInitialize_HonorsSourcePolicyAllowlist(t *testing.T) {
	// A policy that only permits SELECT should reject DESCRIBE/SHOW.
	src := &fakeSource{policy: duckdbquack.Policy{AllowedStatementKinds: []string{"SELECT"}}}
	cfg := Config{
		Name:        "t",
		Type:        "duckdb-sql",
		Source:      "s",
		Description: "d",
		Statement:   "DESCRIBE remote.sales",
	}
	_, err := cfg.Initialize(map[string]sources.Source{"s": src})
	if err == nil {
		t.Fatalf("expected reject, got accept")
	}
	if !strings.Contains(err.Error(), "not in the allowed set") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestInitialize_RejectsUnknownSource(t *testing.T) {
	cfg := Config{Name: "t", Type: "duckdb-sql", Source: "missing", Description: "d", Statement: "SELECT 1"}
	_, err := cfg.Initialize(map[string]sources.Source{})
	if err == nil || !strings.Contains(err.Error(), "unknown source") {
		t.Fatalf("expected unknown-source error, got: %v", err)
	}
}
