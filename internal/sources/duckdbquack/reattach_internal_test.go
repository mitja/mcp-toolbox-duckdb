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
	"database/sql/driver"
	"errors"
	"testing"
)

// TestNeedsReAttach asserts the error-pattern matcher catches the cases we
// have observed end-to-end (DuckDB catalog-missing message, duckdb-go's
// "Invalid connection id" after a Quack server restart, driver.ErrBadConn,
// and known Quack network failure signatures) without false-positiving on
// unrelated SQL errors.
//
// The end-to-end recovery is exercised by TestReAttach_RecoversFromMissingCatalog
// in tests/duckdbquack/. This unit test pins the substring contract so a
// future driver / DuckDB version that renames an error message gets caught.
func TestNeedsReAttach(t *testing.T) {
	const alias = "remote"

	cases := []struct {
		desc string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"driver.ErrBadConn", driver.ErrBadConn, true},
		{
			"catalog missing names the alias",
			errors.New(`Catalog Error: Catalog with name "remote" does not exist!`),
			true,
		},
		{
			"connection id invalidated (duckdb-go post-Quack-restart)",
			errors.New("Invalid Input Error: Invalid connection id"),
			true,
		},
		{
			"quack failed to send",
			errors.New("IO Error: Failed to send message"),
			true,
		},
		{
			"quack failed to receive",
			errors.New("IO Error: Failed to receive message"),
			true,
		},
		{
			"could not connect",
			errors.New("IO Error: Could not connect"),
			true,
		},
		{
			"connection reset",
			errors.New("read: Connection reset by peer"),
			true,
		},
		// Negative cases — must NOT trigger the retry path.
		{
			"syntax error",
			errors.New("Parser Error: syntax error at or near 'WHERE'"),
			false,
		},
		{
			"type mismatch",
			errors.New("Conversion Error: cannot cast 'abc' to INTEGER"),
			false,
		},
		{
			"unrelated 'does not exist' (no alias)",
			errors.New(`Catalog Error: Table with name "nope" does not exist!`),
			false,
		},
		{
			"permission denied",
			errors.New("Invalid Input Error: Authorization failed"),
			false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			got := needsReAttach(tc.err, alias)
			if got != tc.want {
				t.Fatalf("needsReAttach(%q, %q) = %v; want %v", tc.err, alias, got, tc.want)
			}
		})
	}
}
