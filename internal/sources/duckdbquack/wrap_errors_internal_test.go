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
	"errors"
	"strings"
	"testing"
)

// TestWrapKnownErrors_StreamingScan pins the wrapping behavior for DuckDB's
// "Multiple streaming scans …" error. The cryptic engine message gets a
// pointer at the push_down_to_remote flag (single-source case) and the
// manual quack_query() wrapper (multi-source case), while the original
// error is preserved for errors.Is / errors.Unwrap.
func TestWrapKnownErrors_StreamingScan(t *testing.T) {
	orig := errors.New("Not implemented Error: Multiple streaming scans or streaming scans + CTAS / insert in the same query are not currently supported")
	wrapped := wrapKnownErrors(orig)
	if wrapped == nil {
		t.Fatalf("expected wrapped error, got nil")
	}
	if !errors.Is(wrapped, orig) {
		t.Errorf("errors.Is(wrapped, orig) is false; %%w should preserve the original")
	}
	msg := wrapped.Error()
	for _, want := range []string{
		"push_down_to_remote",
		"quack_query(",
		"Original error:",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("wrapped message missing %q\n--- got ---\n%s", want, msg)
		}
	}
}

func TestWrapKnownErrors_PassthroughOnNil(t *testing.T) {
	if got := wrapKnownErrors(nil); got != nil {
		t.Errorf("wrapKnownErrors(nil) = %v; want nil", got)
	}
}

func TestWrapKnownErrors_PassthroughOnUnrelated(t *testing.T) {
	orig := errors.New("some other DuckDB error: division by zero")
	got := wrapKnownErrors(orig)
	if got == nil {
		t.Fatalf("expected error passthrough, got nil")
	}
	if got.Error() != orig.Error() {
		t.Errorf("unrelated error was rewritten; got=%q want=%q", got.Error(), orig.Error())
	}
}
