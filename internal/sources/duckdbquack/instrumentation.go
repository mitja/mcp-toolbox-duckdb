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
	"errors"
	"fmt"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// instrumentationName is the OTel Tracer/Meter scope for the source. It
// shows up in span / metric labels so operators can filter on
// `otel.scope.name`.
const instrumentationName = "github.com/googleapis/mcp-toolbox/internal/sources/duckdbquack"

// tracer returns the current global TracerProvider's tracer for this
// instrumentation scope. We resolve it per-call rather than caching at
// package init so that tests which swap in a tracetest.SpanRecorder via
// otel.SetTracerProvider see the new provider on the very next RunSQL.
// The overhead is one global-provider lookup per query — negligible
// compared to the actual SQL roundtrip.
func tracer() trace.Tracer { return otel.Tracer(instrumentationName) }

// Metric instruments are initialized lazily on first use rather than at
// package init. The OTel global MeterProvider returned by otel.Meter()
// before Toolbox has called otel.SetMeterProvider is a no-op; instruments
// created from that no-op meter do not start emitting later when the real
// provider is swapped in. Deferring creation until the first RunSQL
// invocation lets the package see whatever provider is installed by then
// (in practice: the OTLP / GCP meter Toolbox configured in main()).
//
// The downside: SetMeterProvider swaps that happen *after* the first
// RunSQL are not picked up (the instruments are already cached). For
// production use this is fine — SetMeterProvider is called once at
// startup and never again. Tests that assert on metric emissions must
// install their MeterProvider before invoking RunSQL the first time.
var (
	metricsOnce sync.Once

	queryDuration  metric.Float64Histogram
	queryRows      metric.Int64Histogram
	queryErrors    metric.Int64Counter
	queryTruncated metric.Int64Counter
	queryReattach  metric.Int64Counter
)

func ensureMetrics() {
	metricsOnce.Do(func() {
		m := otel.Meter(instrumentationName)
		var err error
		queryDuration, err = m.Float64Histogram(
			"duckdb.query.duration",
			metric.WithDescription("Latency of a single SQL invocation through duckdb-quack Source.RunSQL."),
			metric.WithUnit("s"),
		)
		if err != nil {
			panic(fmt.Sprintf("duckdb-quack metric setup (duration): %v", err))
		}
		queryRows, err = m.Int64Histogram(
			"duckdb.query.rows_returned",
			metric.WithDescription("Number of rows returned by a single SQL invocation, after MaxRows truncation."),
			metric.WithUnit("{row}"),
		)
		if err != nil {
			panic(fmt.Sprintf("duckdb-quack metric setup (rows): %v", err))
		}
		queryErrors, err = m.Int64Counter(
			"duckdb.query.errors_total",
			metric.WithDescription("Count of SQL invocations that returned an error. Labeled by error.type."),
			metric.WithUnit("{call}"),
		)
		if err != nil {
			panic(fmt.Sprintf("duckdb-quack metric setup (errors): %v", err))
		}
		queryTruncated, err = m.Int64Counter(
			"duckdb.query.truncated_total",
			metric.WithDescription("Count of SQL invocations whose result was truncated by MaxRows."),
			metric.WithUnit("{call}"),
		)
		if err != nil {
			panic(fmt.Sprintf("duckdb-quack metric setup (truncated): %v", err))
		}
		queryReattach, err = m.Int64Counter(
			"duckdb.connection.reattach_total",
			metric.WithDescription("Count of times the source re-attached the remote catalog after a conn drop."),
			metric.WithUnit("{event}"),
		)
		if err != nil {
			panic(fmt.Sprintf("duckdb-quack metric setup (reattach): %v", err))
		}
	})
}

// errorType maps an error to a short, stable label suitable for use as the
// `error.type` span attribute and metric dimension. Generic Go errors map
// to "error"; the duckdb-quack-specific conditions get their own labels so
// operators can graph "how often did we hit a reattach?" or "how often did
// a query time out?" without parsing free-form error messages.
func errorType(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "deadline_exceeded"
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	return "error"
}

// recordOutcome adds the per-result span attributes and emits the duration/
// rows/errors/truncated metrics for a single RunSQL invocation. baseAttrs
// is the slice of attribute.KeyValue used both as span attributes (set on
// the span at startup) and as metric dimensions; we pass it pre-built to
// avoid two parallel slices in the caller.
func recordOutcome(
	ctx context.Context,
	span trace.Span,
	baseAttrs []attribute.KeyValue,
	res *QueryResult,
	err error,
	duration time.Duration,
) {
	ensureMetrics()
	queryDuration.Record(ctx, duration.Seconds(), metric.WithAttributes(baseAttrs...))

	if res != nil {
		span.SetAttributes(
			attribute.Int("db.response.rows", len(res.Rows)),
			attribute.Bool("db.response.truncated", res.Truncated),
		)
		queryRows.Record(ctx, int64(len(res.Rows)), metric.WithAttributes(baseAttrs...))
		if res.Truncated {
			queryTruncated.Add(ctx, 1, metric.WithAttributes(baseAttrs...))
		}
	}

	if err != nil {
		etype := errorType(err)
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		span.SetAttributes(attribute.String("error.type", etype))
		queryErrors.Add(ctx, 1, metric.WithAttributes(
			append([]attribute.KeyValue{attribute.String("error.type", etype)}, baseAttrs...)...,
		))
		return
	}
	span.SetStatus(codes.Ok, "")
}
