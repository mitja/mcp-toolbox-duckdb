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

// Package duckdbexecutesql implements "duckdb-execute-sql", a development
// tool that accepts agent-supplied SQL and runs it against a duckdb-quack
// source. The tool is gated behind a per-entry `enabled: true` YAML field:
// the registration refuses to start the server unless that field is
// explicitly true, and an INFO-level "this is enabled" line is logged at
// startup so the operator sees it on every boot.
//
// **This tool is not intended for production agent toolsets.** Spec §3
// explicitly classifies a "let the LLM run arbitrary DuckDB SQL" surface
// as a non-goal: SQL from an untrusted source should be treated like
// arbitrary code. The same statement validator that duckdb-sql uses at
// config-load runs at every invocation here, so destructive verbs are
// rejected before they reach the database — but the validator is defense
// in depth, not a SQL sandbox. The intended use is local development and
// human-in-the-loop debugging only.
package duckdbexecutesql

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	yaml "github.com/goccy/go-yaml"
	"github.com/googleapis/mcp-toolbox/internal/embeddingmodels"
	"github.com/googleapis/mcp-toolbox/internal/sources"
	"github.com/googleapis/mcp-toolbox/internal/sources/duckdbquack"
	"github.com/googleapis/mcp-toolbox/internal/tools"
	"github.com/googleapis/mcp-toolbox/internal/tools/duckdb/duckdbmeta"
	"github.com/googleapis/mcp-toolbox/internal/tools/duckdb/duckdbsql"
	"github.com/googleapis/mcp-toolbox/internal/util"
	"github.com/googleapis/mcp-toolbox/internal/util/parameters"
)

const resourceType string = "duckdb-execute-sql"

func init() {
	if !tools.Register(resourceType, newConfig) {
		panic(fmt.Sprintf("tool type %q already registered", resourceType))
	}
}

func newConfig(ctx context.Context, name string, decoder *yaml.Decoder) (tools.ToolConfig, error) {
	actual := Config{Name: name}
	if err := decoder.DecodeContext(ctx, &actual); err != nil {
		return nil, err
	}
	return actual, nil
}

// Config is the YAML-decoded configuration for a duckdb-execute-sql tool.
//
// Enabled is a *bool rather than bool so that a missing field is
// distinguishable from an explicit `enabled: false`. Both refuse to
// register — the tool requires an explicit `enabled: true`.
type Config struct {
	Name           string                 `yaml:"name" validate:"required"`
	Type           string                 `yaml:"type" validate:"required"`
	Source         string                 `yaml:"source" validate:"required"`
	Description    string                 `yaml:"description" validate:"required"`
	Enabled        *bool                  `yaml:"enabled"`
	AuthRequired   []string               `yaml:"authRequired"`
	Annotations    *tools.ToolAnnotations `yaml:"annotations,omitempty"`
	ScopesRequired []string               `yaml:"scopesRequired"`
}

var _ tools.ToolConfig = Config{}

func (cfg Config) ToolConfigType() string { return resourceType }

func (cfg Config) Initialize(srcs map[string]sources.Source) (tools.Tool, error) {
	if cfg.Enabled == nil || !*cfg.Enabled {
		return nil, fmt.Errorf(
			"tool %q (type %s) requires explicit `enabled: true` in tools.yaml. "+
				"This tool exposes an agent-supplied SQL surface and is intended "+
				"for local development and human-in-the-loop debugging only; "+
				"do not enable it for production agent toolsets",
			cfg.Name, resourceType,
		)
	}

	rawSrc, ok := srcs[cfg.Source]
	if !ok {
		return nil, fmt.Errorf("tool %q references unknown source %q", cfg.Name, cfg.Source)
	}
	if _, ok := rawSrc.(duckdbmeta.CompatibleSource); !ok {
		return nil, fmt.Errorf("tool %q: source %q (type %q) is not compatible with %s", cfg.Name, cfg.Source, rawSrc.SourceType(), resourceType)
	}

	// Startup warning so the operator always sees this on every boot,
	// not just in the YAML.
	slog.Warn(
		"duckdb-execute-sql is enabled. This tool exposes an agent-supplied SQL "+
			"surface and is intended for local development and human-in-the-loop "+
			"debugging only; do not enable it for production agent toolsets.",
		"tool", cfg.Name,
		"source", cfg.Source,
	)

	// Programmatically add the agent-facing `sql` parameter. The tool
	// type, not the YAML, owns this parameter — there is nothing for the
	// developer to configure here.
	sqlParam := parameters.NewStringParameter("sql", "The SQL statement to execute. Must be a single read-only SELECT/WITH/EXPLAIN/DESCRIBE/SHOW.")
	params := parameters.Parameters{sqlParam}

	annotations := tools.GetAnnotationsOrDefault(cfg.Annotations, tools.NewDestructiveAnnotations)
	mcpManifest := tools.GetMcpManifest(cfg.Name, cfg.Description, cfg.AuthRequired, params, annotations)

	return Tool{
		Config:      cfg,
		Parameters:  params,
		manifest:    tools.Manifest{Description: cfg.Description, Parameters: params.Manifest(), AuthRequired: cfg.AuthRequired},
		mcpManifest: mcpManifest,
	}, nil
}

var _ tools.Tool = Tool{}

type Tool struct {
	Config
	Parameters  parameters.Parameters `yaml:"parameters"`
	manifest    tools.Manifest
	mcpManifest tools.McpManifest
}

func (t Tool) Invoke(ctx context.Context, mgr tools.SourceProvider, params parameters.ParamValues, _ tools.AccessToken) (any, util.ToolboxError) {
	src, err := tools.GetCompatibleSource[duckdbmeta.CompatibleSource](mgr, t.Source, t.Name, t.Type)
	if err != nil {
		return nil, util.NewClientServerError("source used is not compatible with the tool", http.StatusInternalServerError, err)
	}

	sql, ok := params.AsMap()["sql"].(string)
	if !ok {
		return nil, util.NewAgentError("parameter 'sql' is required and must be a string", nil)
	}

	policy := src.EffectivePolicy()
	allowed := policy.AllowedStatementKinds
	if len(allowed) == 0 {
		allowed = duckdbsql.DefaultAllowedStatementKinds
	}
	if err := duckdbsql.ValidateStatement(sql, allowed, policy.ForbiddenPatterns); err != nil {
		return nil, util.NewAgentError(fmt.Sprintf("statement rejected by policy: %v", err), err)
	}

	hash := duckdbmeta.StatementHash(sql)
	resp, runErr := duckdbmeta.Invoke(ctx, src, t.Source, hash, func(ctx context.Context, opts duckdbquack.QueryOptions) (*duckdbquack.QueryResult, error) {
		return src.RunSQL(ctx, sql, nil, opts)
	})
	if runErr != nil {
		return nil, util.ProcessGeneralError(runErr)
	}
	return resp, nil
}

func (t Tool) EmbedParams(ctx context.Context, paramValues parameters.ParamValues, embeddingModelsMap map[string]embeddingmodels.EmbeddingModel) (parameters.ParamValues, error) {
	return parameters.EmbedParams(ctx, t.Parameters, paramValues, embeddingModelsMap, nil)
}

func (t Tool) Manifest() tools.Manifest             { return t.manifest }
func (t Tool) McpManifest() tools.McpManifest       { return t.mcpManifest }
func (t Tool) Authorized(verified []string) bool    { return tools.IsAuthorized(t.AuthRequired, verified) }
func (t Tool) ToConfig() tools.ToolConfig           { return t.Config }
func (t Tool) GetParameters() parameters.Parameters { return t.Parameters }
func (t Tool) GetScopesRequired() []string          { return t.ScopesRequired }
func (t Tool) GetAuthTokenHeaderName(_ tools.SourceProvider) (string, error) {
	return "Authorization", nil
}
func (t Tool) RequiresClientAuthorization(_ tools.SourceProvider) (bool, error) {
	return false, nil
}
