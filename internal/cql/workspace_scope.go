package cql

import (
	"errors"
	"strconv"
	"strings"
)

// ErrWorkspaceScopeRequired means a query is not bounded to a finite set of
// workspaces on every boolean branch.
var ErrWorkspaceScopeRequired = errors.New("workspace scope required")

// WorkspaceScopeField identifies the workspace column used by a scope.
type WorkspaceScopeField string

const (
	WorkspaceScopeName WorkspaceScopeField = "name"
	WorkspaceScopeKey  WorkspaceScopeField = "key"
	WorkspaceScopeID   WorkspaceScopeField = "id"
	// WorkspaceScopeNameOrKey is the generic workspace field, which accepts either.
	WorkspaceScopeNameOrKey WorkspaceScopeField = "name_or_key"
)

// WorkspaceScopeReference is one positive workspace value that bounds a query.
type WorkspaceScopeReference struct {
	Field WorkspaceScopeField
	Value string
}

// ExtractWorkspaceScope returns the finite workspace references that bound a
// query. Every OR branch must carry its own scope; an AND needs at least one.
func ExtractWorkspaceScope(query string) ([]WorkspaceScopeReference, error) {
	tokens, err := NewTokenizer(strings.TrimSpace(query)).Tokenize()
	if err != nil {
		return nil, err
	}
	ast, err := NewParser(tokens).Parse()
	if err != nil {
		return nil, err
	}

	references, scoped := extractWorkspaceScopeNode(ast)
	if !scoped || len(references) == 0 {
		return nil, ErrWorkspaceScopeRequired
	}

	seen := make(map[string]struct{}, len(references))
	unique := make([]WorkspaceScopeReference, 0, len(references))
	for _, reference := range references {
		key := string(reference.Field) + "\x00" + strings.ToLower(reference.Value)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		unique = append(unique, reference)
	}
	return unique, nil
}

func extractWorkspaceScopeNode(node *ASTNode) ([]WorkspaceScopeReference, bool) {
	if node == nil {
		return nil, false
	}

	switch node.Type {
	case NodeBinaryOp:
		switch strings.ToUpper(node.Operator) {
		case "AND":
			left, leftScoped := extractWorkspaceScopeNode(node.Left)
			right, rightScoped := extractWorkspaceScopeNode(node.Right)
			if !leftScoped && !rightScoped {
				return nil, false
			}
			return append(left, right...), true
		case "OR":
			left, leftScoped := extractWorkspaceScopeNode(node.Left)
			right, rightScoped := extractWorkspaceScopeNode(node.Right)
			if !leftScoped || !rightScoped {
				return nil, false
			}
			return append(left, right...), true
		default:
			return nil, false
		}
	case NodeComparison:
		if node.Operator != "=" || node.Left == nil || node.Left.Type != NodeIdentifier {
			return nil, false
		}
		field, ok := workspaceScopeField(node.Left.Value)
		if !ok {
			return nil, false
		}
		reference, ok := workspaceScopeReference(field, node.Right)
		if !ok {
			return nil, false
		}
		return []WorkspaceScopeReference{reference}, true
	case NodeInExpression:
		if !strings.EqualFold(node.Operator, "IN") || node.Field == nil || node.Field.Type != NodeIdentifier || node.Values == nil {
			return nil, false
		}
		field, ok := workspaceScopeField(node.Field.Value)
		if !ok || len(node.Values.Arguments) == 0 {
			return nil, false
		}
		references := make([]WorkspaceScopeReference, 0, len(node.Values.Arguments))
		for _, value := range node.Values.Arguments {
			reference, valid := workspaceScopeReference(field, value)
			if !valid {
				return nil, false
			}
			references = append(references, reference)
		}
		return references, true
	default:
		return nil, false
	}
}

func workspaceScopeField(field string) (WorkspaceScopeField, bool) {
	switch strings.ToLower(field) {
	case "workspace":
		return WorkspaceScopeNameOrKey, true
	case "workspacekey":
		return WorkspaceScopeKey, true
	case "workspaceid", "workspace_id":
		return WorkspaceScopeID, true
	default:
		return "", false
	}
}

func workspaceScopeReference(field WorkspaceScopeField, node *ASTNode) (WorkspaceScopeReference, bool) {
	if node == nil {
		return WorkspaceScopeReference{}, false
	}

	switch field {
	case WorkspaceScopeName, WorkspaceScopeKey, WorkspaceScopeNameOrKey:
		if node.Type == NodeIdentifier {
			if strings.TrimSpace(node.Value) == "" {
				return WorkspaceScopeReference{}, false
			}
			return WorkspaceScopeReference{Field: field, Value: node.Value}, true
		}
		if node.Type != NodeLiteral || (node.DataType != STRING && node.DataType != IDENTIFIER) || strings.TrimSpace(node.Value) == "" {
			return WorkspaceScopeReference{}, false
		}
		return WorkspaceScopeReference{Field: field, Value: node.Value}, true
	case WorkspaceScopeID:
		if node.Type != NodeLiteral || node.DataType != NUMBER {
			return WorkspaceScopeReference{}, false
		}
		id, err := strconv.Atoi(node.Value)
		if err != nil || id <= 0 {
			return WorkspaceScopeReference{}, false
		}
		return WorkspaceScopeReference{Field: field, Value: strconv.Itoa(id)}, true
	default:
		return WorkspaceScopeReference{}, false
	}
}
