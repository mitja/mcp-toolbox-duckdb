---
title: "duckdb-list-catalogs"
type: docs
weight: 2
description: >
  Return the catalogs visible to a duckdb-quack source's in-process
  client. For a typical deployment this is the configured attach alias
  (e.g., `remote`) plus DuckDB's built-in `system` and `temp` catalogs.
---

## About

A `duckdb-list-catalogs` tool answers "which catalogs can this source
reach?". The tool runs a client-side query against
`information_schema.schemata` — no agent parameters, no remote round-trip.

## Compatible Sources

{{< compatible-sources >}}

## Example

```yaml
kind: tool
name: list_catalogs
type: duckdb-list-catalogs
source: sales-quack
description: Return the catalogs visible to the analytics client.
```

A typical response, with one source ATTACHed under the default alias:

```json
{
  "columns": [{"name": "catalog_name", "type": "VARCHAR"}],
  "rows": [
    {"catalog_name": "memory"},
    {"catalog_name": "remote"},
    {"catalog_name": "system"},
    {"catalog_name": "temp"}
  ],
  "row_count": 4,
  "truncated": false,
  "source": "sales-quack",
  "statement_hash": "sha256:..."
}
```

## Reference

| **field**      | **type** | **required** | **description**                                                                          |
|----------------|:--------:|:------------:|------------------------------------------------------------------------------------------|
| `type`         | string   | true         | Must be `"duckdb-list-catalogs"`.                                                        |
| `source`       | string   | true         | Name of the [`duckdb-quack`](../../source.md) source backing this tool.                  |
| `description`  | string   | true         | Description passed to the LLM.                                                           |
| `authRequired` | []string | false        | Names of auth services the caller must have verified to invoke the tool.                 |
| `annotations`  | object   | false        | MCP tool annotations. Defaults to read-only.                                             |
