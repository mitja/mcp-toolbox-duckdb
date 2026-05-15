# Phase 1 Plan: DuckDB + Quack Backend for MCP Toolbox

Working branch: `feat/duckdb-quack` on `github.com/mitja/mcp-toolbox-duckdb` (fork of `googleapis/mcp-toolbox`, renamed upstream from `genai-toolbox`).

Spec: `.spec/mcp-toolbox-quack-duckdb.md` (local-only).

---

# Phase 1 Summary (status: **complete**, 2026-05-15)

End-to-end Phase 1 landed in two repositories. The forward-looking plan below remains as the working specification for the day-by-day work; this section captures what actually shipped and what we learned.

## Commits

**Fork** `mitja/mcp-toolbox-duckdb`, branch `feat/duckdb-quack`, 8 commits ahead of upstream `main`:

```
319ef068 docs(duckdb-quack): drop redundant kind/name from source example
189c27b8 ci: wire tests/duckdbquack into integration.cloudbuild.yaml
4e495900 docs(duckdb-quack): add source and tool pages
055bb1a1 feat(source/duckdb-quack): render DECIMAL as string, reject BLOB
2f1d124b test(source/duckdb-quack): in-process integration test + SECRET/ATTACH fixes
85c40f35 chore(deps): go mod tidy after adding duckdb-go
ad041e8e feat(tool/duckdb-sql): add curated SQL tool with statement policy
f21c99ab feat(source/duckdb-quack): scaffold source package
```

**Demo** `mitja/mcp-toolbox-duckdb-demo`, branch `main`, 2 commits:

```
80a3d07 fix(demo): smoke-test fixes after first end-to-end run
f2b8ef0 feat: initial demo stack for the MCP Toolbox DuckDB/Quack adapter
```

## Acceptance criteria (spec §9 Phase 1)

| Criterion | Status | Where |
|---|---|---|
| Tool with `DROP TABLE` fails config-load validation | ✅ | `TestInitialize_RejectsBadStatements` (unit) + `internal/tools/duckdb/duckdbsql/statement.go` Layer A |
| Direct destructive query fails server-side authz | ✅ | `TestServerSideAuthz_RejectsInsert` (integration, post-attach activation) |
| Valid SELECT returns typed JSON | ✅ | `TestTypedJSONShape` + curl through full Compose stack |
| `max_rows` honored, `truncated` flag set | ✅ | `TestMaxRowsTruncates` |
| `timeout` honored | ✅ | `TestTimeoutCancels` |
| `docker compose up` starts the stack and serves the tool | ✅ | Verified end-to-end on darwin/arm64 + OrbStack |

**Bonus result, spec §11 Risk 2 resolved:** all six parameter-binding cases (`?` string / float / int-IN / `time.Time` / ILIKE pattern / `$1` named) work transparently against tables attached over Quack. The fallback to parameterless server-side macros is not needed.

End-to-end JSON response verified through the full Compose stack:

```json
{
  "columns": [
    {"name": "customer", "type": "VARCHAR"},
    {"name": "revenue",  "type": "DECIMAL(38,2)"},
    {"name": "orders",   "type": "BIGINT"}
  ],
  "rows": [
    {"customer": "Alice GmbH", "revenue": "2661.65", "orders": 4},
    {"customer": "Frank GmbH", "revenue": "410",     "orders": 1}
  ],
  "row_count": 2,
  "truncated": false,
  "source": "sales-quack",
  "statement_hash": "sha256:e2c935d7313cbe8ef418a7db2350b2912affb49e567f30262afbdaeb79f91c98"
}
```

## Spec corrections discovered while building

1. `quack_serve` named-argument syntax is `:=`, not `=>` (spec §6.2 wrong).
2. `CREATE SECRET (TYPE quack, …)` accepts **only** `TOKEN`. `DISABLE_SSL` belongs on `ATTACH`. Spec §5.2 implied otherwise.
3. The Quack URI parser has a quirk: hostnames matching the scheme keyword (e.g., a docker service literally named `quack`) collide and parse as `host=<port>, port=<port>`. Workaround: rename the service. Worth filing upstream.
4. `quack_authorization_function` must be activated **after** the expected clients have ATTACHed. Earlier activation rejects ATTACH's own catalog probe queries. The demo's `init.sql.tmpl` creates the macro but leaves activation to a documented post-startup `docker exec` step.
5. `FROM` is not a valid leading statement kind. Spec §5.2 listed it in `allowed_statement_kinds`; the implementation drops it.
6. The upstream Toolbox `Dockerfile` cross-compiles via Zig, whose `lld` linker cannot resolve C++ stdlib symbols (`typeinfo for std::ostream`, etc.) that the DuckDB-Go static library requires. The demo uses an inline `dockerfile_inline:` that does a native CGO compile with `golang:1-bookworm` (matching `distroless/cc-debian12` for glibc) plus `g++` for libstdc++. The fork's upstream Dockerfile is untouched.

## Toolbox-side surprises (worth a callout in CLAUDE.local.md too)

- `--config` is the flag for the tools.yaml path; `--tools-file` (in the spec example) is not recognized.
- The `serve` subcommand is implicit on the root command; do not include it.
- `/api/...` endpoints require `--enable-api`. Default exposed surfaces are `/mcp` (JSON-RPC) and a few internal routes.
- The `tools.yaml` map-format uses the YAML map key as the entry name; do **not** also include `kind:` / `name:` inside each entry — Toolbox refuses to start with a duplicate-key error.
- macOS binds port 5000 to AirPlay; the demo publishes Toolbox on host port 5555.

## Notable design choices that landed

- **In-process Quack server for integration tests.** A second `sql.Open("duckdb", "")` in the test process hosts `quack_serve`, the client Source ATTACHes to it via localhost on a random free port. No Docker, no testcontainers, no external dependencies; the full integration test suite (8 cases including the parameter-binding matrix and DECIMAL/BLOB rendering) runs in under 3 seconds.
- **Three-layer defense in depth.** (1) Tool config-load validator catches developer mistakes; (2) per-invocation `policy.timeout` and `policy.max_rows`; (3) Quack server-side `quack_authorization_function = 'read_only'` as the real boundary. The implementation, docs, and demo README all repeat this framing.
- **Source policy is the authority.** Tools read `MaxRows`, `Timeout`, and `AllowedStatementKinds` via the source's `EffectivePolicy()` method, so multiple tools sharing a source share the policy. The `compatibleSource` interface in `duckdbsql` is duck-typed, so a future second DuckDB-backed source (if one ever lands) can plug in identically without changing the tool — but no such source is currently planned.
- **Statement validator with comment-aware scanning.** Handles `'..'`, `E'..'`, `".."`, `--`, `/* */`, doubled-quote escapes. `SELECT 'do not DROP this'` and `SELECT 1 -- DROP TABLE x` pass; `DROP TABLE x` and `SELECT 1; SELECT 2` fail. 19 accept + 22 reject unit cases.
- **DECIMAL → string via the column-type guard.** `renderValue` only calls `String()` when the column type starts with `DECIMAL`, so other Stringer types (notably `time.Time`) keep their normal JSON marshaling (RFC-3339).
- **BLOB rejected with a sentinel**, not an error. Replaces the cell with `"<blob: N bytes>"` so a single BLOB column doesn't kill the whole query. A future `result.allow_blob: true` opt-in is possible.

## CI wiring

`.ci/integration.cloudbuild.yaml` now has a `duckdbquack` step modeled on the SQLite shard. Its detect-changes pattern matches `duckdbquack|duckdb/duckdbsql|internal/server/|.ci/`. Coverage is scoped to the source and tool packages. The shared upstream test suites (`RunToolGetTest`, `RunToolInvokeTest`, `RunMCPToolCallMethod`) are **not** yet wired in — that's Phase 5 polish before the upstream PR.

## What's explicitly deferred

- **Phase 2** — metadata tools (`duckdb-list-catalogs/schemas/tables`, `duckdb-describe-table`, `duckdb-summarize-table`, `duckdb-quack-whoami`).
- **Phase 3** — richer policy engine (`forbidden_patterns`, `allowed_catalogs`, `allowed_schemas`, `allowed_tables`), `duckdb-execute-sql` dev tool with explicit-enable gate, server-side hardened auth/authz macros that don't break ATTACH.
- **Phase 4** — full OpenTelemetry spans/metrics per spec §8.
- **Phase 5** — shared `tests/tool.go` suite integration, golangci-lint clean, compatibility test matrix across DuckDB stable + Quack nightly, **file an issue on `googleapis/mcp-toolbox` and open the upstream PR.**

**Dropped from the original plan: embedded `duckdb` source + `DuckDBRunner` interface refactor.** Remote Quack is the architectural bet that distinguishes this project — lifecycle separation between Toolbox and the analytical runtime — and embedded DuckDB is already trivially achievable via the DuckDB CLI, duckdb-python, or running Quack on the same host and `ATTACH`ing locally. Adding a separate embedded source would dilute the focus and double the maintenance surface without a concrete user need. The `DuckDBRunner` refactor is YAGNI without a second implementation; the current duck-typed `compatibleSource` interface in `duckdbsql` provides the same polymorphism, and moving types into a shared package is a 15-minute mechanical refactor whenever it becomes useful. None of the remaining phases (2–5) depend on either. Reopen the discussion if a concrete user need for embedded DuckDB surfaces later.

**Also dropped from the original plan: the dedicated Phase for multi-remote / connection pooling / reconnect / backoff.** Multi-remote sources already work — each YAML `sources:` entry initializes an independent `*sql.DB` with its own ATTACH and Policy. Connection pooling is architecturally N/A for `duckdb-quack`: the ATTACH state lives on a single TCP connection, so `SetMaxOpenConns(1)` is the *correct* setting (mirrors the SQLite source) and exposing a knob would only break the source. Reconnect/backoff is a real concern for Quack specifically (the ATTACH is per-conn, so if the server restarts the next conn from the pool has lost state), but it's a ~30-line targeted change rather than a phase of work — listed below as a Phase-1 polish item.

## Phase-1 polish items

Status as of 2026-05-15:

### Done

- **✅ Reconnect / re-attach when the Quack server restarts.** `Source.RunSQL` now detects reattach-worthy errors (the catalog-missing message that names the alias, `driver.ErrBadConn`, duckdb-go's `Invalid connection id` after a Quack restart, and several Quack network-failure signatures), pins a fresh `*sql.Conn`, re-runs the per-conn bootstrap (`LOAD quack` + `CREATE OR REPLACE SECRET` + `DETACH` + `ATTACH`), and retries the user query once. Generic SQL errors bypass the retry so callers see their original failure verbatim. Verified end-to-end against the demo stack: `docker compose restart quack-server` followed by `curl /api/tool/.../invoke` returns typed JSON, with no caller-visible recovery seam. Commits: `d6f61ebd` (initial implementation, "Catalog … does not exist" + network signatures, integration test that DETACHes mid-flight) and `b883cba0` (adds the `Invalid connection id` pattern after the smoke test surfaced it as the actual production failure, plus a 12-case internal unit test that pins the matcher). Side-fix on the demo side: `mcp-toolbox-duckdb-demo@11ccde5` makes `init.sql` idempotent by wiping `/data/analytics.duckdb` on every container start, so `docker compose restart quack-server` no longer crashes on a duplicate-primary-key from the re-running seed.

### Still pending

- **DECIMAL scale preservation.** `duckdb-go.Decimal.String()` strips trailing zeros (`1370.50` → `"1370.5"`). The numeric value is preserved, but the column's declared scale is not. Fix: read `(precision, scale)` out of the `DECIMAL(p,s)` column type at row time and format the value to that scale ourselves.
- **Upstream bug filings (do NOT auto-file, see `CLAUDE.local.md`).** Two writeups are already paste-ready in `mcp-toolbox-duckdb-demo/NOTES.md`:
  1. Quack URI parser confuses hostname `quack` with the scheme keyword.
  2. `quack_authorization_function` rejects ATTACH's own internal catalog probes, making it impossible to enable authz before clients have attached.
- **`golangci-lint run --fix`.** Not installed on the dev machine; will run in CI on PR. Worth doing locally before the upstream PR (Phase 5).

---

## Critical investigation finding: no external plugin support

**In-tree fork is the only viable path.** Upstream `googleapis/mcp-toolbox` has no plugin mechanism:

- All sources and tools live under `internal/` — Go forbids importing those from outside the module.
- Registration is at compile time: each source/tool package's `init()` calls `sources.Register(...)` / `tools.Register(...)` into a global registry.
- Wiring is centralized in `cmd/internal/imports.go` — a single file with ~200 blank imports that link packages into the binary.
- No Go `plugin` (`.so`) loader, no Wasm/RPC sidecar, no YAML-driven shell-out.
- `DEVELOPER.md` confirms the contribution path: "We recommend looking at an example source implementation" → `internal/sources/postgres/postgres.go`.

**Implication:** We fork on `feat/duckdb-quack`, keep the module path `github.com/googleapis/mcp-toolbox` unchanged (so an upstream PR is trivial later), and add packages in-tree.

---

## Repo strategy

- Fork: `github.com/mitja/mcp-toolbox-duckdb` ✓ (already created).
- Working branch: `feat/duckdb-quack` ✓ (already created from `main`).
- Module path: leave as `github.com/googleapis/mcp-toolbox` (do not rename). Local development against the demo/LangGraph side can use `go mod edit -replace` or a `go.work`.
- Upstream sync cadence: periodically `git fetch upstream main` and merge/rebase. Add `upstream` remote when needed.
- Eventual upstream path: file a feature-request issue on `googleapis/mcp-toolbox` first (per `DEVELOPER.md`), then send a PR after Phase 1 stabilizes (Phase 5 in our renumbered plan; was Phase 7 in the original spec).

---

## Code layout

```
internal/sources/duckdbquack/
    duckdbquack.go                                # Config, Source, Register(), Initialize, RunSQL
    duckdbquack_test.go

internal/tools/duckdb/
    duckdbsql/
        duckdbsql.go                              # Config, Tool, Register(), Invoke
        duckdbsql_test.go

cmd/internal/imports.go                            # EDIT: add 2 blank imports

docs/en/resources/sources/duckdb-quack.md          # source docs
docs/en/resources/tools/duckdb-sql.md              # tool docs

tests/duckdbquack/                                 # integration tests, follows existing pattern

# Demo stack (under /demo so we can iterate without polluting the upstream tree;
# may move into a sibling repo later if upstream prefers minimal extras)
demo/quack-server/Dockerfile
demo/quack-server/init.sql
demo/quack-server/seed.sql
demo/langgraph/pyproject.toml
demo/langgraph/app.py
demo/tools.yaml
demo/docker-compose.yaml
demo/claude-code/.claude.json.example
```

Naming conventions (per `GEMINI.md` §"Tool Naming"):
- Source `type:` strings: `duckdb-quack`, `duckdb` (kebab-case, includes product).
- Tool `type:` strings: `duckdb-sql`, future `duckdb-list-tables`, etc.
- User-chosen tool `name:` strings: `snake_case` (e.g., `revenue_by_customer`), without product prefix.

Commit/branch conventions (per `GEMINI.md` §"Branching and Commits"):
- Conventional Commits: `feat(source/duckdb-quack): ...`, `feat(tool/duckdb-sql): ...`, `test(...)`, `docs(...)`.
- Branch: `feat/duckdb-quack` (already set).
- **No `Co-Authored-By: Claude` trailers** (see `CLAUDE.local.md`).

---

## Day-by-day deliverables

### Day 1 — `duckdb-quack` source package

**Template:** clone `internal/sources/sqlite/sqlite.go` (closest match: both use `database/sql`).

**Driver choice:** `github.com/duckdb/duckdb-go/v2` (the **official** DuckDB Go client, tracks DuckDB v1.5.2, the version Quack ships in). Avoid `marcboeker/go-duckdb` (legacy pre-v2.5 lineage).

**CGO caveat:** duckdb-go requires CGO. Confirm the fork's `Dockerfile` base image supports it — switch from `distroless/static` to `distroless/cc-debian12` if needed.

**Config struct (YAML-decoded):**
```go
type Config struct {
    Name           string `yaml:"name" validate:"required"`
    Type           string `yaml:"type" validate:"required"`
    URI            string `yaml:"uri" validate:"required"`    // e.g., quack:host:9494
    Token          string `yaml:"token" validate:"required"`
    DisableSSL     bool   `yaml:"disable_ssl"`
    ClientDatabase string `yaml:"client_database"`            // default ":memory:"
    AttachAlias    string `yaml:"attach_alias"`               // default "remote"
    AttachOnStartup bool  `yaml:"attach_on_startup"`          // default true

    Quack struct {
        InstallFrom    string `yaml:"install_from"`           // default "core_nightly"
        LoadExtension  bool   `yaml:"load_extension"`         // default true
        UseSecret      bool   `yaml:"use_secret"`             // default true
    } `yaml:"quack"`

    Policy struct {
        ReadOnly                  bool          `yaml:"read_only"`                    // default true
        RejectMultipleStatements  bool          `yaml:"reject_multiple_statements"`   // default true
        AllowedStatementKinds     []string      `yaml:"allowed_statement_kinds"`      // default [SELECT,WITH,DESCRIBE,SHOW,EXPLAIN]
        MaxRows                   int           `yaml:"max_rows"`                     // default 1000
        MaxBytes                  int           `yaml:"max_bytes"`                    // default 2*1024*1024
        Timeout                   time.Duration `yaml:"timeout"`                      // default 30s
    } `yaml:"policy"`
}
```

**Required symbols:**
- `const SourceType = "duckdb-quack"`
- `func init() { sources.Register(SourceType, newConfig) }`
- `func newConfig(ctx, name, decoder) (sources.SourceConfig, error)`
- `func (Config) SourceConfigType() string { return SourceType }`
- `func (r Config) Initialize(ctx, tracer) (sources.Source, error)`
- `type Source struct { Config; Db *sql.DB }`
- `func (s *Source) SourceType() string`, `func (s *Source) ToConfig() any`
- Type-unique getter: `func (s *Source) DuckDBQuackDB() *sql.DB { return s.Db }` (this is the duck-typed compat marker the tool will require — mirrors `SQLiteDB()`/`PostgresPool()` upstream)
- `func (s *Source) RunSQL(ctx, statement string, params []any) (any, error)`
- `func (s *Source) Close() error` — flushes WAL on persistent databases (no-op for `:memory:` but cheap to wire)

**Initialize flow:**
1. `db, _ := sql.Open("duckdb", "")` — in-memory client.
2. `db.SetMaxOpenConns(1); db.SetMaxIdleConns(0)` (idle conns extend temp-table lifetimes in DuckDB; the `ATTACH` lives on a single conn).
3. `db.ExecContext(ctx, "INSTALL quack FROM " + r.Quack.InstallFrom)` then `db.ExecContext(ctx, "LOAD quack")`.
4. If `Quack.UseSecret`: `CREATE SECRET toolbox_<sanitizedName> ( TYPE quack, TOKEN '<escaped>', DISABLE_SSL <bool> )` — **note: `CREATE SECRET` can't bind `?` placeholders**; must escape the token into the SQL. Enforce `^[A-Za-z0-9_-]{8,}$` on token at config load.
5. If `AttachOnStartup`: parse URI to extract `host:port`, then `ATTACH 'quack:host:port' AS <alias> (TYPE quack)`.
6. `db.PingContext(ctx)` to verify.

**Imports edit:** add to `cmd/internal/imports.go` (alphabetical, after `couchbase`):
```go
_ "github.com/googleapis/mcp-toolbox/internal/sources/duckdbquack"
```

**Day 1 verification:** `go build ./...` and `go test ./internal/sources/duckdbquack/...` (unit tests for config validation only — integration test is Day 4).

---

### Day 2 — `duckdb-sql` tool + parameter-binding gate

**Template:** clone `internal/tools/sqlite/sqlitesql/sqlitesql.go`. Change package, `resourceType` → `"duckdb-sql"`, swap `compatibleSource` to require `DuckDBQuackDB()`.

**Duck-typed compat interface:**
```go
type compatibleSource interface {
    DuckDBQuackDB() *sql.DB
    RunSQL(context.Context, string, []any) (any, error)
}
```
The duck-typed shape keeps the tool decoupled from any specific source struct, so a hypothetical future DuckDB-backed source (e.g., embedded, if a concrete need ever surfaces) could plug in without changing the tool. No such source is planned.

**Statement policy validation at config-load** (in `Tool.Initialize`, runs once at server start — satisfies "tool with `DROP TABLE` fails validation"):
1. Trim, strip trailing `;`.
2. Reject if any unquoted/uncommented `;` remains (multi-statement guard via small state machine handling `'..'`, `".."`, `--`, `/* .. */`).
3. First non-comment word, uppercased, must be in `policy.AllowedStatementKinds`. **Spec correction:** drop `FROM` from the leading-keyword allowlist; `FROM` is not a valid statement opener.
4. Forbidden substring scan (case-insensitive, word-boundary aware): `INSTALL`, `LOAD`, `ATTACH`, `DETACH`, `CREATE SECRET`, `COPY`, `CALL`, `PRAGMA`, `INSERT`, `UPDATE`, `DELETE`, `DROP`, `ALTER`, `TRUNCATE`, `MERGE`, `GRANT`, `REVOKE`.
5. Fail Toolbox startup with a clear error if any tool fails. **This is defense in depth, not the boundary** — the boundary is the Quack server's `read_only` authz macro.

**Per-invocation runtime enforcement:**
- `ctx, cancel := context.WithTimeout(ctx, source.Policy.Timeout); defer cancel()`.
- `source.RunSQL(ctx, statement, params.AsSlice())`.
- Wrap result with `truncated`, `row_count`, `source` name, `statement_hash` (sha256 hex of canonical statement, computed once at `Initialize`).

**Parameter binding — the critical gating test:**

Spec Risk 2 flags prepared-statement binding across an attached Quack catalog as unverified. Add `tests/duckdbquack/binding_test.go` on Day 2 with this matrix:

| Test | Statement | Param type |
|---|---|---|
| T1 | `SELECT * FROM remote.sales WHERE customer = ?` | string |
| T2 | `SELECT * FROM remote.sales WHERE amount > ?` | float64 |
| T3 | `SELECT * FROM remote.sales WHERE id IN (?, ?, ?)` | three int64 |
| T4 | `SELECT * FROM remote.sales WHERE order_date >= ?` | time.Time |
| T5 | `SELECT * FROM remote.sales WHERE customer ILIKE '%' \|\| ? \|\| '%'` | string with quotes |
| T6 | `SELECT * FROM remote.sales WHERE customer = $1` | named $-style |

Each test asserts the bound result equals the literal-SQL result. **All must pass to proceed with Day 3 as planned.**

**Fallback if any T1–T6 fails:** restrict Phase 1 tools to parameterless server-side macros. Server-side:
```sql
CREATE MACRO sales_by_customer(p) AS TABLE
  SELECT * FROM sales WHERE customer ILIKE '%' || p || '%';
```
Tool calls `SELECT * FROM sales_by_customer(?)`. The `?` is a single positional arg to a function call — simpler binding path.

**Imports edit:** add to `cmd/internal/imports.go` (in tools block):
```go
_ "github.com/googleapis/mcp-toolbox/internal/tools/duckdb/duckdbsql"
```

---

### Day 3 — Demo stack

**Quack server image** (`demo/quack-server/Dockerfile`):
```dockerfile
FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y curl unzip ca-certificates \
 && curl -L https://github.com/duckdb/duckdb/releases/download/v1.5.2/duckdb_cli-linux-amd64.zip -o /tmp/d.zip \
 && unzip /tmp/d.zip -d /usr/local/bin && rm /tmp/d.zip
COPY init.sql seed.sql /etc/quack/
EXPOSE 9494
CMD ["sh", "-c", "duckdb /data/analytics.duckdb -init /etc/quack/init.sql"]
```

**Seed data** (`demo/quack-server/seed.sql`): `sales(id, customer, amount, order_date)` + `orders(id, customer, product, qty, order_date)`, ~50 rows spanning a few customers.

**Server init/hardening** (`demo/quack-server/init.sql`):
```sql
.read /etc/quack/seed.sql
INSTALL quack FROM core_nightly;
LOAD quack;

CREATE TABLE quack_tokens(auth_token VARCHAR, user_name VARCHAR);
INSERT INTO quack_tokens VALUES ('analytics-team-token', 'analytics-agent');

CREATE MACRO check_token(sid, client_token, server_token) AS (
    EXISTS (SELECT 1 FROM quack_tokens WHERE auth_token = client_token));
CREATE MACRO read_only(sid, query) AS (
    regexp_matches(upper(trim(query)), '^(SELECT|WITH|EXPLAIN|DESCRIBE|SHOW)\b'));

CALL quack_serve('quack:0.0.0.0:9494',
                 token := 'analytics-team-token',
                 allow_other_hostname := true);

SET GLOBAL quack_authentication_function = 'check_token';
SET GLOBAL quack_authorization_function  = 'read_only';
```

**Spec corrections found while drafting this:**
- `quack_serve` uses **`:=`** for named args (spec §6.2 has `=>` — wrong).
- `FROM` was listed as a leading-statement-kind allowlist member (spec §5.2) — not valid; dropped.
- `allow_other_hostname := true` is required because clients connect to `quack` (Compose service name), not `0.0.0.0`.

**`demo/docker-compose.yaml`:**
```yaml
services:
  quack:
    build: ./quack-server
    healthcheck:
      test: ["CMD", "sh", "-c", "echo 'SELECT 1' | duckdb"]
      interval: 5s
  toolbox:
    build: ../  # fork root
    command: ["serve", "--tools-file", "/cfg/tools.yaml", "--address", "0.0.0.0"]
    environment:
      QUACK_TOKEN: analytics-team-token
    volumes:
      - ./tools.yaml:/cfg/tools.yaml:ro
    depends_on:
      quack:
        condition: service_healthy
    ports: ["5000:5000"]
  langgraph:
    build: ./langgraph
    environment:
      TOOLBOX_URL: http://toolbox:5000
    depends_on: [toolbox]
```

**`demo/tools.yaml`:** spec §9 Phase 1 example verbatim (source `sales-quack`, tool `revenue_by_customer`, toolset `analytics_readonly`).

**LangGraph demo** (`demo/langgraph/app.py`):
```python
from toolbox_langchain import ToolboxClient
from langgraph.prebuilt import create_react_agent
from langchain_anthropic import ChatAnthropic

client = ToolboxClient("http://toolbox:5000")
tools = client.load_toolset("analytics_readonly")
agent = create_react_agent(ChatAnthropic(model="claude-sonnet-4-6"), tools)
print(agent.invoke({"messages": [("user", "Show top customers matching 'gmbh'")]}))
```
`pyproject.toml`: `toolbox-langchain`, `langgraph`, `langchain-anthropic`.

**Claude Code MCP config** (`demo/claude-code/.claude.json.example`):
```json
{
  "mcpServers": {
    "toolbox-analytics": {
      "url": "http://localhost:5000/mcp",
      "transport": "http"
    }
  }
}
```
(Toolbox exposes MCP at `/mcp`; verify against `internal/server/mcp/` paths during Day 3.)

---

### Day 4 — Tests, docs, polish

**Unit tests:**
- `TestStatementValidator_*` — table-driven: `DROP TABLE` fails, `SELECT *` passes, `SELECT 1; SELECT 2` fails, comments handled correctly.
- `TestConfig_RejectsDropTable` — YAML with `statement: DROP TABLE x` fails to load.
- `TestConfig_RejectsBadToken` — token not matching `^[A-Za-z0-9_-]{8,}$` fails.

**Integration tests** (`tests/duckdbquack/integration_test.go`, gated by `-tags=integration`, testcontainers-go like `tests/postgres/`):
- `TestRevenueByCustomer_TypedJSON` — asserts column types in response (e.g., `DECIMAL(18,2)` → string).
- `TestMaxRows_TruncatesAndFlagsTrue` — seed 5000 rows, `max_rows: 100`, expect `truncated: true` and exactly 100 rows.
- `TestTimeout_CancelsLongQuery` — `SELECT count(*) FROM range(10_000_000) t1, range(10_000) t2` with 1s timeout.
- `TestQuackServer_RejectsInsert` — issue raw `db.Exec("INSERT INTO remote.sales ...")` bypassing the tool layer; expect server-side authz rejection (proves the Quack `read_only` macro is the real boundary).

**Docs:**
- `docs/en/resources/sources/duckdb-quack.md` — config reference matching upstream doc style.
- `docs/en/resources/tools/duckdb-sql.md` — tool reference.
- `README.md` updates at fork root + `demo/README.md` quickstart (`docker compose up`, expected output, troubleshooting).

**CI wiring:**
- Add `tests/duckdbquack/` to `.ci/integration.cloudbuild.yaml` per upstream `GEMINI.md` instructions.
- Unit tests run by default in `go test -race -v ./internal/...`.

---

## Result serializer (spec §7)

Lives in `internal/sources/duckdbquack/duckdbquack.go`, function `Source.RunSQL`. Returns `any` (per upstream `Source` interface), serialized to JSON by the chi router.

```go
type column struct { Name, Type string }   // type from rows.ColumnTypes() → DatabaseTypeName()
type result struct {
    Columns       []column      `json:"columns"`
    Rows          []orderedmap.Row `json:"rows"`   // reuse internal/util/orderedmap.Row for column order
    RowCount      int           `json:"row_count"`
    Truncated     bool          `json:"truncated"`
    Source        string        `json:"source"`
    StatementHash string        `json:"statement_hash"`
}
```

**Type mapping:**

| DuckDB type | Go scan target | JSON shape |
|---|---|---|
| `BOOLEAN` | `*bool` | bool |
| `TINYINT`..`BIGINT` | `*int64` | number |
| `UTINYINT`..`UBIGINT` | `*uint64` | number (string above 2^53) |
| `HUGEINT`, `UHUGEINT` | `*string` | string |
| `FLOAT`, `DOUBLE` | `*float64` | number |
| `DECIMAL(p,s)` | `*string` | **string** (default per spec §7) |
| `VARCHAR`, `UUID`, `JSON` | `*string` | string |
| `DATE` | `*time.Time` | ISO-8601 date |
| `TIME`, `TIMETZ` | `*time.Time` | ISO-8601 |
| `TIMESTAMP`, `TIMESTAMPTZ` | `*time.Time` | RFC-3339 |
| `LIST`, `STRUCT`, `MAP` | `*string` → re-parse | nested JSON |
| `BLOB` | `*[]byte` | **rejected by default** (future `result.allow_blob: true`) |
| NULL | `*any` → nil | `null` |

**Limits enforced inside `RunSQL`:**
- `policy.MaxRows`: stop after N rows scanned, set `Truncated=true`, drain remaining rows.
- `policy.MaxBytes`: rough byte counter via `json.Marshal` per row; stop early.
- `policy.MaxCellBytes`: per-cell guard → `"<truncated:N bytes>"` sentinel.
- `policy.Timeout`: enforced by `ctx` deadline at the tool layer; source just respects `ctx`.

---

## Statement policy (two layers, both must pass)

**Layer A — config-load-time, in `duckdbsql.Config.Initialize`.** Catches `DROP TABLE` in the YAML the developer writes. Rules listed in §"Day 2".

**Layer B — runtime, on the Quack server.** The `read_only` authz macro is the **real** boundary. Even if Layer A is bypassed (bug, regex evasion), the server refuses anything that doesn't start with `SELECT|WITH|EXPLAIN|DESCRIBE|SHOW`. The agent never sees raw write capability.

Code comments must frame this explicitly: "Layer A is developer-tooling sanity. Layer B is the security boundary. Neither is a SQL sandbox — agents must never construct SQL fragments, only bound values."

---

## Test plan for MVP acceptance criteria

| Spec §9 criterion | Test file | Test name |
|---|---|---|
| `docker compose up` starts all services | Makefile target / `tests/duckdbquack/compose_test.go` | `TestCompose_AllServicesHealthy` |
| LangGraph can call `revenue_by_customer` | `demo/langgraph/test_smoke.py` | `test_revenue_by_customer_returns_rows` |
| Claude Code MCP shows only configured toolset | manual + `curl http://localhost:5000/mcp` (JSON-RPC `tools/list`) | `TestMcpToolsList` |
| Tool with `DROP TABLE` fails validation | `internal/tools/duckdb/duckdbsql/duckdbsql_test.go` | `TestConfig_RejectsDropTable` |
| Direct destructive query fails server-side authz | `tests/duckdbquack/integration_test.go` | `TestQuackServer_RejectsInsert` |
| Valid SELECT returns typed JSON | `tests/duckdbquack/integration_test.go` | `TestRevenueByCustomer_TypedJSON` |
| `max_rows` honored | `tests/duckdbquack/integration_test.go` | `TestMaxRows_TruncatesAndFlagsTrue` |
| `timeout` honored | `tests/duckdbquack/integration_test.go` | `TestTimeout_CancelsLongQuery` |

---

## Risks (Phase-1-specific, beyond spec §11)

1. **Parameter binding across `ATTACH` is the biggest unknown.** Day 2 binding test gates the rest of the plan.
2. **CGO/distroless mismatch.** Confirm Dockerfile base before Day 1 Docker build, or fork image won't run.
3. **`core_nightly` is a floating reference.** Pin Quack extension version on both client and server (`INSTALL quack VERSION '...' FROM core_nightly` — verify syntax Day 1), or independent restarts can drift.
4. **`CREATE SECRET` can't use `?` placeholders.** Must string-escape token; enforce `^[A-Za-z0-9_-]{8,}$` on `token` at config load.
5. **`ATTACH` lives on a single client conn.** `SetMaxOpenConns(1)` reduces but doesn't eliminate disconnect risk. Add pre-query check that re-attaches if the alias disappears.
6. **YAML envvar substitution timing.** `${QUACK_TOKEN}` in `tools.yaml` and the Quack server's `init.sql` token must stay in sync; document via Compose `environment:` propagation.
7. **Testcontainers + custom Quack image.** No public Quack image exists; we build from `demo/quack-server/Dockerfile` and load into test docker context (~20s setup overhead per integration test run).

---

## Out of scope (deferred to Phases 2–5)

- Metadata tools: `duckdb-list-tables`, `duckdb-describe-table`, `duckdb-summarize-table`, `duckdb-quack-whoami` — Phase 2.
- Configurable policy with `forbidden_patterns`, `allowed_catalogs`, `allowed_schemas`, `allowed_tables` — Phase 3.
- `duckdb-execute-sql` dev tool with explicit-enable gate — Phase 3.
- Full OpenTelemetry spans/metrics per spec §8 (Phase 1 emits only what upstream chi/router spans give by default) — Phase 4.
- Compatibility test matrix across DuckDB stable/nightly — Phase 5.
- Arrow result mode, pagination, server-side result caching, Pydantic AI demo — Phase 5 could-haves.
- Upstream PR to `googleapis/mcp-toolbox` — after Phase 5 stabilizes the fork.

**Dropped from the original spec phase plan:** embedded `duckdb` source (file-backed), the shared `DuckDBRunner` interface refactor, and the dedicated phase for multi-remote / connection pooling / reconnect / backoff. Remote Quack is the project's distinguishing architectural bet, embedded support would dilute it, and the runner refactor is YAGNI without a second implementation. Multi-remote already works via independent `sources:` entries. Connection pooling is architecturally N/A — Quack's ATTACH is per-conn, so `SetMaxOpenConns(1)` is the correct value (matching the SQLite source). Reconnect/backoff is a real concern but a targeted ~30-line change — see the Phase-1 polish item above. None of Phases 2–5 depend on any of these.
