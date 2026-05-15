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

// Package duckdbquack implements an MCP Toolbox source that connects to a
// remote DuckDB instance via the Quack remote protocol.
//
// The source opens an in-memory DuckDB client, loads the Quack extension,
// creates a Quack secret for authentication, and attaches the remote DuckDB
// catalog under a configurable alias. Tools then issue read-only SQL against
// the attached catalog.
package duckdbquack

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	_ "github.com/duckdb/duckdb-go/v2" // DuckDB driver, registers as "duckdb"
	"github.com/goccy/go-yaml"
	"github.com/googleapis/mcp-toolbox/internal/sources"
	"github.com/googleapis/mcp-toolbox/internal/util/orderedmap"
	"go.opentelemetry.io/otel/trace"
)

const SourceType string = "duckdb-quack"

// Defaults applied when the YAML config omits an optional field.
const (
	defaultClientDatabase = ":memory:"
	defaultAttachAlias    = "remote"
	defaultInstallFrom    = "core_nightly"
	defaultMaxRows        = 1000
	defaultTimeout        = 30 * time.Second
)

// tokenPattern restricts Quack tokens to characters that are safe to embed
// in a CREATE SECRET / ATTACH statement without further escaping. DuckDB's
// CREATE SECRET syntax does not accept bind parameters, so the token must
// be interpolated; enforcing this character set at config load prevents
// quote-injection regardless of where the token came from (env var, file).
var tokenPattern = regexp.MustCompile(`^[A-Za-z0-9_\-]{8,}$`)

// decimalTypePattern matches a DuckDB type-name string like "DECIMAL(18,2)"
// and captures the scale group. Used as a fallback when the driver does not
// implement sql.ColumnType.DecimalSize().
var decimalTypePattern = regexp.MustCompile(`^DECIMAL\(\s*\d+\s*,\s*(\d+)\s*\)$`)

// validate interface
var _ sources.SourceConfig = Config{}

func init() {
	if !sources.Register(SourceType, newConfig) {
		panic(fmt.Sprintf("source type %q already registered", SourceType))
	}
}

func newConfig(ctx context.Context, name string, decoder *yaml.Decoder) (sources.SourceConfig, error) {
	actual := Config{Name: name}
	if err := decoder.DecodeContext(ctx, &actual); err != nil {
		return nil, err
	}
	return actual, nil
}

// QuackOptions controls how the Quack extension is loaded and how the remote
// catalog is attached on the client side.
type QuackOptions struct {
	// InstallFrom is the DuckDB extension repository to install Quack from.
	// Defaults to "core_nightly". Quack is in beta; pin a stable repo in
	// production.
	InstallFrom string `yaml:"install_from"`

	// UseSecret, when true (default), creates a DuckDB SECRET of TYPE quack
	// holding the token, then attaches the remote without an inline token.
	// When false, the token is passed inline on the ATTACH statement.
	UseSecret *bool `yaml:"use_secret"`
}

// Policy controls how tools using this source execute SQL: which statement
// kinds are allowed, how many rows may be returned, and how long a query may
// run. Tools enforce these at config-load (allowed kinds) and per-invocation
// (max rows, timeout). The Quack server's own authorization callback is the
// real security boundary; Policy is defense in depth and developer-ergonomic
// sanity-checking.
type Policy struct {
	// ReadOnly is informational at present; the Quack server's read_only
	// authorization callback is the enforced boundary.
	ReadOnly bool `yaml:"read_only"`

	// RejectMultipleStatements rejects SQL containing more than one
	// statement at tool config-load time. Defaults to true.
	RejectMultipleStatements *bool `yaml:"reject_multiple_statements"`

	// AllowedStatementKinds is the set of leading keywords accepted by
	// the duckdb-sql tool. Empty means "use the tool's built-in default".
	AllowedStatementKinds []string `yaml:"allowed_statement_kinds"`

	// MaxRows caps the number of rows returned to the caller. 0 means
	// "use the default" (1000). Excess rows are dropped and Truncated is
	// set to true on the result.
	MaxRows int `yaml:"max_rows"`

	// Timeout is the per-invocation context deadline applied by the
	// tool layer. 0 means "use the default" (30s).
	Timeout time.Duration `yaml:"timeout"`
}

// Config is the YAML-decoded configuration for a duckdb-quack source.
type Config struct {
	Name            string       `yaml:"name" validate:"required"`
	Type            string       `yaml:"type" validate:"required"`
	URI             string       `yaml:"uri" validate:"required"`   // e.g., "quack:host:9494"
	Token           string       `yaml:"token" validate:"required"` // see tokenPattern
	DisableSSL      bool         `yaml:"disable_ssl"`
	ClientDatabase  string       `yaml:"client_database"`           // default ":memory:"
	AttachAlias     string       `yaml:"attach_alias"`              // default "remote"
	AttachOnStartup *bool        `yaml:"attach_on_startup"`         // default true
	Quack           QuackOptions `yaml:"quack"`
	Policy          Policy       `yaml:"policy"`
}

func (r Config) SourceConfigType() string {
	return SourceType
}

// withDefaults returns a copy of the config with empty optional fields
// replaced by their defaults.
func (r Config) withDefaults() Config {
	c := r
	if c.ClientDatabase == "" {
		c.ClientDatabase = defaultClientDatabase
	}
	if c.AttachAlias == "" {
		c.AttachAlias = defaultAttachAlias
	}
	if c.AttachOnStartup == nil {
		t := true
		c.AttachOnStartup = &t
	}
	if c.Quack.InstallFrom == "" {
		c.Quack.InstallFrom = defaultInstallFrom
	}
	if c.Quack.UseSecret == nil {
		t := true
		c.Quack.UseSecret = &t
	}
	if c.Policy.RejectMultipleStatements == nil {
		t := true
		c.Policy.RejectMultipleStatements = &t
	}
	if c.Policy.MaxRows == 0 {
		c.Policy.MaxRows = defaultMaxRows
	}
	if c.Policy.Timeout == 0 {
		c.Policy.Timeout = defaultTimeout
	}
	return c
}

// EffectivePolicy returns the resolved Policy after defaults are applied.
// Tools call this to read MaxRows / Timeout / AllowedStatementKinds.
func (s *Source) EffectivePolicy() Policy {
	return s.Config.Policy
}

func (r Config) Initialize(ctx context.Context, tracer trace.Tracer) (sources.Source, error) {
	cfg := r.withDefaults()

	if !tokenPattern.MatchString(cfg.Token) {
		return nil, fmt.Errorf("invalid token for duckdb-quack source %q: must match %s", cfg.Name, tokenPattern)
	}
	if !strings.HasPrefix(cfg.URI, "quack:") {
		return nil, fmt.Errorf("duckdb-quack source %q: uri must start with \"quack:\", got %q", cfg.Name, cfg.URI)
	}
	if err := validateIdentifier(cfg.AttachAlias); err != nil {
		return nil, fmt.Errorf("duckdb-quack source %q: invalid attach_alias: %w", cfg.Name, err)
	}

	db, err := initClient(ctx, tracer, cfg)
	if err != nil {
		return nil, fmt.Errorf("unable to initialize duckdb-quack client: %w", err)
	}

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("unable to connect successfully: %w", err)
	}

	return &Source{Config: cfg, Db: db}, nil
}

var _ sources.Source = &Source{}

// Source is an initialized duckdb-quack source. It wraps a single-connection
// DuckDB client that holds the Quack ATTACH state.
type Source struct {
	Config
	Db *sql.DB
}

func (s *Source) SourceType() string {
	return SourceType
}

func (s *Source) ToConfig() sources.SourceConfig {
	return s.Config
}

// DuckDBQuackDB returns the underlying *sql.DB handle. Tools compatible with
// the duckdb-quack source duck-type on this method.
func (s *Source) DuckDBQuackDB() *sql.DB {
	return s.Db
}

// Close releases the underlying DuckDB client. Required by duckdb-go for WAL
// flushing on persistent databases (no-op for ":memory:" but wired anyway).
func (s *Source) Close() error {
	if s.Db == nil {
		return nil
	}
	return s.Db.Close()
}

// initClient opens the in-memory DuckDB client, installs and loads the Quack
// extension, creates a Quack secret (if configured), and attaches the remote
// catalog under the configured alias.
func initClient(ctx context.Context, tracer trace.Tracer, cfg Config) (*sql.DB, error) {
	//nolint:all // Reassigned ctx for span propagation.
	ctx, span := sources.InitConnectionSpan(ctx, tracer, SourceType, cfg.Name)
	defer span.End()

	db, err := sql.Open("duckdb", cfg.ClientDatabase)
	if err != nil {
		return nil, fmt.Errorf("sql.Open duckdb: %w", err)
	}

	// The ATTACH state lives on a single connection. Limit the pool to one
	// to avoid silently losing the attach when an idle conn is recycled.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	if _, err := db.ExecContext(ctx, "INSTALL quack FROM "+cfg.Quack.InstallFrom); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("INSTALL quack: %w", err)
	}
	if _, err := db.ExecContext(ctx, "LOAD quack"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("LOAD quack: %w", err)
	}

	if cfg.Quack.UseSecret != nil && *cfg.Quack.UseSecret {
		if err := createQuackSecret(ctx, db, cfg); err != nil {
			_ = db.Close()
			return nil, err
		}
	}

	if cfg.AttachOnStartup != nil && *cfg.AttachOnStartup {
		if err := attachRemote(ctx, db, cfg); err != nil {
			_ = db.Close()
			return nil, err
		}
	}

	return db, nil
}

// createQuackSecret creates a DuckDB SECRET of TYPE quack carrying the token.
// The Quack SECRET schema only accepts TOKEN; TLS preference lives on the
// ATTACH statement instead (see attachRemote). The secret name is scoped to
// the source name to permit multiple duckdb-quack sources within the same
// Toolbox process.
//
// CREATE SECRET does not accept bind parameters, so the token is interpolated
// into the SQL. Token format is enforced by tokenPattern in Initialize, which
// makes injection-by-quotes impossible.
func createQuackSecret(ctx context.Context, db *sql.DB, cfg Config) error {
	secretName := "toolbox_" + sanitizeIdentifier(cfg.Name)
	stmt := fmt.Sprintf(
		"CREATE SECRET %s (TYPE quack, TOKEN '%s')",
		secretName, cfg.Token,
	)
	if _, err := db.ExecContext(ctx, stmt); err != nil {
		return fmt.Errorf("CREATE SECRET for source %q: %w", cfg.Name, err)
	}
	return nil
}

// attachRemote runs ATTACH against the configured URI. The 'quack:' URI scheme
// implies TYPE quack; we pass it explicitly for clarity and forward the
// disable_ssl preference if the user set it. (Quack defaults to plaintext for
// localhost and TLS otherwise.)
func attachRemote(ctx context.Context, db *sql.DB, cfg Config) error {
	opts := "TYPE quack"
	if cfg.DisableSSL {
		opts += ", DISABLE_SSL true"
	}
	stmt := fmt.Sprintf("ATTACH '%s' AS %s (%s)", cfg.URI, cfg.AttachAlias, opts)
	if _, err := db.ExecContext(ctx, stmt); err != nil {
		return fmt.Errorf("ATTACH %q AS %s: %w", cfg.URI, cfg.AttachAlias, err)
	}
	return nil
}

// Column describes one column in a QueryResult. Type is the DuckDB type name
// as reported by rows.ColumnTypes() (e.g., "VARCHAR", "DECIMAL(18,2)").
type Column struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// QueryResult is the structured response from RunSQL. Tools wrap this with
// row_count, source name, and statement_hash to produce the spec §7 JSON.
type QueryResult struct {
	Columns   []Column         `json:"columns"`
	Rows      []orderedmap.Row `json:"rows"`
	Truncated bool             `json:"truncated"`
}

// QueryOptions carries per-invocation knobs. MaxRows=0 means no row limit.
type QueryOptions struct {
	MaxRows int
}

// quackQuerier is the subset of *sql.DB / *sql.Conn that executeQuery
// needs. Splitting it out lets RunSQL re-execute a query on a pinned
// *sql.Conn during the re-attach recovery path without duplicating the
// row-scanning code.
type quackQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// QuackQuery executes a SQL statement directly on the remote Quack server
// via the `quack_query()` table function, bypassing the ATTACHed catalog.
// This is the route for metadata queries that DuckDB does not push down
// through ATTACH — notably anything against information_schema.tables,
// information_schema.columns, SHOW TABLES, DESCRIBE, and SUMMARIZE, all of
// which return empty / fail against the local view of an attached Quack
// catalog but work cleanly when invoked on the remote server.
//
// The token does not need to be passed explicitly: quack_query resolves
// the matching `CREATE SECRET (TYPE quack, …)` automatically. DisableSSL
// is forwarded from the source config.
//
// remoteSQL is interpolated into the outer query as a string literal, so
// single quotes inside it are escaped. The URI is similarly escaped (the
// source already enforces the `quack:` prefix and token format at config
// load, so the URI is developer-controlled, not agent-controlled).
func (s *Source) QuackQuery(ctx context.Context, remoteSQL string, opts QueryOptions) (*QueryResult, error) {
	escapedURI := strings.ReplaceAll(s.URI, "'", "''")
	escapedSQL := strings.ReplaceAll(remoteSQL, "'", "''")
	stmt := fmt.Sprintf(
		"SELECT * FROM quack_query('%s', '%s', disable_ssl := %t)",
		escapedURI, escapedSQL, s.DisableSSL,
	)
	return s.RunSQL(ctx, stmt, nil, opts)
}

// RunSQL executes a SQL statement against the client DuckDB (which may
// reference attached Quack catalog tables) and returns columns, rows, and a
// truncation flag. Column types are captured from rows.ColumnTypes() so the
// caller can apply DuckDB-aware type rules.
//
// If opts.MaxRows > 0 and the query produces more rows than MaxRows, the
// extra rows are drained from the cursor (to release server resources) and
// Truncated is set to true.
//
// Reconnect path: if the first attempt fails with an error that suggests the
// ATTACH was lost (server restart, conn replaced by the pool, etc.), RunSQL
// pins a fresh *sql.Conn from the pool, re-runs the LOAD / CREATE SECRET /
// ATTACH bootstrap on it, and retries the user query once on that pinned
// conn. Detection is conservative (see needsReAttach) — generic SQL errors
// are returned verbatim so the caller sees the original failure.
func (s *Source) RunSQL(ctx context.Context, statement string, params []any, opts QueryOptions) (*QueryResult, error) {
	res, err := s.executeQuery(ctx, s.Db, statement, params, opts)
	if err == nil || !needsReAttach(err, s.AttachAlias) {
		return res, err
	}

	conn, cErr := s.Db.Conn(ctx)
	if cErr != nil {
		return nil, fmt.Errorf("query failed: %w (acquiring conn for retry also failed: %v)", err, cErr)
	}
	defer conn.Close()

	if raErr := s.reAttachOnConn(ctx, conn); raErr != nil {
		return nil, fmt.Errorf("query failed: %w (re-attach also failed: %v)", err, raErr)
	}
	return s.executeQuery(ctx, conn, statement, params, opts)
}

// executeQuery runs the SQL on q (either the source's *sql.DB or a pinned
// *sql.Conn) and serializes the rows per spec §7.
func (s *Source) executeQuery(ctx context.Context, q quackQuerier, statement string, params []any, opts QueryOptions) (*QueryResult, error) {
	rows, err := q.QueryContext(ctx, statement, params...)
	if err != nil {
		return nil, fmt.Errorf("unable to execute query: %w", err)
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return nil, fmt.Errorf("unable to get column names: %w", err)
	}
	colTypes, err := rows.ColumnTypes()
	if err != nil {
		return nil, fmt.Errorf("unable to get column types: %w", err)
	}
	columns := make([]Column, len(cols))
	// scales holds the declared scale (`s`) of each DECIMAL(p,s) column for
	// use by renderValue; 0 (and unused) for non-DECIMAL columns.
	scales := make([]int64, len(cols))
	for i, name := range cols {
		dbType := colTypes[i].DatabaseTypeName()
		columns[i] = Column{Name: name, Type: dbType}
		if _, scale, ok := colTypes[i].DecimalSize(); ok {
			scales[i] = scale
		} else if s, ok := decimalScaleFromType(dbType); ok {
			// duckdb-go-bindings does not implement DecimalSize() on its
			// ColumnType, so parse the scale out of the type-name string
			// (e.g., "DECIMAL(18,2)").
			scales[i] = s
		}
	}

	rawValues := make([]any, len(cols))
	scanTargets := make([]any, len(cols))
	for i := range rawValues {
		scanTargets[i] = &rawValues[i]
	}

	out := make([]orderedmap.Row, 0)
	truncated := false
	for rows.Next() {
		if opts.MaxRows > 0 && len(out) >= opts.MaxRows {
			truncated = true
			// Drain remaining rows so the server-side cursor closes promptly.
			for rows.Next() {
			}
			break
		}
		if err := rows.Scan(scanTargets...); err != nil {
			return nil, fmt.Errorf("unable to scan row: %w", err)
		}
		row := orderedmap.Row{}
		for i, name := range cols {
			row.Add(name, renderValue(rawValues[i], columns[i].Type, scales[i]))
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating rows: %w", err)
	}
	return &QueryResult{Columns: columns, Rows: out, Truncated: truncated}, nil
}

// needsReAttach decides whether an error from a user query suggests the
// ATTACH state was lost on the underlying conn and a re-bootstrap should be
// attempted. Conservative: only triggers on patterns that are deterministic
// signals of lost ATTACH (DuckDB's catalog-missing message naming the alias
// or driver.ErrBadConn) or known Quack-side network failures. Generic SQL
// errors (syntax errors, type errors, etc.) bypass the retry path so the
// caller sees the original failure verbatim.
func needsReAttach(err error, alias string) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, driver.ErrBadConn) {
		return true
	}
	msg := err.Error()
	// DuckDB's message when an attached catalog disappears mentions both the
	// alias and "does not exist". Match conservatively on both substrings to
	// avoid colliding with unrelated "does not exist" errors (e.g., table
	// missing inside an existing catalog).
	if strings.Contains(msg, alias) && strings.Contains(msg, "does not exist") {
		return true
	}
	// duckdb-go-bindings surfaces "Invalid Input Error: Invalid connection
	// id" when the underlying TCP conn to the Quack server has gone bad
	// (server restart, network drop). It's the most common signal we see
	// in practice after a Quack server restart, before the *sql.DB pool
	// has marked the conn bad.
	if strings.Contains(msg, "Invalid connection id") {
		return true
	}
	// Other Quack HTTP-side failures the duckdb-go driver surfaces as
	// plain errors.
	for _, sig := range []string{
		"Failed to send message",
		"Failed to receive message",
		"Could not connect",
		"Connection reset",
	} {
		if strings.Contains(msg, sig) {
			return true
		}
	}
	return false
}

// reAttachOnConn re-runs the per-connection bootstrap (LOAD quack +
// CREATE OR REPLACE SECRET + DETACH + ATTACH) on a freshly-pinned conn so
// the subsequent query retry can reference the remote catalog again.
//
// The DETACH is best-effort: on a brand-new conn the alias does not exist
// yet, so DETACH errors; on a conn where ATTACH is still active, DETACH
// clears it before we re-ATTACH. Swallowing the error keeps the function
// idempotent.
//
// INSTALL is *not* re-run — the Quack extension binary is cached process-wide
// after the first install, and reinstalling on every reconnect would slow
// recovery significantly.
func (s *Source) reAttachOnConn(ctx context.Context, conn *sql.Conn) error {
	if _, err := conn.ExecContext(ctx, "LOAD quack"); err != nil {
		return fmt.Errorf("LOAD quack: %w", err)
	}
	if s.Quack.UseSecret != nil && *s.Quack.UseSecret {
		secretName := "toolbox_" + sanitizeIdentifier(s.Name)
		stmt := fmt.Sprintf(
			"CREATE OR REPLACE SECRET %s (TYPE quack, TOKEN '%s')",
			secretName, s.Token,
		)
		if _, err := conn.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("CREATE OR REPLACE SECRET: %w", err)
		}
	}
	_, _ = conn.ExecContext(ctx, fmt.Sprintf("DETACH %s", s.AttachAlias))
	opts := "TYPE quack"
	if s.DisableSSL {
		opts += ", DISABLE_SSL true"
	}
	stmt := fmt.Sprintf("ATTACH '%s' AS %s (%s)", s.URI, s.AttachAlias, opts)
	if _, err := conn.ExecContext(ctx, stmt); err != nil {
		return fmt.Errorf("ATTACH: %w", err)
	}
	return nil
}

// renderValue applies the per-column-type rules from spec §7 to one cell.
// The duckdb-go driver already decodes LIST/STRUCT/MAP into native Go
// []any and map[string]any values that encoding/json handles correctly, so
// only the precision-sensitive (DECIMAL) and rejected-by-default (BLOB)
// types need explicit handling here. The scale parameter is the declared
// scale of a DECIMAL(p,s) column (0 and unused for other types).
func renderValue(v any, dbType string, scale int64) any {
	if v == nil {
		return nil
	}
	// DECIMAL(p,s): render as string to preserve precision. The duckdb-go
	// driver decodes DECIMAL into a value whose String() method returns the
	// canonical decimal representation but strips trailing zeros — so a
	// DECIMAL(18,2) value of 1370.50 comes back as "1370.5". Pad to the
	// column's declared scale so the response shows the full scale. Guard
	// on the column type so we don't accidentally stringify other Stringer
	// types such as time.Time, which JSON marshals correctly on its own.
	if strings.HasPrefix(dbType, "DECIMAL") {
		if s, ok := v.(fmt.Stringer); ok {
			return formatDecimal(s.String(), scale)
		}
	}
	// BLOB: rejected by default. Replace the value with a sentinel that
	// preserves the row layout but signals the rejection and the byte
	// count. A future opt-in (e.g., result.allow_blob: true) can return
	// the raw []byte (which encoding/json renders as base64).
	if dbType == "BLOB" {
		if b, ok := v.([]byte); ok {
			return fmt.Sprintf("<blob: %d bytes>", len(b))
		}
	}
	return v
}

// decimalScaleFromType extracts the scale from a DuckDB DECIMAL type-name
// string like "DECIMAL(18,2)". Returns ok=false for any other type. This is
// a fallback for drivers (notably duckdb-go-bindings) that don't implement
// sql.ColumnType.DecimalSize().
func decimalScaleFromType(dbType string) (int64, bool) {
	m := decimalTypePattern.FindStringSubmatch(dbType)
	if m == nil {
		return 0, false
	}
	var scale int64
	for _, c := range m[1] {
		scale = scale*10 + int64(c-'0')
	}
	return scale, true
}

// formatDecimal pads the trimmed string form of a DECIMAL value with
// trailing zeros so it shows the column's declared scale. duckdb-go's
// Decimal.String() drops trailing zeros (1370.50 -> "1370.5", 1000.00 ->
// "1000"); this restores them so the JSON response matches the column's
// declared shape.
//
// Inputs are expected to be the output of duckdb-go's Decimal.String(): a
// signed decimal literal, optionally with a single '.' separator, never in
// scientific notation. scale<=0 returns the value unchanged.
func formatDecimal(s string, scale int64) string {
	if scale <= 0 || s == "" {
		return s
	}
	dot := strings.IndexByte(s, '.')
	if dot < 0 {
		return s + "." + strings.Repeat("0", int(scale))
	}
	have := int64(len(s) - dot - 1)
	if have >= scale {
		return s
	}
	return s + strings.Repeat("0", int(scale-have))
}

// validateIdentifier accepts ASCII identifiers safe to interpolate into SQL
// (used for the ATTACH alias and the SECRET name suffix).
func validateIdentifier(s string) error {
	if s == "" {
		return fmt.Errorf("identifier must not be empty")
	}
	for i, r := range s {
		ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '_' ||
			(i > 0 && r >= '0' && r <= '9')
		if !ok {
			return fmt.Errorf("identifier %q contains invalid character %q at position %d", s, r, i)
		}
	}
	return nil
}

// sanitizeIdentifier maps a source name to a SQL-safe suffix used in the
// scoped secret name. Non-identifier characters become underscores. Source
// names are developer-supplied so this is forgiving rather than strict.
func sanitizeIdentifier(s string) string {
	var b strings.Builder
	for i, r := range s {
		ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '_' ||
			(i > 0 && r >= '0' && r <= '9')
		if ok {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	if b.Len() == 0 {
		return "src"
	}
	return b.String()
}
