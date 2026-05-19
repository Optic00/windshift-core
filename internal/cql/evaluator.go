package cql

import (
	"fmt"
	"strings"
)

// Evaluator evaluates QL queries against SQL database
type Evaluator struct {
	sqlGenerator *SQLGenerator
}

// NewEvaluator creates a new QL evaluator. customFieldMap may be nil; when nil
// the generator falls back to name-based JSON extraction (legacy behavior).
func NewEvaluator(workspaceMap map[string]int, customFieldMap CustomFieldMap, dbDriver string) *Evaluator {
	gen := NewSQLGenerator(workspaceMap, customFieldMap, dbDriver)
	gen.EnableLegacyCustomFieldNameFallback()
	return &Evaluator{sqlGenerator: gen}
}

// evaluateQL tokenizes and parses a CQL query, then generates SQL using the given generator.
// This is the shared pipeline for both item and asset evaluators.
func evaluateQL(cqlQuery string, gen *SQLGenerator) (string, []interface{}, error) { //nolint:gocritic // unnamedResult
	if strings.TrimSpace(cqlQuery) == "" {
		return "", nil, nil
	}

	// Tokenize
	tokenizer := NewTokenizer(cqlQuery)
	tokens, err := tokenizer.Tokenize()
	if err != nil {
		return "", nil, fmt.Errorf("tokenization error: %w", err)
	}

	// Parse
	parser := NewParser(tokens)
	ast, err := parser.Parse()
	if err != nil {
		return "", nil, fmt.Errorf("parse error: %w", err)
	}

	// Generate SQL
	sqlStr, args, err := gen.GenerateSQL(ast)
	if err != nil {
		return "", nil, fmt.Errorf("SQL generation error: %w", err)
	}

	return sqlStr, args, nil
}

// EvaluateToSQL converts a QL query string to SQL WHERE clause
func (e *Evaluator) EvaluateToSQL(cqlQuery string) (string, []interface{}, error) { //nolint:gocritic // unnamedResult
	return evaluateQL(cqlQuery, e.sqlGenerator)
}

// AssetEvaluator evaluates QL queries for assets
type AssetEvaluator struct {
	sqlGenerator *SQLGenerator
	workspaceMap map[string]int // For linkedOf() inner queries against items
}

// NewAssetEvaluator creates a new QL evaluator for assets. Supported call
// shapes are:
//
//	NewAssetEvaluator(setMap, workspaceMap)
//	NewAssetEvaluator(setMap, workspaceMap, assetCustomFieldMap, dbDriver)
//	NewAssetEvaluator(setMap, workspaceMap, assetCustomFieldMap, itemCustomFieldMap, dbDriver)
//
// assetCustomFieldMap covers asset-side custom fields; itemCustomFieldMap is
// passed through to inner item queries spawned by linkedOf() and may be nil if
// those are not expected to filter on item custom fields.
func NewAssetEvaluator(setMap, workspaceMap map[string]int, args ...interface{}) *AssetEvaluator {
	assetCustomFieldMap, itemCustomFieldMap, dbDriver := parseAssetEvaluatorArgs(args...)
	gen := NewAssetSQLGenerator(setMap, assetCustomFieldMap, itemCustomFieldMap, dbDriver)
	gen.EnableLegacyCustomFieldNameFallback()
	return &AssetEvaluator{
		sqlGenerator: gen,
		workspaceMap: workspaceMap,
	}
}

func parseAssetEvaluatorArgs(args ...interface{}) (assetCustomFieldMap, itemCustomFieldMap CustomFieldMap, dbDriver string) {
	switch len(args) {
	case 0:
		return nil, nil, ""
	case 1:
		if s, ok := args[0].(string); ok {
			return nil, nil, s
		}
		return toCustomFieldMap(args[0]), nil, ""
	case 2:
		return toCustomFieldMap(args[0]), nil, stringArg(args[1])
	default:
		return toCustomFieldMap(args[0]), toCustomFieldMap(args[1]), stringArg(args[2])
	}
}

func toCustomFieldMap(v interface{}) CustomFieldMap {
	switch m := v.(type) {
	case nil:
		return nil
	case CustomFieldMap:
		return m
	case map[string]CustomFieldInfo:
		return CustomFieldMap(m)
	case map[string]int:
		out := make(CustomFieldMap, len(m))
		for name, id := range m {
			out[strings.ToLower(name)] = CustomFieldInfo{ID: id, Kind: CFKindScalar}
		}
		return out
	default:
		return nil
	}
}

func stringArg(v interface{}) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// EvaluateToSQL converts a QL query string to SQL WHERE clause for assets
func (e *AssetEvaluator) EvaluateToSQL(cqlQuery string) (string, []interface{}, error) { //nolint:gocritic // unnamedResult
	// Inject workspace map for linkedOf() inner queries
	e.sqlGenerator.workspaceMap = e.workspaceMap
	return evaluateQL(cqlQuery, e.sqlGenerator)
}
