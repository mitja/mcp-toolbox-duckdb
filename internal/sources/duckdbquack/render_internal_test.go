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

import "testing"

func TestDecimalScaleFromType(t *testing.T) {
	cases := []struct {
		in     string
		want   int64
		wantOk bool
	}{
		{"DECIMAL(18,2)", 2, true},
		{"DECIMAL(38,4)", 4, true},
		{"DECIMAL(5,0)", 0, true},
		{"DECIMAL( 18 , 2 )", 2, true},
		{"DECIMAL(18,12)", 12, true},
		{"VARCHAR", 0, false},
		{"BIGINT", 0, false},
		{"DECIMAL", 0, false},
		{"DECIMAL(18)", 0, false},
		{"", 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, ok := decimalScaleFromType(tc.in)
			if ok != tc.wantOk || got != tc.want {
				t.Fatalf("decimalScaleFromType(%q) = (%d, %v); want (%d, %v)", tc.in, got, ok, tc.want, tc.wantOk)
			}
		})
	}
}

func TestFormatDecimal(t *testing.T) {
	cases := []struct {
		desc  string
		in    string
		scale int64
		want  string
	}{
		// scale<=0: pass-through.
		{"scale zero", "123", 0, "123"},
		{"negative scale", "123", -1, "123"},
		{"empty input", "", 4, ""},

		// Pad an integer to scale.
		{"integer scale 2", "1370", 2, "1370.00"},
		{"zero scale 2", "0", 2, "0.00"},
		{"negative integer scale 2", "-7", 2, "-7.00"},

		// Pad a decimal already shorter than scale.
		{"trimmed trailing zero", "1370.5", 2, "1370.50"},
		{"trimmed two zeros", "1370.5", 4, "1370.5000"},
		{"already at scale", "1370.50", 2, "1370.50"},
		{"negative trimmed", "-1370.5", 2, "-1370.50"},
		{"small number", "0.1", 4, "0.1000"},

		// Defensive: input already has more digits than scale -> leave alone.
		// (duckdb-go's Decimal.String() never produces more digits than the
		// declared scale, but be liberal in what we accept.)
		{"more digits than scale", "1.234", 2, "1.234"},
	}

	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			got := formatDecimal(tc.in, tc.scale)
			if got != tc.want {
				t.Fatalf("formatDecimal(%q, %d) = %q; want %q", tc.in, tc.scale, got, tc.want)
			}
		})
	}
}
