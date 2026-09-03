package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"

	v2 "windshift/internal/restapi/v2"
)

type document struct {
	OpenAPI string                          `json:"openapi"`
	Paths   map[string]map[string]operation `json:"paths"`
}

type operation struct {
	Security []map[string][]string `json:"security"`
}

func main() {
	specPath := flag.String("spec", "api/openapi-v2.json", "v2 OpenAPI JSON path")
	flag.Parse()

	data, err := os.ReadFile(*specPath)
	if err != nil {
		fail("read spec: %v", err)
	}
	var spec document
	if err := json.Unmarshal(data, &spec); err != nil {
		fail("decode spec: %v", err)
	}
	if !strings.HasPrefix(spec.OpenAPI, "3.") {
		fail("openapi version %q is not 3.x", spec.OpenAPI)
	}

	want := make(map[string]v2.Route)
	for _, route := range v2.Inventory() {
		key := strings.ToLower(route.Method) + " " + route.Path
		want[key] = route
		item, ok := spec.Paths[route.Path]
		if !ok {
			fail("route %s is missing", key)
		}
		operation, ok := item[strings.ToLower(route.Method)]
		if !ok {
			fail("route %s is missing", key)
		}
		validateSecurity(route, operation)
	}

	for path, item := range spec.Paths {
		for method := range item {
			if !isHTTPMethod(method) {
				continue
			}
			key := method + " " + path
			if _, ok := want[key]; !ok {
				fail("OpenAPI operation %s is not in the v2 inventory", key)
			}
		}
	}
	fmt.Printf("API v2 OpenAPI parity is valid (%d operations).\n", len(want))
}

func validateSecurity(route v2.Route, operation operation) {
	if route.Auth == v2.AuthPublic {
		if len(operation.Security) != 0 {
			fail("public route %s %s declares security", route.Method, route.Path)
		}
		return
	}
	if len(operation.Security) != 1 {
		fail("authenticated route %s %s must declare one security requirement", route.Method, route.Path)
	}
	scopes, ok := operation.Security[0]["BearerAuth"]
	if !ok || strings.Join(scopes, "\x00") != strings.Join(route.Scopes, "\x00") {
		fail("route %s %s scopes do not match its inventory", route.Method, route.Path)
	}
}

func isHTTPMethod(method string) bool {
	switch strings.ToUpper(method) {
	case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodHead, http.MethodOptions:
		return true
	default:
		return false
	}
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "openapi-v2-check: "+format+"\n", args...)
	os.Exit(1)
}
