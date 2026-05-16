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

// Package duckdblisttables implements "duckdb-list-tables", a metadata
// tool that lists tables (and/or views) in a fixed schema on the remote
// DuckDB. The schema is baked into the tool config at server start time;
// the agent supplies no parameters. Each table the developer wants
// exposed needs its own tool entry — that's the "curated tools"
// philosophy.
package duckdblisttables

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	yaml "github.com/goccy/go-yaml"
	"github.com/googleapis/mcp-toolbox/internal/embeddingmodels"
	"github.com/googleapis/mcp-toolbox/internal/sources"
	"github.com/googleapis/mcp-toolbox/internal/sources/duckdbquack"
	"github.com/googleapis/mcp-toolbox/internal/tools"
	"github.com/googleapis/mcp-toolbox/internal/tools/duckdb/duckdbmeta"
	"github.com/googleapis/mcp-toolbox/internal/util"
	"github.com/googleapis/mcp-toolbox/internal/util/parameters"
)

const resourceType string = "duckdb-list-tables"

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

type Config struct {
	Name           string                 `yaml:"name" validate:"required"`
	Type           string                 `yaml:"type" validate:"required"`
	Source         string                 `yaml:"source" validate:"required"`
	Description    string                 `yaml:"description" validate:"required"`
	Schema         string                 `yaml:"schema" validate:"required"`
	IncludeTables  *bool                  `yaml:"include_tables"` // default true
	IncludeViews   *bool                  `yaml:"include_views"`  // default true
	AuthRequired   []string               `yaml:"authRequired"`
	Annotations    *tools.ToolAnnotations `yaml:"annotations,omitempty"`
	ScopesRequired []string               `yaml:"scopesRequired"`
}

var _ tools.ToolConfig = Config{}

func (cfg Config) ToolConfigType() string { return resourceType }

func (cfg Config) Initialize(srcs map[string]sources.Source) (tools.Tool, error) {
	rawSrc, ok := srcs[cfg.Source]
	if !ok {
		return nil, fmt.Errorf("tool %q references unknown source %q", cfg.Name, cfg.Source)
	}
	if _, ok := rawSrc.(duckdbmeta.CompatibleSource); !ok {
		return nil, fmt.Errorf("tool %q: source %q (type %q) is not compatible with %s", cfg.Name, cfg.Source, rawSrc.SourceType(), resourceType)
	}
	if err := duckdbmeta.ValidateIdentifier(cfg.Schema); err != nil {
		return nil, fmt.Errorf("tool %q: invalid schema %q: %w", cfg.Name, cfg.Schema, err)
	}

	includeTables := cfg.IncludeTables == nil || *cfg.IncludeTables
	includeViews := cfg.IncludeViews == nil || *cfg.IncludeViews
	if !includeTables && !includeViews {
		return nil, fmt.Errorf("tool %q: at least one of include_tables, include_views must be true", cfg.Name)
	}

	// Build a remote-side query against the server's information_schema.
	// The schema value is interpolated as a string literal; the validator
	// above already restricts it to a conservative identifier subset, so
	// quote-escaping is belt-and-suspenders rather than load-bearing.
	schemaLit := strings.ReplaceAll(cfg.Schema, "'", "''")
	kinds := []string{}
	if includeTables {
		kinds = append(kinds, "'BASE TABLE'")
	}
	if includeViews {
		kinds = append(kinds, "'VIEW'")
	}
	remoteStmt := fmt.Sprintf(
		"SELECT table_name, table_type FROM information_schema.tables "+
			"WHERE table_schema = '%s' AND table_type IN (%s) ORDER BY table_name",
		schemaLit, strings.Join(kinds, ", "),
	)

	allParameters, paramManifest, err := parameters.ProcessParameters(nil, nil)
	if err != nil {
		return nil, err
	}

	return Tool{
		Config:        cfg,
		remoteStmt:    remoteStmt,
		AllParams:     allParameters,
		manifest:      tools.Manifest{Description: cfg.Description, Parameters: paramManifest, AuthRequired: cfg.AuthRequired},
		statementHash: duckdbmeta.StatementHash(remoteStmt),
	}, nil
}

var _ tools.Tool = Tool{}

type Tool struct {
	Config
	remoteStmt    string
	AllParams     parameters.Parameters `yaml:"allParams"`
	manifest      tools.Manifest
	statementHash string
}

func (t Tool) Invoke(ctx context.Context, mgr tools.SourceProvider, _ parameters.ParamValues, _ tools.AccessToken) (any, util.ToolboxError) {
	src, err := tools.GetCompatibleSource[duckdbmeta.CompatibleSource](mgr, t.Source, t.Name, t.Type)
	if err != nil {
		return nil, util.NewClientServerError("source used is not compatible with the tool", http.StatusInternalServerError, err)
	}
	resp, runErr := duckdbmeta.Invoke(ctx, src, t.Source, t.statementHash, func(ctx context.Context, opts duckdbquack.QueryOptions) (*duckdbquack.QueryResult, error) {
		return src.QuackQuery(ctx, t.remoteStmt, opts)
	})
	if runErr != nil {
		return nil, util.ProcessGeneralError(runErr)
	}
	return resp, nil
}

func (t Tool) EmbedParams(ctx context.Context, paramValues parameters.ParamValues, embeddingModelsMap map[string]embeddingmodels.EmbeddingModel) (parameters.ParamValues, error) {
	return parameters.EmbedParams(ctx, t.AllParams, paramValues, embeddingModelsMap, nil)
}

func (t Tool) Manifest() tools.Manifest  { return t.manifest }
func (t Tool) GetName() string           { return t.Name }
func (t Tool) GetDescription() string    { return t.Description }
func (t Tool) GetAuthRequired() []string { return t.AuthRequired }
func (t Tool) GetAnnotations() *tools.ToolAnnotations {
	return tools.GetAnnotationsOrDefault(t.Annotations, tools.NewReadOnlyAnnotations)
}
func (t Tool) Authorized(verified []string) bool    { return tools.IsAuthorized(t.AuthRequired, verified) }
func (t Tool) ToConfig() tools.ToolConfig           { return t.Config }
func (t Tool) GetParameters() parameters.Parameters { return t.AllParams }
func (t Tool) GetScopesRequired() []string          { return t.ScopesRequired }
func (t Tool) GetAuthTokenHeaderName(_ tools.SourceProvider) (string, error) {
	return "Authorization", nil
}
func (t Tool) RequiresClientAuthorization(_ tools.SourceProvider) (bool, error) {
	return false, nil
}
