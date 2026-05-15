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

package duckdbsql_test

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/googleapis/mcp-toolbox/internal/server"
	"github.com/googleapis/mcp-toolbox/internal/testutils"
	"github.com/googleapis/mcp-toolbox/internal/tools/duckdb/duckdbsql"
	"github.com/googleapis/mcp-toolbox/internal/util/parameters"
)

func TestParseFromYamlDuckDBSQL(t *testing.T) {
	ctx, err := testutils.ContextWithNewLogger()
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	tcs := []struct {
		desc string
		in   string
		want server.ToolConfigs
	}{
		{
			desc: "basic example",
			in: `
            kind: tool
            name: revenue_by_customer
            type: duckdb-sql
            source: sales-quack
            description: Revenue summary by customer.
            statement: |
                SELECT customer, SUM(amount) AS revenue
                FROM remote.sales
                WHERE customer ILIKE '%' || ? || '%'
                GROUP BY customer
                ORDER BY revenue DESC
            parameters:
                - name: customer_pattern
                  type: string
                  description: Case-insensitive customer name pattern.
            `,
			want: server.ToolConfigs{
				"revenue_by_customer": duckdbsql.Config{
					Name:         "revenue_by_customer",
					Type:         "duckdb-sql",
					Source:       "sales-quack",
					Description:  "Revenue summary by customer.",
					AuthRequired: []string{},
					Statement: "SELECT customer, SUM(amount) AS revenue\n" +
						"FROM remote.sales\n" +
						"WHERE customer ILIKE '%' || ? || '%'\n" +
						"GROUP BY customer\n" +
						"ORDER BY revenue DESC\n",
					Parameters: []parameters.Parameter{
						parameters.NewStringParameter("customer_pattern", "Case-insensitive customer name pattern."),
					},
				},
			},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.desc, func(t *testing.T) {
			_, _, _, got, _, _, err := server.UnmarshalResourceConfig(ctx, testutils.FormatYaml(tc.in))
			if err != nil {
				t.Fatalf("unable to unmarshal: %s", err)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Fatalf("incorrect parse: diff %v", diff)
			}
		})
	}
}
