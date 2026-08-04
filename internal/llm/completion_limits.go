package llm

import (
	"errors"
	"strings"
	"sync/atomic"
)

const (
	modernCompletionTokenParameter = "max_completion_tokens"
	legacyCompletionTokenParameter = "max_tokens"
	responsesTokenParameter        = "max_output_tokens"
)

// completionTokenNegotiator remembers OpenAI-compatible endpoints that reject
// the modern output-limit field. Fantasy still owns both request encodings;
// this state only selects which of its typed options to use on the next call.
type completionTokenNegotiator struct {
	useLegacy atomic.Bool
}

func rejectsModernCompletionTokenParameter(err error) bool {
	if !errors.Is(err, ErrAPIError) {
		return false
	}
	message := strings.ToLower(err.Error())
	if (!strings.Contains(message, "status 400") && !strings.Contains(message, "status 422")) ||
		!strings.Contains(message, modernCompletionTokenParameter) {
		return false
	}
	for _, marker := range []string{
		"unsupported parameter",
		"not supported",
		"unknown parameter",
		"unknown field",
		"unrecognized request argument",
		"extra inputs are not permitted",
		"invalid parameter",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

func isCompletionTokenLimitError(err error) bool {
	if !errors.Is(err, ErrAPIError) {
		return false
	}
	message := strings.ToLower(err.Error())
	if !strings.Contains(message, "status 400") && !strings.Contains(message, "status 422") {
		return false
	}
	hasTokenLimit := strings.Contains(message, modernCompletionTokenParameter) ||
		strings.Contains(message, legacyCompletionTokenParameter) ||
		strings.Contains(message, responsesTokenParameter) ||
		strings.Contains(message, "model output limit")
	return hasTokenLimit && (strings.Contains(message, "reached") || strings.Contains(message, "exceeded"))
}
