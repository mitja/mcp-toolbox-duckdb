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

package duckdblistcatalogs_test

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/googleapis/mcp-toolbox/internal/server"
	"github.com/googleapis/mcp-toolbox/internal/testutils"
	"github.com/googleapis/mcp-toolbox/internal/tools/duckdb/duckdblistcatalogs"
)

func TestParseFromYaml(t *testing.T) {
	ctx, err := testutils.ContextWithNewLogger()
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	in := `
        kind: tool
        name: list_catalogs
        type: duckdb-list-catalogs
        source: sales-quack
        description: Return the catalogs visible to the client DuckDB.
        `
	want := server.ToolConfigs{
		"list_catalogs": duckdblistcatalogs.Config{
			Name:         "list_catalogs",
			Type:         "duckdb-list-catalogs",
			Source:       "sales-quack",
			Description:  "Return the catalogs visible to the client DuckDB.",
			AuthRequired: []string{},
		},
	}
	_, _, _, got, _, _, err := server.UnmarshalResourceConfig(ctx, testutils.FormatYaml(in))
	if err != nil {
		t.Fatalf("unable to unmarshal: %s", err)
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("incorrect parse: diff %v", diff)
	}
}
