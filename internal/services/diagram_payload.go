package services

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const (
	DiagramKindMermaid    = "mermaid"
	DiagramKindExcalidraw = "excalidraw"

	MaxDiagramPayloadBytes = 5 << 20
	MaxDiagramElements     = 10_000
	MaxDiagramFiles        = 1_000
	MaxMermaidSourceBytes  = 256 << 10
)

var (
	ErrDiagramPayloadRequired = errors.New("provide either mermaid or excalidraw")
	ErrDiagramPayloadConflict = errors.New("provide either mermaid or excalidraw, not both")
	ErrDiagramPayloadInvalid  = errors.New("invalid diagram payload")
	ErrDiagramPayloadTooLarge = errors.New("diagram payload exceeds limits")
)

// BuildDiagramPayload validates mutually exclusive Mermaid/Excalidraw input
// and returns the canonical JSON stored by both work-item and Page diagrams.
// Mermaid remains a one-shot seed that the browser converts on first edit.
func BuildDiagramPayload(mermaid string, excalidraw json.RawMessage) (data, kind string, err error) {
	mermaid = strings.TrimSpace(mermaid)
	mermaidSet := mermaid != ""
	excalidraw = bytes.TrimSpace(excalidraw)
	excalidrawSet := len(excalidraw) > 0 && !bytes.Equal(excalidraw, []byte("null"))
	switch {
	case mermaidSet && excalidrawSet:
		return "", "", ErrDiagramPayloadConflict
	case mermaidSet:
		if len(mermaid) > MaxMermaidSourceBytes {
			return "", "", ErrDiagramPayloadTooLarge
		}
		wrapper, marshalErr := json.Marshal(map[string]string{
			"type":   DiagramKindMermaid,
			"source": mermaid,
		})
		if marshalErr != nil {
			return "", "", fmt.Errorf("encode mermaid wrapper: %w", marshalErr)
		}
		return string(wrapper), DiagramKindMermaid, nil
	case excalidrawSet:
		if err := ValidateExcalidrawScene(excalidraw); err != nil {
			return "", "", err
		}
		return string(excalidraw), DiagramKindExcalidraw, nil
	default:
		return "", "", ErrDiagramPayloadRequired
	}
}

// DetectDiagramKind classifies a stored payload without treating malformed
// JSON as Mermaid. Call ValidateStoredDiagramPayload when validity matters.
func DetectDiagramKind(data []byte) string {
	var seed struct {
		Type   string `json:"type"`
		Source string `json:"source"`
	}
	if json.Unmarshal(data, &seed) == nil &&
		seed.Type == DiagramKindMermaid &&
		strings.TrimSpace(seed.Source) != "" {
		return DiagramKindMermaid
	}
	return DiagramKindExcalidraw
}

// ValidateStoredDiagramPayload accepts either the canonical Mermaid seed or
// a structurally valid Excalidraw scene and returns its kind.
func ValidateStoredDiagramPayload(data []byte) (string, error) {
	if len(data) > MaxDiagramPayloadBytes {
		return "", ErrDiagramPayloadTooLarge
	}
	if DetectDiagramKind(data) == DiagramKindMermaid {
		var seed struct {
			Type   string `json:"type"`
			Source string `json:"source"`
		}
		if err := json.Unmarshal(data, &seed); err != nil {
			return "", fmt.Errorf("%w: malformed Mermaid seed", ErrDiagramPayloadInvalid)
		}
		if len(strings.TrimSpace(seed.Source)) > MaxMermaidSourceBytes {
			return "", ErrDiagramPayloadTooLarge
		}
		return DiagramKindMermaid, nil
	}
	if err := ValidateExcalidrawScene(data); err != nil {
		return "", err
	}
	return DiagramKindExcalidraw, nil
}

// ValidateExcalidrawScene enforces the stable scene envelope needed by the
// renderer/editor and bounds the collections that can be expanded in memory.
// Additional top-level and element fields are preserved for forward
// compatibility with Excalidraw.
func ValidateExcalidrawScene(data []byte) error {
	if len(data) == 0 {
		return fmt.Errorf("%w: scene is required", ErrDiagramPayloadInvalid)
	}
	if len(data) > MaxDiagramPayloadBytes {
		return ErrDiagramPayloadTooLarge
	}
	var scene map[string]json.RawMessage
	if err := json.Unmarshal(data, &scene); err != nil {
		return fmt.Errorf("%w: scene must be a JSON object", ErrDiagramPayloadInvalid)
	}
	rawElements, ok := scene["elements"]
	if !ok {
		return fmt.Errorf("%w: scene.elements is required", ErrDiagramPayloadInvalid)
	}
	var elements []json.RawMessage
	if err := json.Unmarshal(rawElements, &elements); err != nil {
		return fmt.Errorf("%w: scene.elements must be an array", ErrDiagramPayloadInvalid)
	}
	if len(elements) > MaxDiagramElements {
		return ErrDiagramPayloadTooLarge
	}
	for i, raw := range elements {
		var element struct {
			ID   string `json:"id"`
			Type string `json:"type"`
		}
		if err := json.Unmarshal(raw, &element); err != nil {
			return fmt.Errorf("%w: scene.elements[%d] must be an object", ErrDiagramPayloadInvalid, i)
		}
		if strings.TrimSpace(element.ID) == "" || strings.TrimSpace(element.Type) == "" {
			return fmt.Errorf("%w: scene.elements[%d] requires id and type", ErrDiagramPayloadInvalid, i)
		}
	}
	if err := validateOptionalJSONObject(scene, "appState"); err != nil {
		return err
	}
	if rawFiles, ok := scene["files"]; ok && !bytes.Equal(bytes.TrimSpace(rawFiles), []byte("null")) {
		var files map[string]json.RawMessage
		if err := json.Unmarshal(rawFiles, &files); err != nil {
			return fmt.Errorf("%w: scene.files must be an object", ErrDiagramPayloadInvalid)
		}
		if len(files) > MaxDiagramFiles {
			return ErrDiagramPayloadTooLarge
		}
	}
	return nil
}

func validateOptionalJSONObject(scene map[string]json.RawMessage, field string) error {
	raw, ok := scene[field]
	if !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return fmt.Errorf("%w: scene.%s must be an object", ErrDiagramPayloadInvalid, field)
	}
	return nil
}
