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

package duckdbexecutesql

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/googleapis/mcp-toolbox/internal/sources"
	"github.com/googleapis/mcp-toolbox/internal/sources/duckdbquack"
)

// fakeSource implements sources.Source + duckdbmeta.CompatibleSource so
// Config.Initialize can run without a real Quack server.
type fakeSource struct{}

func (f *fakeSource) SourceType() string             { return duckdbquack.SourceType }
func (f *fakeSource) ToConfig() sources.SourceConfig { return duckdbquack.Config{} }
func (f *fakeSource) DuckDBQuackDB() *sql.DB         { return nil }
func (f *fakeSource) RunSQL(_ context.Context, _ string, _ []any, _ duckdbquack.QueryOptions) (*duckdbquack.QueryResult, error) {
	return &duckdbquack.QueryResult{}, nil
}
func (f *fakeSource) QuackQuery(_ context.Context, _ string, _ duckdbquack.QueryOptions) (*duckdbquack.QueryResult, error) {
	return &duckdbquack.QueryResult{}, nil
}
func (f *fakeSource) EffectivePolicy() duckdbquack.Policy { return duckdbquack.Policy{} }

func TestInitialize_RefusesWithoutExplicitEnable(t *testing.T) {
	srcs := map[string]sources.Source{"s": &fakeSource{}}

	cases := []struct {
		desc string
		cfg  Config
	}{
		{
			desc: "enabled field missing",
			cfg:  Config{Name: "t", Type: "duckdb-execute-sql", Source: "s", Description: "d"},
		},
		{
			desc: "enabled explicitly false",
			cfg:  Config{Name: "t", Type: "duckdb-execute-sql", Source: "s", Description: "d", Enabled: ptr(false)},
		},
	}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			_, err := tc.cfg.Initialize(srcs)
			if err == nil {
				t.Fatalf("expected refusal, got nil")
			}
			if !strings.Contains(err.Error(), "enabled: true") {
				t.Fatalf("error %q should mention enabled: true", err.Error())
			}
		})
	}
}

func TestInitialize_AcceptsWhenEnabled(t *testing.T) {
	srcs := map[string]sources.Source{"s": &fakeSource{}}
	cfg := Config{
		Name:        "dev_sql",
		Type:        "duckdb-execute-sql",
		Source:      "s",
		Description: "Dev SQL.",
		Enabled:     ptr(true),
	}
	tool, err := cfg.Initialize(srcs)
	if err != nil {
		t.Fatalf("expected accept, got: %v", err)
	}
	if tool == nil {
		t.Fatalf("expected non-nil Tool")
	}
	// The synthetic sql parameter should be in the manifest.
	if got := tool.Manifest(); len(got.Parameters) != 1 {
		t.Fatalf("expected one parameter on manifest, got %+v", got.Parameters)
	}
}

func TestInitialize_RejectsUnknownSource(t *testing.T) {
	cfg := Config{
		Name:        "t",
		Type:        "duckdb-execute-sql",
		Source:      "missing",
		Description: "d",
		Enabled:     ptr(true),
	}
	_, err := cfg.Initialize(map[string]sources.Source{})
	if err == nil || !strings.Contains(err.Error(), "unknown source") {
		t.Fatalf("expected unknown-source error, got: %v", err)
	}
}

func ptr[T any](v T) *T { return &v }
