package restapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

// DefaultJSONRequestBodyLimit bounds ordinary API JSON payloads to 2 MiB.
// This leaves encoding headroom for fields capped at 1 MiB.
const DefaultJSONRequestBodyLimit int64 = 2 << 20

// NewJSONDecoder returns a decoder whose request body uses the default cap.
func NewJSONDecoder(w http.ResponseWriter, r *http.Request) *json.Decoder {
	r.Body = http.MaxBytesReader(w, r.Body, DefaultJSONRequestBodyLimit)
	return json.NewDecoder(r.Body)
}

// DecodeJSONBody decodes one JSON value while enforcing the default body cap.
func DecodeJSONBody(w http.ResponseWriter, r *http.Request, dst any) error {
	return NewJSONDecoder(w, r).Decode(dst)
}

// ReadJSONBody reads a JSON request body up to the default limit.
func ReadJSONBody(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	r.Body = http.MaxBytesReader(w, r.Body, DefaultJSONRequestBodyLimit)
	return io.ReadAll(r.Body)
}

// IsRequestBodyTooLarge reports whether a capped request exceeded its limit.
func IsRequestBodyTooLarge(err error) bool {
	var maxErr *http.MaxBytesError
	return errors.As(err, &maxErr)
}
