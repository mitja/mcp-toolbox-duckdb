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

package duckdbquack_test

import (
	"context"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/googleapis/mcp-toolbox/internal/server"
	"github.com/googleapis/mcp-toolbox/internal/sources"
	"github.com/googleapis/mcp-toolbox/internal/sources/duckdbquack"
	"github.com/googleapis/mcp-toolbox/internal/testutils"
)

func TestParseFromYamlDuckDBQuack(t *testing.T) {
	tcs := []struct {
		desc string
		in   string
		want server.SourceConfigs
	}{
		{
			desc: "minimal example",
			in: `
            kind: source
            name: sales-quack
            type: duckdb-quack
            uri: quack:duckdb-server:9494
            token: analytics-team-token
            `,
			want: map[string]sources.SourceConfig{
				"sales-quack": duckdbquack.Config{
					Name:  "sales-quack",
					Type:  duckdbquack.SourceType,
					URI:   "quack:duckdb-server:9494",
					Token: "analytics-team-token",
				},
			},
		},
		{
			desc: "with overrides",
			in: `
            kind: source
            name: finance-quack
            type: duckdb-quack
            uri: quack:finance-duckdb:9494
            token: finance-team-token
            disable_ssl: true
            client_database: ":memory:"
            attach_alias: finance
            attach_on_startup: false
            quack:
              install_from: core
              use_secret: false
            `,
			want: map[string]sources.SourceConfig{
				"finance-quack": duckdbquack.Config{
					Name:            "finance-quack",
					Type:            duckdbquack.SourceType,
					URI:             "quack:finance-duckdb:9494",
					Token:           "finance-team-token",
					DisableSSL:      true,
					ClientDatabase:  ":memory:",
					AttachAlias:     "finance",
					AttachOnStartup: ptr(false),
					Quack: duckdbquack.QuackOptions{
						InstallFrom: "core",
						UseSecret:   ptr(false),
					},
				},
			},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.desc, func(t *testing.T) {
			got, _, _, _, _, _, err := server.UnmarshalResourceConfig(context.Background(), testutils.FormatYaml(tc.in))
			if err != nil {
				t.Fatalf("unable to unmarshal: %s", err)
			}
			if !cmp.Equal(tc.want, got) {
				t.Fatalf("incorrect parse:\nwant: %#v\ngot:  %#v", tc.want, got)
			}
		})
	}
}

func TestFailParseFromYaml(t *testing.T) {
	tcs := []struct {
		desc      string
		in        string
		errSubstr string
	}{
		{
			desc: "missing uri",
			in: `
            kind: source
            name: q
            type: duckdb-quack
            token: analytics-team-token
            `,
			errSubstr: "Field validation for 'URI' failed on the 'required' tag",
		},
		{
			desc: "missing token",
			in: `
            kind: source
            name: q
            type: duckdb-quack
            uri: quack:x:9494
            `,
			errSubstr: "Field validation for 'Token' failed on the 'required' tag",
		},
		{
			desc: "unknown field",
			in: `
            kind: source
            name: q
            type: duckdb-quack
            uri: quack:x:9494
            token: analytics-team-token
            foo: bar
            `,
			errSubstr: `unknown field "foo"`,
		},
	}
	for _, tc := range tcs {
		t.Run(tc.desc, func(t *testing.T) {
			_, _, _, _, _, _, err := server.UnmarshalResourceConfig(context.Background(), testutils.FormatYaml(tc.in))
			if err == nil {
				t.Fatalf("expected parsing to fail")
			}
			if !strings.Contains(err.Error(), tc.errSubstr) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.errSubstr)
			}
		})
	}
}

func ptr[T any](v T) *T { return &v }
