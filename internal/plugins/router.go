package plugins

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
)

// Router handles HTTP routing for plugins
type Router struct {
	manager *Manager
}

// NewRouter creates a new plugin router
func NewRouter(manager *Manager) *Router {
	return &Router{
		manager: manager,
	}
}

// RegisterRoutes registers plugin routes with the main ServeMux
// Uses catch-all pattern {path...} for plugin path matching
func (r *Router) RegisterRoutes(mux *http.ServeMux) {
	// Match every method while preserving the plugin name and trailing path.
	mux.HandleFunc("GET /api/plugins/{plugin}/{path...}", r.HandlePluginRequest)
	mux.HandleFunc("POST /api/plugins/{plugin}/{path...}", r.HandlePluginRequest)
	mux.HandleFunc("PUT /api/plugins/{plugin}/{path...}", r.HandlePluginRequest)
	mux.HandleFunc("DELETE /api/plugins/{plugin}/{path...}", r.HandlePluginRequest)
	mux.HandleFunc("PATCH /api/plugins/{plugin}/{path...}", r.HandlePluginRequest)
	mux.HandleFunc("OPTIONS /api/plugins/{plugin}/{path...}", r.HandlePluginRequest)
}

// HandlePluginRequest handles incoming requests for plugins
func (r *Router) HandlePluginRequest(w http.ResponseWriter, req *http.Request) {
	pluginName := req.PathValue("plugin")

	pluginPath := "/" + req.PathValue("path")
	if pluginPath == "/" {
		pluginPath = "/"
	}

	plugin, exists := r.manager.GetPlugin(pluginName)
	if !exists {
		http.Error(w, fmt.Sprintf("Plugin not found: %s", pluginName), http.StatusNotFound)
		return
	}

	if !plugin.Enabled {
		http.Error(w, fmt.Sprintf("Plugin is disabled: %s", pluginName), http.StatusForbidden)
		return
	}

	routeFound := false
	for _, route := range plugin.Routes {
		if matchRoute(route, req.Method, pluginPath) {
			routeFound = true
			break
		}
	}

	if !routeFound {
		http.Error(w, fmt.Sprintf("Route not found in plugin: %s %s", req.Method, pluginPath), http.StatusNotFound)
		return
	}

	// Adapt the HTTP request to the plugin protocol, then forward it.
	body, err := io.ReadAll(req.Body)
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}

	headers := make(map[string]string)
	for key, values := range req.Header {
		if len(values) > 0 {
			headers[key] = values[0]
		}
	}

	query := make(map[string]string)
	for key, values := range req.URL.Query() {
		if len(values) > 0 {
			query[key] = values[0]
		}
	}

	pluginReq := &HTTPRequest{
		Method:  req.Method,
		Path:    pluginPath,
		Headers: headers,
		Body:    string(body),
		Query:   query,
		Params:  map[string]string{"plugin": pluginName},
	}

	pluginResp, err := r.manager.HandleRequest(pluginName, pluginReq)
	if err != nil {
		http.Error(w, fmt.Sprintf("Plugin error: %v", err), http.StatusInternalServerError)
		return
	}

	for key, value := range pluginResp.Headers {
		w.Header().Set(key, value)
	}

	if w.Header().Get("Content-Type") == "" {
		// Infer a default only when the plugin did not provide one.
		var js json.RawMessage
		if err := json.Unmarshal([]byte(pluginResp.Body), &js); err == nil {
			w.Header().Set("Content-Type", "application/json")
		} else {
			w.Header().Set("Content-Type", "text/plain")
		}
	}

	if pluginResp.StatusCode != 0 {
		w.WriteHeader(pluginResp.StatusCode)
	} else {
		w.WriteHeader(http.StatusOK)
	}

	if _, err := w.Write([]byte(pluginResp.Body)); err != nil { //nolint:gosec // G705: plugin responses from trusted/verified plugin code
		// Log error but response is already partially written
		slog.Error("failed to write plugin response", slog.Any("error", err))
	}
}

// matchRoute checks if a route matches the request
func matchRoute(route Route, method, path string) bool {
	if route.Method != "" && route.Method != method {
		return false
	}

	if route.Path == path {
		return true
	}

	if strings.HasSuffix(route.Path, "*") {
		// A trailing star matches the route prefix.
		prefix := strings.TrimSuffix(route.Path, "*")
		return strings.HasPrefix(path, prefix)
	}

	return false
}

// GetPluginRoutes returns all registered plugin routes
func (r *Router) GetPluginRoutes() map[string][]Route {
	routes := make(map[string][]Route)

	for _, plugin := range r.manager.ListPlugins() {
		if plugin.Enabled {
			routes[plugin.Manifest.Name] = plugin.Routes
		}
	}

	return routes
}
