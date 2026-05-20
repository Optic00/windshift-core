package llm

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestJSONSchemaToGBNFEscapesEnumStrings(t *testing.T) {
	schema := json.RawMessage(`{
		"type": "string",
		"enum": ["a\"b\\c"]
	}`)

	grammar, err := JSONSchemaToGBNF(schema)
	if err != nil {
		t.Fatalf("JSONSchemaToGBNF returned error: %v", err)
	}

	if strings.Contains(grammar, `"a"b\c"`) {
		t.Fatalf("grammar contains unescaped enum literal: %s", grammar)
	}
	if !strings.Contains(grammar, `a\\\"b`) || !strings.Contains(grammar, `\\\\c`) {
		t.Fatalf("grammar does not contain escaped enum literal: %s", grammar)
	}
}

func TestJSONSchemaToGBNFAllowsOptionalObjectProperties(t *testing.T) {
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {
			"required_field": { "type": "string" },
			"optional_field": { "type": "string" }
		},
		"required": ["required_field"],
		"additionalProperties": false
	}`)

	grammar, err := JSONSchemaToGBNF(schema)
	if err != nil {
		t.Fatalf("JSONSchemaToGBNF returned error: %v", err)
	}

	if !strings.Contains(grammar, `"\"required_field\"" ws ":" ws root_required_field`) {
		t.Fatalf("grammar does not allow required-only object: %s", grammar)
	}
	if !strings.Contains(grammar, `"\"optional_field\"" ws ":" ws root_optional_field`) ||
		!strings.Contains(grammar, `ws "," ws "\"required_field\""`) {
		t.Fatalf("grammar does not allow optional property object: %s", grammar)
	}
}
