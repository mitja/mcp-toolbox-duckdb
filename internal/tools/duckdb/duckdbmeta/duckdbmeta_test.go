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

package duckdbmeta

import (
	"strings"
	"testing"
)

func TestStatementHash(t *testing.T) {
	h1 := StatementHash("SELECT 1")
	h2 := StatementHash("  SELECT\n  1  ")
	if h1 != h2 {
		t.Fatalf("expected canonical hash to be whitespace-insensitive, got %s vs %s", h1, h2)
	}
	if !strings.HasPrefix(h1, "sha256:") {
		t.Fatalf("expected sha256: prefix, got %s", h1)
	}
	if len(h1) != len("sha256:")+64 {
		t.Fatalf("expected 64 hex chars after prefix, got %s", h1)
	}
	if StatementHash("SELECT 2") == h1 {
		t.Fatalf("different statements should hash differently")
	}
}

func TestValidateIdentifier(t *testing.T) {
	good := []string{"a", "abc", "Abc", "_abc", "table_1", "T1", "X_2_Y"}
	for _, s := range good {
		if err := ValidateIdentifier(s); err != nil {
			t.Errorf("ValidateIdentifier(%q) unexpected error: %v", s, err)
		}
	}
	bad := []string{"", " ", "1abc", "ab-c", "ab.c", "ab c", `ab"c`, "ab'c", "ab;drop"}
	for _, s := range bad {
		if err := ValidateIdentifier(s); err == nil {
			t.Errorf("ValidateIdentifier(%q) expected error, got nil", s)
		}
	}
}

func TestQuoteIdentifier(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"sales", `"sales"`},
		{"Order", `"Order"`},
		// Defensive: callers should validate first, but the function
		// still doubles any embedded quotes correctly.
		{`weird"name`, `"weird""name"`},
	}
	for _, tc := range cases {
		if got := QuoteIdentifier(tc.in); got != tc.want {
			t.Errorf("QuoteIdentifier(%q) = %q; want %q", tc.in, got, tc.want)
		}
	}
}
