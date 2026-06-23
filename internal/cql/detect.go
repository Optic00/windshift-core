package cql

// LooksLikeQuery reports whether s should be interpreted as a structured CQL
// query rather than a free-text search term.
//
// It is used by surfaces that accept a single user-supplied string and want to
// transparently support both modes — e.g. the item search endpoint, where
// `milestone = '0.8.2'` is a filter but `login bug` is a phrase to match.
//
// The string is tokenized and parsed; it is treated as CQL only when it parses
// cleanly AND the resulting AST root is an actual filter node (a comparison,
// IN/NOT IN, IS [NOT] NULL, or a boolean combination of those). A bare
// identifier, literal, function call, or list — all of which the grammar
// accepts as a valid expression — is NOT treated as CQL, so ordinary
// single-word searches fall through to full-text matching.
func LooksLikeQuery(s string) bool {
	tokens, err := NewTokenizer(s).Tokenize()
	if err != nil {
		return false
	}
	ast, err := NewParser(tokens).Parse()
	if err != nil || ast == nil {
		return false
	}
	switch ast.Type {
	case NodeComparison, NodeInExpression, NodeNullCheck, NodeBinaryOp:
		return true
	default:
		return false
	}
}
