package llm

import "strings"

// Vision override modes for the per-connection provider_config vision_mode key.
//   - auto: defer to the model's resolved capability (catalog signal + curated map)
//   - on:   force vision on (e.g. a local/custom model id the map can't recognize)
//   - off:  force vision off (a model the map wrongly marks capable)
const (
	VisionModeAuto = "auto"
	VisionModeOn   = "on"
	VisionModeOff  = "off"
)

// isValidVisionMode reports whether mode is a recognized vision override.
func isValidVisionMode(mode string) bool {
	switch mode {
	case VisionModeAuto, VisionModeOn, VisionModeOff:
		return true
	default:
		return false
	}
}

// EffectiveVision resolves whether vision is enabled for a connection given its
// override mode and the model's resolved capability. The override wins: on/off
// are absolute; auto (or any unrecognized value) defers to modelSupportsVision.
func EffectiveVision(visionMode string, modelSupportsVision bool) bool {
	switch strings.ToLower(strings.TrimSpace(visionMode)) {
	case VisionModeOn:
		return true
	case VisionModeOff:
		return false
	default:
		return modelSupportsVision
	}
}

// Vision support uses catalog modalities when available, then a conservative
// case-insensitive model-ID fallback. Connections can override that fallback.
var visionModelSubstrings = []string{
	// OpenAI
	"gpt-4o", "gpt-4.1", "gpt-4-turbo", "gpt-4-vision", "gpt-5", "chatgpt-4o",
	// Anthropic (all Claude 3+ accept images)
	"claude-3", "claude-sonnet-4", "claude-opus-4", "claude-haiku-4", "claude-4",
	// Google Gemini (multimodal from 1.5 onward)
	"gemini-1.5", "gemini-2", "gemini-3",
	// xAI Grok
	"grok-2-vision", "grok-3", "grok-4",
	// Meta Llama vision
	"llama-3.2", "llama-4",
	// Generic vision markers used across vendors
	"pixtral", "llava", "vision", "-vl-", "-vl",
}

// hasImageModality reports whether a catalog's input_modalities list includes
// image input. Used for OpenRouter's architecture.input_modalities.
func hasImageModality(modalities []string) bool {
	for _, m := range modalities {
		if strings.EqualFold(strings.TrimSpace(m), "image") {
			return true
		}
	}
	return false
}

// curatedVisionCapable reports whether the curated map recognizes the model id
// as vision-capable.
func curatedVisionCapable(modelID string) bool {
	id := strings.ToLower(strings.TrimSpace(modelID))
	if id == "" {
		return false
	}
	for _, sub := range visionModelSubstrings {
		if strings.Contains(id, sub) {
			return true
		}
	}
	return false
}

// EnrichModelsVision fills in SupportsVision from the curated map for any model
// the catalog didn't already mark vision-capable. It never downgrades a model
// already flagged true (an authoritative catalog signal wins), so it is safe to
// call repeatedly — at refresh time before persisting, and again at read time
// to protect caches written before the flag existed.
//
// The provider argument is accepted for future per-provider rules; the curated
// map is currently keyed purely by model id (OpenRouter-style namespacing makes
// ids self-identifying), so it is unused today.
func EnrichModelsVision(_ ProviderType, models []ModelInfo) {
	for i := range models {
		if models[i].SupportsVision {
			continue
		}
		if curatedVisionCapable(models[i].ID) {
			models[i].SupportsVision = true
		}
	}
}
