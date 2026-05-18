---
title: "DuckDB Quack Source"
linkTitle: "Source"
type: docs
weight: 1
description: >
  Connect MCP Toolbox to a remote DuckDB instance over the Quack remote
  protocol. The DuckDB analytical runtime can be restarted, redeployed, and
  scaled independently of the Toolbox control plane.
no_list: true
---

## About

[DuckDB][duckdb-docs] is an embeddable analytical database. The
[Quack][quack-docs] extension turns a DuckDB instance into an HTTP-accessible
server that other DuckDB clients can connect to. The `duckdb-quack` source
runs an in-process DuckDB client inside Toolbox, loads the Quack extension,
and `ATTACH`es a remote DuckDB catalog under a user-defined alias. Tools then
issue read-only SQL that references attached-catalog tables (e.g.
`SELECT … FROM remote.sales`) and bound parameters work transparently across
the boundary.

Why a remote DuckDB rather than embedded? Lifecycle separation. The Toolbox
process can be restarted, redeployed, and scaled without disturbing the
analytical runtime, and one Toolbox can serve agents that target several
DuckDB backends.

**Status:** Quack is currently in beta and ships in DuckDB v1.5.2+ via the
`core_nightly` extension repository. Protocol details and default parameter
values may still change.

[duckdb-docs]: https://duckdb.org/docs/
[quack-docs]: https://duckdb.org/docs/current/quack/overview

## Available Tools

{{< list-tools >}}

## Requirements

### A running Quack server

You need a DuckDB process serving its data over Quack. The minimum
bootstrap, after `INSTALL quack FROM core_nightly; LOAD quack;`, is:

```sql
CALL quack_serve(
  'quack:0.0.0.0:9494',
  token := 'analytics-team-token',
  allow_other_hostname := true,
  disable_ssl := true                  -- omit for TLS deployments
);
```

A read-only authorization callback is the real security boundary. Install
it **after** clients have attached, because the macro is also evaluated for
the internal catalog probes that `ATTACH` issues:

```sql
CREATE OR REPLACE MACRO read_only(sid, query) AS (
  regexp_matches(upper(trim(query)),
                 '^(SELECT|WITH|EXPLAIN|DESCRIBE|SHOW)\b')
);
SET GLOBAL quack_authorization_function = 'read_only';
```

For non-local deployments, terminate TLS at a reverse proxy in front of
Quack and do not expose the Quack listener directly to the public internet.

### Token format

The token is interpolated into a `CREATE SECRET` statement at source
initialization. The source enforces `^[A-Za-z0-9_-]{8,}$` on the value to
prevent quote-injection regardless of where the token came from (env var,
secret manager, file).

## Example

```yaml
sources:
  sales-quack:
    type: duckdb-quack
    uri: quack:duckdb-server:9494
    token: ${QUACK_TOKEN}
    disable_ssl: true                  # plaintext on internal network
    attach_alias: remote               # tables appear as remote.<name>
    policy:
      read_only: true
      reject_multiple_statements: true
      max_rows: 1000
      timeout: 30s

tools:
  revenue_by_customer:
    type: duckdb-sql
    source: sales-quack
    description: Revenue by customer, filtered by a case-insensitive name pattern.
    parameters:
      - name: customer_pattern
        type: string
        description: Case-insensitive customer name substring (no wildcards).
    statement: |
      SELECT customer,
             SUM(amount) AS revenue,
             COUNT(*)    AS orders
      FROM remote.sales
      WHERE customer ILIKE '%' || ? || '%'
      GROUP BY customer
      ORDER BY revenue DESC

toolsets:
  analytics_readonly:
    - revenue_by_customer
```

Note: do not include explicit `kind:` or `name:` fields inside a map
entry — the map key serves as the name, and the kind is inferred from
the parent (`sources:` → `source`, `tools:` → `tool`). Adding them
duplicates the name and Toolbox refuses to start.

## Reference

### Configuration fields

| **field**             |  **type**   | **required** | **default**    | **description**                                                                                                |
|-----------------------|:-----------:|:------------:|----------------|----------------------------------------------------------------------------------------------------------------|
| `type`                | string      | true         | —              | Must be `"duckdb-quack"`.                                                                                      |
| `uri`                 | string      | true         | —              | Quack URI in the form `quack:<host>:<port>`.                                                                   |
| `token`               | string      | true         | —              | Auth token. Must match `^[A-Za-z0-9_-]{8,}$`. Typically supplied via env-var substitution (e.g. `${QUACK_TOKEN}`). |
| `disable_ssl`         | boolean     | false        | `false`        | Set to `true` for plaintext connections (internal networks). Quack defaults to plaintext for `127.0.0.1` connections regardless. |
| `client_database`     | string      | false        | `":memory:"`   | DuckDB database used by the in-process client. Persistent files work, but the only state the client holds is the `ATTACH`. |
| `attach_alias`        | string      | false        | `"remote"`     | Catalog name under which the remote DuckDB is attached. Tools reference tables as `<alias>.<table>`.           |
| `attach_on_startup`   | boolean     | false        | `true`         | Set to `false` to defer `ATTACH` to first tool invocation.                                                     |
| `additional_attachments` | list     | false        | `[]`           | Optional extra Quack servers to ATTACH into the same in-process DuckDB. Each entry needs `uri` and `attach_alias`; `token` and `disable_ssl` default to the source's primary values when omitted. Enables cross-catalog queries (see Advanced Usage). |
| `quack.install_from`  | string      | false        | `"core_nightly"`| DuckDB extension repository to install Quack from. Pin to a stable repository for reproducible deployments.   |
| `quack.use_secret`    | boolean     | false        | `true`         | When true, the token is stored in a per-source `CREATE SECRET (TYPE quack, …)` and the `ATTACH` reads it implicitly. |
| `policy.read_only`    | boolean     | false        | `false`        | Informational; the Quack server's `quack_authorization_function` is the enforced boundary.                     |
| `policy.reject_multiple_statements` | boolean | false | `true`     | Reject statements with more than one SQL command at tool config-load time.                                    |
| `policy.allowed_statement_kinds` | []string | false | (tool default) | Leading SQL keywords accepted by `duckdb-sql` tools that target this source. Defaults to `SELECT, WITH, DESCRIBE, SHOW, EXPLAIN, PIVOT, UNPIVOT, VALUES, TABLE`. |
| `policy.max_rows`     | integer     | false        | `1000`         | Maximum rows returned to the caller. Excess rows are dropped and `truncated` is set to `true` on the response. |
| `policy.timeout`      | duration    | false        | `30s`          | Per-invocation context deadline applied at the tool layer.                                                     |

### Connection properties

The in-process DuckDB client connects to the Quack server through a single
TCP connection that carries the `ATTACH` state:

- `MaxOpenConns`: 1 (the `ATTACH` lives on one connection)
- `MaxIdleConns`: 1 (kept warm so the attach is not lost between calls)

### Observability

The source emits OpenTelemetry telemetry on every `RunSQL` invocation,
so every `duckdb-*` tool inherits it without per-tool wiring. Spans
are parented to whatever request span Toolbox's MCP layer already
creates, so a trace links `MCP request -> tool execution -> SQL`.

**Span**: `duckdb.query`, scope
`github.com/googleapis/mcp-toolbox/internal/sources/duckdbquack`.

| Attribute                       | Type    | Description                                                                                  |
|---------------------------------|---------|----------------------------------------------------------------------------------------------|
| `db.system`                     | string  | Always `"duckdb"`.                                                                           |
| `toolbox.source.name`           | string  | The source's YAML name (e.g., `sales-quack`).                                                 |
| `db.statement.parameter_count`  | int     | Number of bound parameters passed to the query.                                              |
| `db.response.rows`              | int     | Rows actually returned (after `policy.max_rows` truncation).                                 |
| `db.response.truncated`         | bool    | `true` when `policy.max_rows` capped the result.                                             |
| `error.type`                    | string  | Set on failure: `deadline_exceeded`, `canceled`, or `error`.                                  |

A `reattach` span event is added when the source recovers a lost
ATTACH by re-running the per-connection bootstrap; the event carries
`trigger.error` so you can see what surfaced the recovery.

**Metrics** (scope same as above; recorded on every `RunSQL`):

| Metric                              | Kind            | Unit       | Dimensions                                              |
|-------------------------------------|-----------------|------------|---------------------------------------------------------|
| `duckdb.query.duration`             | histogram       | `s`        | source name, parameter count                            |
| `duckdb.query.rows_returned`        | histogram       | `{row}`    | source name, parameter count                            |
| `duckdb.query.errors_total`         | counter         | `{call}`   | source name, parameter count, `error.type`              |
| `duckdb.query.truncated_total`     | counter         | `{call}`   | source name, parameter count                            |
| `duckdb.connection.reattach_total` | counter         | `{event}`  | source name                                             |

Raw SQL text is never logged or attached to the span — only its
shape (parameter count) and outcome (rows / truncation / error type).
The Toolbox-side MCP layer separately records
`toolbox.tool.execution.duration` per tool invocation, so the inner
`duckdb.query.duration` plus the outer tool duration give you both
the SQL-level latency and the end-to-end tool latency.

### Security model

Defense in depth, applied at three layers:

1. **`duckdb-sql` tool, config-load validator** — multi-statement
   rejection, leading-keyword allowlist, and a forbidden-substring scan
   (DDL/DML/extension verbs). Catches developer mistakes in `tools.yaml`;
   refuses to start the Toolbox process if any tool's statement fails.
2. **Per-invocation limits** — `policy.timeout` propagates as a context
   deadline; `policy.max_rows` caps the result set and sets the
   `truncated` flag when extra rows are dropped.
3. **Quack server authorization callback** — the real security boundary.
   Even a destructive statement that somehow reaches the server (raw
   query, bypassed validator, future bug) is rejected by the
   `quack_authorization_function` macro.

Agents never construct SQL fragments; they only supply bound parameter
values. The statement itself is fixed by the developer in `tools.yaml`.

## Advanced Usage

### Multi-attach: cross-catalog queries

A duckdb-quack source can `ATTACH` more than one Quack server into the
same in-process DuckDB so a single tool can join across catalogs.
Declare the primary attachment via the top-level fields and list extras
under `additional_attachments`:

```yaml
sources:
  combined-analytics:
    type: duckdb-quack
    uri: quack:sales-server:9494
    token: ${QUACK_TOKEN}
    disable_ssl: true
    attach_alias: sales_remote
    additional_attachments:
      - uri: quack:inventory-server:9494
        attach_alias: inventory_remote
        # token and disable_ssl optional; inherit from the source above
        # when unset. Set explicitly here when the second server uses
        # a different token or TLS preference.

tools:
  product_orders_overview:
    type: duckdb-sql
    source: combined-analytics
    statement: |
      SELECT p.name, p.category, COALESCE(SUM(o.qty), 0) AS units_ordered
      FROM inventory_remote.products p
      LEFT JOIN sales_remote.orders o ON o.product = p.name
      GROUP BY p.name, p.category
      ORDER BY units_ordered DESC
```

Each extra attachment gets its own scoped DuckDB `SECRET` (`SCOPE
'quack:host:port'`) so the right token is paired with the right `ATTACH`
even when multiple Quack servers are wired in. Alias uniqueness is
enforced at config load — two attachments cannot share an `attach_alias`
within a single source.

The reconnect path covers every alias the source owns: a single error
that names any one of them (or a generic bad-conn signal from the
underlying TCP) triggers a re-bootstrap of *all* the attachments on a
fresh pool connection.

A runnable end-to-end example, including a second Quack server in the
Compose stack and the `product_orders_overview` tool, lives in the
[demo repo](https://github.com/mitja/mcp-toolbox-duckdb-demo). See the
"Cross-catalog queries and pushdown" section in its README for the
command + expected output.

### Where queries actually run (pushdown)

A duckdb-quack tool's SQL is executed by the *in-process* DuckDB; the
Quack extension implements a remote-scan operator under each ATTACHed
catalog. The split:

- **Pushes down to the remote**: projections (the column list reaching
  `remote.<table>`) and filters (`WHERE` predicates on attached columns).
  Only the resulting rows cross the TCP boundary.
- **Runs locally on the Toolbox-side DuckDB**: joins, aggregates,
  sort/limit, and any function the remote scanner does not push
  through. Local working memory therefore scales with the *operator's*
  intermediate state (e.g., the hash table size of a `GROUP BY`), not
  with the underlying remote table size.

Two consequences worth designing around:

1. **Cross-catalog joins are a local-side cost.** A `JOIN` across
   `additional_attachments` aliases forces both sides to be pulled
   back to the local DuckDB before the join operator runs. Single-source
   tools push much more work to the remote and stay cheap on the
   Toolbox side.
2. **Metadata tools (`duckdb-list-tables`, `duckdb-describe-table`,
   `duckdb-summarize-table`) bypass ATTACH entirely.** They route their
   SQL through Quack's `quack_query()` table function, which is a full
   SQL passthrough — the entire statement is shipped to the remote and
   executed there. Local materialization is essentially zero for these.

The demo repo's README has a "Cross-catalog queries and pushdown"
section with concrete do/don't examples (project narrowly, filter
early, prefer single-source aggregates, always set `LIMIT` for
top-N tools) — they apply uniformly to `duckdb-sql` tools regardless
of how many attachments a source has.


### Federating other data behind the remote Quack

Because the remote behind a `duckdb-quack` source is itself a DuckDB
instance, anything DuckDB can read can be exposed as a view in `main`
and reached through the existing `ATTACH` from the Toolbox side — no
adapter changes. Parquet, CSV, the DuckDB Postgres / MySQL / SQLite
scanners, and Iceberg/Delta extensions all work this way. The remote
does the read; the Toolbox-side in-process DuckDB just sees a normal
view via the attached alias. The demo repo's "Parquet behind a
remote Quack" section walks through a working `read_parquet` example.
