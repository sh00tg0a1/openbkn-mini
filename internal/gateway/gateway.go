// Package gateway is mini's single entry point: it routes /api/<service>/...
// to the in-process services the same way the platform ingress does.
package gateway

import (
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Route maps a path prefix to an upstream address.
type Route struct {
	Prefix   string
	Upstream string // host:port
}

// DefaultRoutes lists the platform API prefixes mini serves.
func DefaultRoutes() []Route {
	return []Route{
		{"/api/bkn-backend/", "127.0.0.1:13015"},
		{"/api/ontology-manager/", "127.0.0.1:13015"}, // legacy name, rewritten to /api/bkn-backend/
		{"/api/ontology-query/", "127.0.0.1:13018"},
		{"/api/vega-backend/", "127.0.0.1:13014"},
		{"/api/agent-retrieval/", "127.0.0.1:30779"},
		{"/api/agent-operator-integration/", "127.0.0.1:9000"},
		{"/api/capabilities-lab/", "127.0.0.1:9000"},
		{"/mini-oss/", "127.0.0.1:8095"},
		{"/api/safe/", "127.0.0.1:3000"},
		{"/api/mf-model-api/", "127.0.0.1:9898"},
		{"/api/mf-model-manager/", "127.0.0.1:9898"},
	}
}

// Start serves the gateway on addr in a goroutine.
func Start(addr string, routes []Route) {
	mux := http.NewServeMux()
	for _, r := range routes {
		target := &url.URL{Scheme: "http", Host: r.Upstream}
		proxy := httputil.NewSingleHostReverseProxy(target)
		proxy.FlushInterval = -1 // stream SSE / MCP responses immediately
		if r.Prefix == "/api/ontology-manager/" {
			mux.Handle(r.Prefix, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				req.URL.Path = "/api/bkn-backend/" + strings.TrimPrefix(req.URL.Path, r.Prefix)
				req.URL.RawPath = ""
				proxy.ServeHTTP(w, req)
			}))
			continue
		}
		mux.Handle(r.Prefix, proxy)
	}
	studioDir := os.Getenv("MINI_STUDIO_DIR")
	files := http.FileServer(http.Dir(studioDir))
	// Studio is built with base /studio/; its files live at the root of the dist dir.
	const studioBase = "/studio/"
	studioConfig := func(w http.ResponseWriter, r *http.Request) {
		// Hosted mode turns off studio's OAuth gate: there is no login in mini.
		w.Header().Set("Content-Type", "application/javascript")
		w.Header().Set("Cache-Control", "no-store")
		// The local user only carries the permissions of the modules mini runs, so
		// studio's own permission filter hides the rest of the console.
		fmt.Fprintln(w, `window.__BKN_STUDIO_RUNTIME__ = {
  mode: "hosted",
  // Services mini does not run: BKN project, Trace, BKN Agent, function tools.
  hiddenNavigation: ["bkn-project", "observability", "experience-agent", "functions"],
  features: { executionFactoryLab: { function: false } },
  currentUser: {
    id: "266c6a42-6131-4d62-8f39-853e7093701c",
    name: "admin",
    isAdmin: false,
    isSuperAdmin: false,
    roles: [],
    permissions: [
      "knowledge-network:view", "knowledge-network:create", "knowledge-network:edit",
      "knowledge-network:delete", "knowledge-network:import", "knowledge-network:export",
      "knowledge-network:preview", "knowledge-network:concept-group:view",
      "catalog:view_detail", "catalog:create", "catalog:modify", "catalog:delete",
      "catalog:task_manage", "catalog:resource_manage", "resource:view_detail",
      // Execution factory: API tools, MCP and Skill. No functions.
      "execution-factory:toolbox:view", "execution-factory:toolbox:create",
      "execution-factory:toolbox:edit", "execution-factory:toolbox:publish",
      "execution-factory:toolbox:delete", "execution-factory:toolbox:debug",
      "execution-factory:tool:view", "execution-factory:tool:create",
      "execution-factory:tool:edit", "execution-factory:tool:delete", "execution-factory:tool:debug",
      "execution-factory:mcp:view", "execution-factory:mcp:create", "execution-factory:mcp:edit",
      "execution-factory:mcp:publish", "execution-factory:mcp:delete", "execution-factory:mcp:debug",
      "execution-factory:skill:view", "execution-factory:skill:create", "execution-factory:skill:edit",
      "execution-factory:skill:publish", "execution-factory:skill:delete",
      "execution-factory:impex:export", "execution-factory:impex:import"
    ]
  }
};`)
	}
	mux.HandleFunc("/config.js", studioConfig)
	mux.HandleFunc(studioBase+"config.js", studioConfig)
	mux.Handle(studioBase, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if studioDir == "" {
			http.Error(w, "studio not bundled", http.StatusNotFound)
			return
		}
		rel := filepath.Clean("/" + strings.TrimPrefix(r.URL.Path, studioBase))
		if info, err := os.Stat(filepath.Join(studioDir, rel)); err == nil && !info.IsDir() {
			http.StripPrefix(strings.TrimSuffix(studioBase, "/"), files).ServeHTTP(w, r)
			return
		}
		// Every other path is a client-side route.
		w.Header().Set("Cache-Control", "no-store")
		http.ServeFile(w, r, filepath.Join(studioDir, "index.html"))
	}))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			fmt.Fprintf(os.Stderr, "[gateway] no route: %s %s\n", r.Method, r.URL.RequestURI())
			http.Error(w, "openbkn mini: no service for "+r.URL.Path, http.StatusNotFound)
			return
		}
		if studioDir != "" && r.URL.Path == "/" {
			http.Redirect(w, r, studioBase, http.StatusFound)
			return
		}
		http.NotFound(w, r)
	})
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := srv.ListenAndServe(); err != nil {
			fmt.Fprintf(os.Stderr, "[mini] FATAL gateway on %s: %v\n", addr, err)
			os.Exit(1)
		}
	}()
}
