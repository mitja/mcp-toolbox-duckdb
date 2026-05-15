---
title: "duckdb-list-schemas"
type: docs
weight: 3
description: >
  Return the schemas in the remote DuckDB's current database. The query
  is pushed to the remote via `quack_query()`, so it sees every schema
  the server exposes — not just the ones the client's ATTACH has
  enumerated.
---

## About

A `duckdb-list-schemas` tool answers "which schemas does the remote
DuckDB have?". Unlike `duckdb-list-catalogs`, this tool runs on the
remote server because the client-side `information_schema` only sees
catalogs (not schemas) populated through ATTACH.

## Compatible Sources

{{< compatible-sources >}}

## Example

```yaml
kind: tool
name: list_remote_schemas
type: duckdb-list-schemas
source: sales-quack
description: Return the schemas in the remote analytics DuckDB.
```

## Reference

| **field**      | **type** | **required** | **description**                                                                          |
|----------------|:--------:|:------------:|------------------------------------------------------------------------------------------|
| `type`         | string   | true         | Must be `"duckdb-list-schemas"`.                                                         |
| `source`       | string   | true         | Name of the [`duckdb-quack`](../../source.md) source backing this tool.                  |
| `description`  | string   | true         | Description passed to the LLM.                                                           |
| `authRequired` | []string | false        | Names of auth services the caller must have verified to invoke the tool.                 |
| `annotations`  | object   | false        | MCP tool annotations. Defaults to read-only.                                             |
