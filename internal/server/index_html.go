package server

import (
	"bytes"
	"html"
	"strconv"
)

func prepareIndexHTML(indexHTML []byte, nonce, contextPath string) []byte {
	htmlBytes := bytes.Replace(indexHTML, []byte("<script>"), []byte(`<script nonce="`+html.EscapeString(nonce)+`">`), 1)

	baseHref := "/"
	if contextPath != "" {
		baseHref = contextPath + "/"
		htmlBytes = prefixRootRelativeHTMLAttrs(htmlBytes, contextPath)
	}

	bootstrap := []byte(`<base href="` + html.EscapeString(baseHref) + `">` + "\n    " + `<script nonce="` + html.EscapeString(nonce) + `">window.__WINDSHIFT_CONTEXT_PATH__=` + strconv.Quote(contextPath) + `;</script>`)
	if bytes.Contains(htmlBytes, []byte("<head>")) {
		htmlBytes = bytes.Replace(htmlBytes, []byte("<head>"), append([]byte("<head>\n    "), bootstrap...), 1)
	}
	return htmlBytes
}

func prefixRootRelativeHTMLAttrs(htmlBytes []byte, contextPath string) []byte {
	prefix := []byte(contextPath)
	htmlBytes = bytes.ReplaceAll(htmlBytes, []byte(`href="/`), append([]byte(`href="`), append(prefix, '/')...))
	htmlBytes = bytes.ReplaceAll(htmlBytes, []byte(`src="/`), append([]byte(`src="`), append(prefix, '/')...))
	return htmlBytes
}
