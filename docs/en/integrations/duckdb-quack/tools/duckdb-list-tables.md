---
title: "duckdb-list-tables"
type: docs
weight: 4
description: >
  List tables (and/or views) in a fixed schema on the remote DuckDB.
  The schema is baked into tools.yaml; the agent supplies no
  parameters.
---

## About

A `duckdb-list-tables` tool answers "what's in this schema?" by
pushing a filter against `information_schema.tables` directly to the
remote DuckDB via `quack_query()`. Each schema the developer wants
exposed needs its own tool entry — that's the curated-tools
philosophy.

The `include_tables` and `include_views` flags (both default `true`)
control whether base tables, views, or both are returned. At least
one must remain true; the tool refuses to start otherwise.

## Compatible Sources

{{< compatible-sources >}}

## Example

```yaml
kind: tool
name: list_sales_tables
type: duckdb-list-tables
source: sales-quack
description: List the base tables in the remote `main` schema.
schema: main
include_tables: true
include_views: false
```

## Reference

| **field**         |   **type**  | **required** | **default** | **description**                                                                          |
|-------------------|:-----------:|:------------:|-------------|------------------------------------------------------------------------------------------|
| `type`            | string      | true         | —           | Must be `"duckdb-list-tables"`.                                                          |
| `source`          | string      | true         | —           | Name of the [`duckdb-quack`](../../source.md) source backing this tool.                  |
| `description`    | string      | true         | —           | Description passed to the LLM.                                                           |
| `schema`         | string      | true         | —           | Schema to list. Validated as an ASCII identifier at config load.                         |
| `include_tables` | boolean     | false        | `true`      | Include `BASE TABLE` rows.                                                               |
| `include_views`  | boolean     | false        | `true`      | Include `VIEW` rows.                                                                     |
| `authRequired`   | []string    | false        |             | Names of auth services the caller must have verified to invoke the tool.                 |
| `annotations`    | object      | false        |             | MCP tool annotations. Defaults to read-only.                                             |
