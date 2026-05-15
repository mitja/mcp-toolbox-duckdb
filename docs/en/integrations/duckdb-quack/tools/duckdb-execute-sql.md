---
title: "duckdb-execute-sql"
type: docs
weight: 7
description: >
  Development-only tool that runs an agent-supplied SQL string against a
  duckdb-quack source. Gated behind an explicit `enabled: true` flag
  with a startup warning so it cannot be enabled by accident.
---

## About

A `duckdb-execute-sql` tool accepts a SQL statement as an agent
parameter, validates it against the source's policy (same validator
as the curated `duckdb-sql` tool — see its docs), and runs it through
`Source.RunSQL`. **The tool is intended for local development and
human-in-the-loop debugging only.** Spec §3 classifies a "let the LLM
run arbitrary DuckDB SQL" surface as a non-goal: SQL from an
untrusted source should be treated like arbitrary code.

To register the tool the YAML entry must contain `enabled: true`
explicitly. A missing field or `enabled: false` causes the server to
refuse to start with a clear error message. On every boot where the
tool is enabled, Toolbox logs a WARN-level line so the operator sees
it in logs, not just in YAML:

```
WARN duckdb-execute-sql is enabled. This tool exposes an agent-
supplied SQL surface and is intended for local development and
human-in-the-loop debugging only; do not enable it for production
agent toolsets. tool=dev_duckdb_execute_sql source=sales-quack
```

Validation runs **at every invocation** (not just config-load,
since the SQL is dynamic): multi-statement bodies are rejected, the
leading keyword must be in the allowlist, and the forbidden-token
scan rejects DDL/DML verbs plus anything in
`source.Policy.ForbiddenPatterns`. The validator is defense in depth;
the server-side `quack_authorization_function` is the real boundary.

## Compatible Sources

{{< compatible-sources >}}

## Parameters

| **field** | **type** | **required** | **description**                                                                                            |
|-----------|:--------:|:------------:|------------------------------------------------------------------------------------------------------------|
| `sql`     | string   | true         | One read-only SQL statement. Must satisfy the source's policy (allowed_statement_kinds, forbidden_patterns). |

## Example

```yaml
kind: tool
name: dev_duckdb_execute_sql
type: duckdb-execute-sql
source: sales-quack
description: >-
  Run an arbitrary read-only SQL against the analytics catalog.
  Dev / debugging only — do not expose to production agents.
enabled: true
```

The agent supplies the SQL as the single `sql` parameter:

```json
{"sql": "SELECT count(*) FROM remote.sales"}
```

## Reference

| **field**     | **type** | **required** | **description**                                                                                                |
|---------------|:--------:|:------------:|----------------------------------------------------------------------------------------------------------------|
| `type`        | string   | true         | Must be `"duckdb-execute-sql"`.                                                                                |
| `source`      | string   | true         | Name of the [`duckdb-quack`](../../source.md) source backing this tool.                                        |
| `description` | string   | true         | Description passed to the LLM.                                                                                 |
| `enabled`     | boolean  | **true**     | Must be `true`. Missing or `false` aborts server start with an error explaining the dev-only intent.           |
| `authRequired`| []string | false        | Names of auth services the caller must have verified to invoke the tool.                                       |
| `annotations` | object   | false        | MCP tool annotations. Defaults to **destructive** (so MCP clients treat the call as state-changing by default).|
