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

// Package duckdbsql implements the "duckdb-sql" MCP Toolbox tool, which runs
// a single curated SQL statement against a duckdb-quack (and, in Phase 2,
// embedded duckdb) source.
//
// The tool validates the statement at config-load time against the source's
// policy (allowed statement kinds, no multi-statement, forbidden tokens) and
// enforces a timeout and row limit at invocation time. Agents only supply
// bound parameter values; the SQL itself is fixed by the developer in
// tools.yaml.
package duckdbsql

import (
	"context"
	"fmt"
	"net/http"

	yaml "github.com/goccy/go-yaml"
	"github.com/googleapis/mcp-toolbox/internal/embeddingmodels"
	"github.com/googleapis/mcp-toolbox/internal/sources"
	"github.com/googleapis/mcp-toolbox/internal/sources/duckdbquack"
	"github.com/googleapis/mcp-toolbox/internal/tools"
	"github.com/googleapis/mcp-toolbox/internal/tools/duckdb/duckdbmeta"
	"github.com/googleapis/mcp-toolbox/internal/util"
	"github.com/googleapis/mcp-toolbox/internal/util/parameters"
)

const resourceType string = "duckdb-sql"

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

// Config is the YAML-decoded configuration for one duckdb-sql tool.
type Config struct {
	Name               string                 `yaml:"name" validate:"required"`
	Type               string                 `yaml:"type" validate:"required"`
	Source             string                 `yaml:"source" validate:"required"`
	Description        string                 `yaml:"description" validate:"required"`
	Statement          string                 `yaml:"statement" validate:"required"`
	AuthRequired       []string               `yaml:"authRequired"`
	Parameters         parameters.Parameters  `yaml:"parameters"`
	TemplateParameters parameters.Parameters  `yaml:"templateParameters"`
	Annotations        *tools.ToolAnnotations `yaml:"annotations,omitempty"`
	ScopesRequired     []string               `yaml:"scopesRequired"`

	// PushDownToRemote, when true, ships the entire statement to the
	// remote Quack server via the quack_query() table function instead of
	// executing it against the locally ATTACHed catalogs. The join (and
	// every other operator) runs on the remote DuckDB next to the data;
	// only the result rows stream back as one scan.
	//
	// Use when a tool's SQL references two or more attached tables and
	// DuckDB would otherwise fail with "Multiple streaming scans … not
	// currently supported". Only available for tools whose source is a
	// duckdb-quack source. Incompatible with bound `parameters:` because
	// quack_query() takes the inner SQL as a string literal and DuckDB
	// does not bind `?` markers inside string literals; template
	// parameters (substituted before the wrap) remain supported.
	PushDownToRemote bool `yaml:"push_down_to_remote"`
}

var _ tools.ToolConfig = Config{}

func (cfg Config) ToolConfigType() string {
	return resourceType
}

func (cfg Config) Initialize(srcs map[string]sources.Source) (tools.Tool, error) {
	rawSrc, ok := srcs[cfg.Source]
	if !ok {
		return nil, fmt.Errorf("tool %q references unknown source %q", cfg.Name, cfg.Source)
	}
	src, ok := rawSrc.(duckdbmeta.CompatibleSource)
	if !ok {
		return nil, fmt.Errorf("tool %q: source %q (type %q) is not compatible with %s", cfg.Name, cfg.Source, rawSrc.SourceType(), resourceType)
	}

	if cfg.PushDownToRemote {
		if _, ok := rawSrc.(*duckdbquack.Source); !ok {
			return nil, fmt.Errorf("tool %q: push_down_to_remote is only supported when the source is a duckdb-quack source (source %q has type %q)", cfg.Name, cfg.Source, rawSrc.SourceType())
		}
		if len(cfg.Parameters) > 0 {
			return nil, fmt.Errorf("tool %q: push_down_to_remote cannot be combined with bound `parameters:` — quack_query() takes the inner SQL as a string literal and DuckDB does not bind `?` markers inside it. Use templateParameters (substituted before the push-down wrap) or remove push_down_to_remote and wrap quack_query(...) manually in the statement", cfg.Name)
		}
	}

	policy := src.EffectivePolicy()
	allowed := policy.AllowedStatementKinds
	if len(allowed) == 0 {
		allowed = DefaultAllowedStatementKinds
	}
	if err := ValidateStatement(cfg.Statement, allowed, policy.ForbiddenPatterns); err != nil {
		return nil, fmt.Errorf("tool %q: statement rejected by policy: %w", cfg.Name, err)
	}

	allParameters, paramManifest, err := parameters.ProcessParameters(cfg.TemplateParameters, cfg.Parameters)
	if err != nil {
		return nil, err
	}

	return Tool{
		Config:        cfg,
		AllParams:     allParameters,
		manifest:      tools.Manifest{Description: cfg.Description, Parameters: paramManifest, AuthRequired: cfg.AuthRequired},
		statementHash: duckdbmeta.StatementHash(cfg.Statement),
	}, nil
}

var _ tools.Tool = Tool{}

// Tool is an initialized duckdb-sql tool: a single read-only SQL statement
// bound to a compatible source, with its policy already resolved.
type Tool struct {
	Config
	AllParams     parameters.Parameters `yaml:"allParams"`
	manifest      tools.Manifest
	statementHash string
}

func (t Tool) Invoke(ctx context.Context, resourceMgr tools.SourceProvider, params parameters.ParamValues, _ tools.AccessToken) (any, util.ToolboxError) {
	source, err := tools.GetCompatibleSource[duckdbmeta.CompatibleSource](resourceMgr, t.Source, t.Name, t.Type)
	if err != nil {
		return nil, util.NewClientServerError("source used is not compatible with the tool", http.StatusInternalServerError, err)
	}

	paramsMap := params.AsMap()
	newStatement, err := parameters.ResolveTemplateParams(t.TemplateParameters, t.Statement, paramsMap)
	if err != nil {
		return nil, util.NewAgentError("unable to extract template params", err)
	}
	newParams, err := parameters.GetParams(t.Parameters, paramsMap)
	if err != nil {
		return nil, util.NewAgentError("unable to extract standard params", err)
	}

	resp, runErr := duckdbmeta.Invoke(ctx, source, t.Source, t.statementHash, func(ctx context.Context, opts duckdbquack.QueryOptions) (*duckdbquack.QueryResult, error) {
		if t.PushDownToRemote {
			// Initialize already rejected bound parameters when this
			// flag is set, so newParams is empty here. The wrap +
			// instrumentation lives inside QuackQuery; the resulting
			// duckdb.query span and reattach path are identical to
			// the RunSQL path.
			return source.QuackQuery(ctx, newStatement, opts)
		}
		return source.RunSQL(ctx, newStatement, newParams.AsSlice(), opts)
	})
	if runErr != nil {
		return nil, util.ProcessGeneralError(runErr)
	}
	return resp, nil
}

func (t Tool) EmbedParams(ctx context.Context, paramValues parameters.ParamValues, embeddingModelsMap map[string]embeddingmodels.EmbeddingModel) (parameters.ParamValues, error) {
	return parameters.EmbedParams(ctx, t.AllParams, paramValues, embeddingModelsMap, nil)
}

func (t Tool) Manifest() tools.Manifest             { return t.manifest }
func (t Tool) Authorized(verified []string) bool    { return tools.IsAuthorized(t.AuthRequired, verified) }
func (t Tool) ToConfig() tools.ToolConfig           { return t.Config }
func (t Tool) GetParameters() parameters.Parameters { return t.AllParams }
func (t Tool) GetScopesRequired() []string          { return t.ScopesRequired }
func (t Tool) GetName() string                      { return t.Name }
func (t Tool) GetDescription() string               { return t.Description }
func (t Tool) GetAuthRequired() []string            { return t.AuthRequired }
func (t Tool) GetAnnotations() *tools.ToolAnnotations {
	return tools.GetAnnotationsOrDefault(t.Annotations, tools.NewReadOnlyAnnotations)
}
func (t Tool) GetAuthTokenHeaderName(_ tools.SourceProvider) (string, error) {
	return "Authorization", nil
}
func (t Tool) RequiresClientAuthorization(_ tools.SourceProvider) (bool, error) {
	return false, nil
}
