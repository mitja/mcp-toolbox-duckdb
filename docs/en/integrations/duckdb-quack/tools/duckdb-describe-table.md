---
title: "duckdb-describe-table"
type: docs
weight: 5
description: >
  Return column metadata (name, data type, nullability) for a fixed
  schema+table on the remote DuckDB. Both the schema and the table are
  baked into tools.yaml; the agent supplies no parameters.
---

## About

A `duckdb-describe-table` tool gives an agent a quick column-level
view of a specific table — sufficient to plan a follow-up SQL query
without having to guess column names or types. The query is pushed
to the remote via `quack_query()` against `information_schema.columns`,
so it sees the live remote schema rather than the client's
ATTACH-enumerated subset.

Each table the developer wants describable needs its own tool entry.

## Compatible Sources

{{< compatible-sources >}}

## Example

```yaml
kind: tool
name: describe_sales
type: duckdb-describe-table
source: sales-quack
description: Return the column metadata for remote.main.sales.
schema: main
table: sales
```

## Reference

| **field**     | **type** | **required** | **description**                                                                          |
|---------------|:--------:|:------------:|------------------------------------------------------------------------------------------|
| `type`        | string   | true         | Must be `"duckdb-describe-table"`.                                                       |
| `source`      | string   | true         | Name of the [`duckdb-quack`](../../source.md) source backing this tool.                  |
| `description` | string   | true         | Description passed to the LLM.                                                           |
| `schema`      | string   | true         | Schema containing the table. Validated as an ASCII identifier at config load.            |
| `table`       | string   | true         | Table name. Validated as an ASCII identifier at config load.                             |
| `authRequired`| []string | false        | Names of auth services the caller must have verified to invoke the tool.                 |
| `annotations` | object   | false        | MCP tool annotations. Defaults to read-only.                                             |
