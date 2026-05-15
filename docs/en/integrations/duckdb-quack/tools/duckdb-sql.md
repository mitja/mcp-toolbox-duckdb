---
title: "duckdb-sql"
type: docs
weight: 1
description: >
  Execute a single curated read-only SQL statement against a duckdb-quack
  source. Agents supply bound parameter values; the statement itself is
  fixed by the developer in tools.yaml.
---

## About

A `duckdb-sql` tool runs one developer-supplied SQL statement against a
[`duckdb-quack`](../../source.md) source. The statement is fixed in
`tools.yaml`; the agent supplies only bound parameter values. This shape
is deliberately narrower than "let an LLM run arbitrary DuckDB SQL" — the
production posture is curated tools with prepared statements and
narrowed capabilities.

DuckDB supports both `?` (positional) and `$1` (named) parameter
placeholders. Both work transparently against tables in attached Quack
catalogs, so a statement like the example below binds `customer_pattern`
correctly even though `remote.sales` lives on a different DuckDB process.

The statement is validated at tool config-load time:

- Multi-statement bodies are rejected.
- The leading keyword must be in the configured allowlist (defaults to
  `SELECT, WITH, DESCRIBE, SHOW, EXPLAIN, PIVOT, UNPIVOT, VALUES, TABLE`).
- A forbidden-token scan rejects DDL/DML verbs (`INSERT`, `UPDATE`,
  `DELETE`, `DROP`, `ALTER`, `TRUNCATE`, `MERGE`, `COPY`, `INSTALL`,
  `LOAD`, `ATTACH`, `DETACH`, `CREATE`, `GRANT`, `REVOKE`, `CALL`,
  `PRAGMA`, `SET`), with string- and comment-aware scanning so
  `SELECT 'do not DROP this'` and `-- DROP TABLE x` pass.

These checks are defense in depth; the real boundary is the Quack
server's `quack_authorization_function`.

## Compatible Sources

{{< compatible-sources >}}

## Example

> **Note:** This tool uses parameterized queries to prevent SQL injection.
> Query parameters can be used as substitutes for values; they cannot be
> substituted for identifiers, column names, table names, or other parts
> of the query.

```yaml
kind: tool
name: revenue_by_customer
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
```

A parameterless variant is just as straightforward:

```yaml
kind: tool
name: top_products
type: duckdb-sql
source: sales-quack
description: Top products by total quantity sold.
statement: |
  SELECT product,
         SUM(qty) AS total_qty,
         COUNT(DISTINCT customer) AS customer_count
  FROM remote.orders
  GROUP BY product
  ORDER BY total_qty DESC
```

## Output Format

`duckdb-sql` returns a stable, typed JSON shape that preserves column
order and surfaces truncation cleanly:

```json
{
  "columns": [
    {"name": "customer", "type": "VARCHAR"},
    {"name": "revenue",  "type": "DECIMAL(38,2)"},
    {"name": "orders",   "type": "BIGINT"}
  ],
  "rows": [
    {"customer": "Alice GmbH", "revenue": "2331.65", "orders": 3}
  ],
  "row_count": 1,
  "truncated": false,
  "source": "sales-quack",
  "statement_hash": "sha256:…"
}
```

The `type` field on each column is the DuckDB type name as reported by
`rows.ColumnTypes()`. `DECIMAL(p,s)` values are rendered as strings to
preserve precision and are padded to the column's declared scale
(`1370.50` stays `"1370.50"`, `1000.00` stays `"1000.00"`). `BLOB`
columns are rejected by default and replaced with a `<blob: N bytes>`
sentinel. `LIST` and `STRUCT` columns are rendered as nested JSON
arrays and objects. `row_count` is the count of rows actually
returned; when `policy.max_rows` is reached, extra rows are dropped
and `truncated` is set to `true`. `statement_hash` is the SHA-256 of
a whitespace-canonical form of the SQL — stable across whitespace
changes so it can be logged without exposing the SQL text.

## Reference

| **field**          |                              **type**                               | **required** | **description**                                                                                                |
|--------------------|:-------------------------------------------------------------------:|:------------:|----------------------------------------------------------------------------------------------------------------|
| `type`             |                               string                               |     true     | Must be `"duckdb-sql"`.                                                                                        |
| `source`           |                               string                               |     true     | Name of the [`duckdb-quack`](../../source.md) source backing this tool.                                        |
| `description`      |                               string                               |     true     | Description passed to the LLM.                                                                                  |
| `statement`        |                               string                               |     true     | One read-only SQL statement. Use `?` or `$N` for bound parameters; reference attached tables as `<alias>.<table>`. |
| `parameters`       |  [parameters](../../../documentation/configuration/tools/_index.md#specifying-parameters)  |    false     | List of [parameters](../../../documentation/configuration/tools/_index.md#specifying-parameters) bound into the statement at invocation. |
| `templateParameters` | [templateParameters](../../../documentation/configuration/tools/_index.md#template-parameters) | false | List of [templateParameters](../../../documentation/configuration/tools/_index.md#template-parameters) substituted into the SQL before prepared-statement execution. |
| `authRequired`     |                              []string                              |    false     | Names of auth services the caller must have verified to invoke the tool.                                       |
| `annotations`      |                       ToolAnnotations                              |    false     | MCP tool annotations. Defaults to read-only.                                                                   |
