package llm

import "strings"

// Vision capability resolution.
//
// Provider /models catalogs do not reliably report whether a model accepts
// image input. OpenRouter is the exception — its architecture.input_modalities
// lists "image" for multimodal models — but every other OpenAI-compatible
// catalog (OpenAI, Gemini, Z.AI, local) omits the signal entirely. So vision
// support is resolved from two sources:
//
//  1. An authoritative catalog signal when present (OpenRouter modalities),
//     set at parse time on ModelInfo.SupportsVision.
//  2. A curated capability map (below) applied as a fallback for models the
//     catalog didn't mark — see EnrichModelsVision.
//
// The map is best-effort and necessarily lags new model releases; that is
// exactly why a per-connection override (provider_config vision_mode, auto/on/
// off) exists to correct it without a code change. Keep entries conservative:
// a false negative is recoverable via the override, a false positive feeds
// image bytes to a blind model.
//
// Matching is case-insensitive substring over the model id. OpenRouter
// namespaces ids as "vendor/model" (e.g. "anthropic/claude-sonnet-4"), so a
// substring naturally matches both the bare and namespaced forms.
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
