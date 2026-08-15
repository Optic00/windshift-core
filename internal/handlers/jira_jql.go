package handlers

import "strings"

func escapeHandlerJQLString(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	return strings.ReplaceAll(value, `"`, `\"`)
}
