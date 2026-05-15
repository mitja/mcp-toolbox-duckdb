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
	"testing"
	"time"

	"github.com/googleapis/mcp-toolbox/internal/sources/duckdbquack"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// withSpanRecorder swaps in an in-memory TracerProvider so each test can
// inspect the spans the source emitted. OTel's global package uses a
// delegating tracer so the swap takes effect for handles already obtained
// via otel.Tracer().
func withSpanRecorder(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	prev := otel.GetTracerProvider()
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(prev) })
	return sr
}

// findSpan returns the first span whose Name matches `name`, or fails the
// test. The recorder accumulates *all* spans the test process emits — across
// subtests, parallel goroutines, and so on — so filter explicitly.
func findSpan(t *testing.T, spans []sdktrace.ReadOnlySpan, name string) sdktrace.ReadOnlySpan {
	t.Helper()
	for _, s := range spans {
		if s.Name() == name {
			return s
		}
	}
	t.Fatalf("no span named %q among %d recorded spans", name, len(spans))
	return nil
}

// attrOf returns the (typed) value of an attribute by key, failing the test
// if absent.
func attrOf(t *testing.T, span sdktrace.ReadOnlySpan, key string) attribute.Value {
	t.Helper()
	for _, kv := range span.Attributes() {
		if string(kv.Key) == key {
			return kv.Value
		}
	}
	t.Fatalf("span %q missing attribute %q", span.Name(), key)
	return attribute.Value{}
}

func TestOTel_RunSQL_EmitsSpanWithSpecAttributes(t *testing.T) {
	sr := withSpanRecorder(t)
	srv := startInProcessQuackServer(t)
	src := newSource(t, srv, duckdbquack.Policy{})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	res, err := src.RunSQL(ctx,
		"SELECT id, customer FROM remote.sales WHERE id IN (?, ?) ORDER BY id",
		[]any{int64(1), int64(2)}, duckdbquack.QueryOptions{})
	if err != nil {
		t.Fatalf("RunSQL: %v", err)
	}
	if len(res.Rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(res.Rows))
	}

	span := findSpan(t, sr.Ended(), "duckdb.query")
	if got := attrOf(t, span, "db.system").AsString(); got != "duckdb" {
		t.Errorf("db.system = %q; want duckdb", got)
	}
	if got := attrOf(t, span, "toolbox.source.name").AsString(); got != "test-source" {
		t.Errorf("toolbox.source.name = %q; want test-source", got)
	}
	if got := attrOf(t, span, "db.statement.parameter_count").AsInt64(); got != 2 {
		t.Errorf("db.statement.parameter_count = %d; want 2", got)
	}
	if got := attrOf(t, span, "db.response.rows").AsInt64(); got != 2 {
		t.Errorf("db.response.rows = %d; want 2", got)
	}
	if got := attrOf(t, span, "db.response.truncated").AsBool(); got != false {
		t.Errorf("db.response.truncated = %v; want false", got)
	}
	if span.Status().Code != codes.Ok {
		t.Errorf("span status = %v; want Ok", span.Status().Code)
	}
}

func TestOTel_RunSQL_RecordsErrorOnFailure(t *testing.T) {
	sr := withSpanRecorder(t)
	srv := startInProcessQuackServer(t)
	src := newSource(t, srv, duckdbquack.Policy{})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// Syntax error: not a reattach-worthy failure, so the span carries
	// the error verbatim without a reattach event.
	if _, err := src.RunSQL(ctx, "SELECT 1 FROM", nil, duckdbquack.QueryOptions{}); err == nil {
		t.Fatalf("expected syntax error")
	}

	span := findSpan(t, sr.Ended(), "duckdb.query")
	if got := span.Status().Code; got != codes.Error {
		t.Errorf("span status = %v; want Error", got)
	}
	if got := attrOf(t, span, "error.type").AsString(); got != "error" {
		t.Errorf("error.type = %q; want \"error\"", got)
	}
}

func TestOTel_RunSQL_EmitsReattachEventOnRecovery(t *testing.T) {
	sr := withSpanRecorder(t)
	srv := startInProcessQuackServer(t)
	src := newSource(t, srv, duckdbquack.Policy{})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Force the reconnect path the same way TestReAttach_* does.
	if _, err := src.DuckDBQuackDB().ExecContext(ctx, "DETACH remote"); err != nil {
		t.Fatalf("manual DETACH: %v", err)
	}
	if _, err := src.RunSQL(ctx, "SELECT id FROM remote.sales LIMIT 1", nil, duckdbquack.QueryOptions{}); err != nil {
		t.Fatalf("RunSQL after DETACH should auto-reattach: %v", err)
	}

	// The recorder accumulates spans from every test that runs before this
	// one too (when run in the package's normal sequence). Find the one
	// that carries a reattach event.
	var found bool
	for _, sp := range sr.Ended() {
		if sp.Name() != "duckdb.query" {
			continue
		}
		for _, ev := range sp.Events() {
			if ev.Name == "reattach" {
				found = true
				break
			}
		}
		if found {
			break
		}
	}
	if !found {
		t.Fatalf("expected at least one duckdb.query span to carry a 'reattach' event after a DETACH/RunSQL cycle")
	}
	_ = sr // silence unused-variable warning; helpers already use it
}
