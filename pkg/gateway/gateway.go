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

// DefaultRoutes lists the platform API prefixes mini serves with the bkn-safe
// stand-in.
func DefaultRoutes() []Route { return Routes(false) }

// Routes lists the paths mini serves. A prefix ending in "/" covers everything
// under it; any other is that one path. With safe, sign-in and permissions are
// the platform's own bkn-safe and Hydra, and the gateway opens the paths the
// platform's gateway opens for them, no more: bkn-safe's other APIs take no
// token and stay inside mini.
func Routes(safe bool) []Route {
	routes := []Route{
		{"/api/bkn-backend/", "127.0.0.1:13015"},
		{"/api/ontology-manager/", "127.0.0.1:13015"}, // legacy name, rewritten to /api/bkn-backend/
		{"/api/ontology-query/", "127.0.0.1:13018"},
		{"/api/vega-backend/", "127.0.0.1:13014"},
		{"/api/agent-retrieval/", "127.0.0.1:30779"},
		{"/api/agent-operator-integration/", "127.0.0.1:9000"},
		{"/api/capabilities-lab/", "127.0.0.1:9000"},
		{"/mini-oss/", "127.0.0.1:8095"},
		{"/api/mf-model-api/", "127.0.0.1:9898"},
		{"/api/mf-model-manager/", "127.0.0.1:9898"},
	}
	if !safe {
		return append(routes, Route{"/api/safe/", safeUpstream})
	}
	for _, path := range safePaths {
		routes = append(routes, Route{path, safeUpstream}, Route{path + "/", safeUpstream})
	}
	for _, path := range hydraPaths {
		routes = append(routes, Route{path, hydraPublicUpstream})
	}
	return routes
}

const (
	safeUpstream        = "127.0.0.1:3000"
	hydraPublicUpstream = "127.0.0.1:4444"
)

// safePaths are bkn-safe's pages and token-checked APIs, as the platform's
// gateway exposes them (bkn-safe chart, ingress.paths).
var safePaths = []string{
	"/login",
	"/sso/login",
	"/sso/callback",
	"/consent",
	"/device",
	"/change-password",
	"/api/safe/v1/auth/change-password",
	"/api/safe/v1/auth/token-exchange",
	"/api/safe/v1/admin",
	"/api/safe/v1/me",
	"/api/safe/v1/permission-requests",
	"/api/safe/v1/capabilities",
	"/api/safe/v1/oauth/register",
	"/.well-known/oauth-authorization-server",
	"/.well-known/oauth-protected-resource",
}

// hydraPaths are Hydra's public OAuth2 and OpenID Connect endpoints (bkn-safe
// chart, ingress.hydraPublicPaths).
var hydraPaths = []string{
	"/oauth2/",
	"/.well-known/openid-configuration",
	"/.well-known/jwks.json",
	"/userinfo",
}

// Options are what the gateway needs to know about how mini runs.
type Options struct {
	// Sandbox is true when a function executor runs Function and Skill code.
	// Studio then shows functions and the sandbox runtime page, as the
	// community edition does.
	Sandbox bool
	// Safe is true when mini runs the platform's sign-in and permissions.
	// Studio then signs the user in and asks bkn-safe what they may do.
	Safe bool
}

// Start serves the gateway on addr in a goroutine.
func Start(addr string, routes []Route, opts Options) {
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
	configJS := studioConfigJS(opts)
	studioConfig := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		w.Header().Set("Cache-Control", "no-store")
		fmt.Fprint(w, configJS)
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
