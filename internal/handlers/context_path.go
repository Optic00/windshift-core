package handlers

import (
	"net/http"
	"strings"
)

const contextPathHeader = "X-Windshift-Context-Path"

func requestContextPrefix(r *http.Request) string {
	prefix := r.Header.Get(contextPathHeader)
	if prefix == "" || prefix == "/" || !strings.HasPrefix(prefix, "/") || strings.ContainsAny(prefix, "?#\\") || strings.Contains(prefix, "//") || strings.Contains(prefix, "..") {
		return ""
	}
	return strings.TrimSuffix(prefix, "/")
}
