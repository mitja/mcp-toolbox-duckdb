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

// Package duckdblistschemas implements "duckdb-list-schemas", a
// parameterless metadata tool that returns the schemas the remote DuckDB
// (reached via the source's Quack URI) exposes. The query is pushed
// directly to the remote via `quack_query()` because the client-side
// view of information_schema does not enumerate the ATTACHed catalog's
// schemas beyond what ATTACH's initial probe populated.
package duckdblistschemas

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

const resourceType string = "duckdb-list-schemas"

// remoteStatement runs on the remote DuckDB server through quack_query.
// current_database() resolves to the server-side database name so we
// only return schemas in the database the source is attached to.
const remoteStatement = `SELECT schema_name FROM information_schema.schemata WHERE catalog_name = current_database() ORDER BY schema_name`

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

	allParameters, paramManifest, err := parameters.ProcessParameters(nil, nil)
	if err != nil {
		return nil, err
	}

	return Tool{
		Config:        cfg,
		AllParams:     allParameters,
		manifest:      tools.Manifest{Description: cfg.Description, Parameters: paramManifest, AuthRequired: cfg.AuthRequired},
		statementHash: duckdbmeta.StatementHash(remoteStatement),
	}, nil
}

var _ tools.Tool = Tool{}

type Tool struct {
	Config
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
		return src.QuackQuery(ctx, remoteStatement, opts)
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
