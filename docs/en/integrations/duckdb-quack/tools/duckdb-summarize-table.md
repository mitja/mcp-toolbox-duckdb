---
title: "duckdb-summarize-table"
type: docs
weight: 6
description: >
  Run DuckDB's SUMMARIZE statement against a fixed schema+table on the
  remote DuckDB. Returns per-column statistics — min, max, avg, std,
  quartiles, distinct-count, null percentage — useful for agents that
  want a numeric profile of a table before writing SQL against it.
---

## About

A `duckdb-summarize-table` tool exposes DuckDB's built-in `SUMMARIZE`
to an agent for one specific table. The statement is pushed to the
remote DuckDB via `quack_query()`, so the statistics are computed
server-side and only the summary rows cross the wire.

Each table the developer wants summarizable needs its own tool entry.

## Compatible Sources

{{< compatible-sources >}}

## Example

```yaml
kind: tool
name: summarize_sales
type: duckdb-summarize-table
source: sales-quack
description: Per-column statistics for remote.main.sales.
schema: main
table: sales
```

## Output Format

`SUMMARIZE` returns one row per source column, with these output
columns (consistent across DuckDB versions, with minor evolution):

`column_name`, `column_type`, `min`, `max`, `approx_unique`, `avg`,
`std`, `q25`, `q50`, `q75`, `count`, `null_percentage`.

## Reference

| **field**     | **type** | **required** | **description**                                                                          |
|---------------|:--------:|:------------:|------------------------------------------------------------------------------------------|
| `type`        | string   | true         | Must be `"duckdb-summarize-table"`.                                                      |
| `source`      | string   | true         | Name of the [`duckdb-quack`](../../source.md) source backing this tool.                  |
| `description` | string   | true         | Description passed to the LLM.                                                           |
| `schema`      | string   | true         | Schema containing the table. Validated as an ASCII identifier at config load.            |
| `table`       | string   | true         | Table name. Validated as an ASCII identifier at config load.                             |
| `authRequired`| []string | false        | Names of auth services the caller must have verified to invoke the tool.                 |
| `annotations` | object   | false        | MCP tool annotations. Defaults to read-only.                                             |
