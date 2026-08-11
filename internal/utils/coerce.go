package utils

// CoerceInt converts a JSON-decoded, database-typed, or programmatically
// constructed number into an int. json.Unmarshal produces float64, database
// scans produce int, and typed DTOs may carry wider int widths. Non-numeric
// values are rejected; fractional floats truncate exactly like the legacy
// decode path.
func CoerceInt(value any) (int, bool) {
	switch v := value.(type) {
	case int:
		return v, true
	case int8:
		return int(v), true
	case int16:
		return int(v), true
	case int32:
		return int(v), true
	case int64:
		return int(v), true
	case float32:
		return int(v), true
	case float64:
		return int(v), true
	default:
		return 0, false
	}
}

// CoerceIntSlice converts a JSON-decoded or Go-side integer array into a
// fresh []int. Accepts []int, []float64, []any, and nil, mirroring
// the legacy per-field coercion so callers keep distinguishing "field absent"
// (via their own presence check) from "explicitly empty".
func CoerceIntSlice(value any) ([]int, bool) {
	if value == nil {
		return []int{}, true
	}
	switch s := value.(type) {
	case []int:
		out := make([]int, len(s))
		copy(out, s)
		return out, true
	case []int64:
		out := make([]int, len(s))
		for i, n := range s {
			out[i] = int(n)
		}
		return out, true
	case []float64:
		out := make([]int, len(s))
		for i, n := range s {
			out[i] = int(n)
		}
		return out, true
	case []any:
		out := make([]int, 0, len(s))
		for _, e := range s {
			n, ok := CoerceInt(e)
			if !ok {
				return nil, false
			}
			out = append(out, n)
		}
		return out, true
	default:
		return nil, false
	}
}
