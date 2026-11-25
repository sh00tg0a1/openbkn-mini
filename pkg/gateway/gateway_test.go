package gateway

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// upstreamFor says where the routes send a path, as the gateway's mux decides.
func upstreamFor(routes []Route, path string) string {
	mux := http.NewServeMux()
	for _, r := range routes {
		mux.HandleFunc(r.Prefix, func(w http.ResponseWriter, _ *http.Request) { w.Header().Set("X-Upstream", r.Upstream) })
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec.Header().Get("X-Upstream")
}

func TestRoutesWithTheStandInOpenAllOfSafe(t *testing.T) {
	routes := Routes(false)
	if got := upstreamFor(routes, "/api/safe/v1/authz/checks"); got != safeUpstream {
		t.Errorf("stand-in authz goes to %q", got)
	}
	for _, path := range []string{"/login", "/oauth2/auth", "/userinfo"} {
		if got := upstreamFor(routes, path); got != "" {
			t.Errorf("%s routed to %q without sign-in", path, got)
		}
	}
}

func TestRoutesWithSafeOpenOnlyWhatThePlatformOpens(t *testing.T) {
	routes := Routes(true)
	for path, want := range map[string]string{
		"/login":                                  safeUpstream,
		"/consent":                                safeUpstream,
		"/device":                                 safeUpstream,
		"/device/success":                         safeUpstream,
		"/change-password":                        safeUpstream,
		"/api/safe/v1/me":                         safeUpstream,
		"/api/safe/v1/me/permissions":             safeUpstream,
		"/api/safe/v1/capabilities":               safeUpstream,
		"/api/safe/v1/admin/users":                safeUpstream,
		"/api/safe/v1/auth/change-password":       safeUpstream,
		"/.well-known/oauth-authorization-server": safeUpstream,
		"/oauth2/auth":                            hydraPublicUpstream,
		"/oauth2/token":                           hydraPublicUpstream,
		"/oauth2/device/auth":                     hydraPublicUpstream,
		"/.well-known/openid-configuration":       hydraPublicUpstream,
		"/.well-known/jwks.json":                  hydraPublicUpstream,
		"/userinfo":                               hydraPublicUpstream,
		"/api/bkn-backend/v1/knowledge-networks":  "127.0.0.1:13015",
	} {
		if got := upstreamFor(routes, path); got != want {
			t.Errorf("%s goes to %q, want %q", path, got, want)
		}
	}
	// These take no token: the platform keeps them off its gateway, so does mini.
	for _, path := range []string{
		"/api/safe/v1/authz/checks",
		"/api/safe/v1/directory/names",
		"/api/safe/v1/internal/license/current",
		"/api/safe/v1/api-keys/introspect",
		"/api/safe/in/v1/managed-proxy-accounts",
		"/admin/clients",
		"/loginx",
	} {
		if got := upstreamFor(routes, path); got != "" {
			t.Errorf("%s is reachable through the gateway (%s)", path, got)
		}
	}
}

func TestRoutesOpenOnlyTheServicesPublicAPIs(t *testing.T) {
	for _, safe := range []bool{false, true} {
		routes := Routes(safe)
		for path, want := range map[string]string{
			"/api/bkn-backend/v1/knowledge-networks":        "127.0.0.1:13015",
			"/api/bkn-backend/v2/knowledge-networks":        "127.0.0.1:13015",
			"/api/ontology-manager/v1/knowledge-networks":   "127.0.0.1:13015",
			"/api/ontology-query/v1/knowledge-networks/k/x": "127.0.0.1:13018",
			"/api/vega-backend/v1/catalogs":                 "127.0.0.1:13014",
			"/api/agent-retrieval/v1/mcp/":                  "127.0.0.1:30779",
			"/api/agent-retrieval/v1":                       "127.0.0.1:30779",
			"/api/agent-operator-integration/v1/skills":     "127.0.0.1:9000",
			"/api/capabilities-lab/v1/x":                    "127.0.0.1:9000",
			"/api/mf-model-manager/v1/llm/list":             "127.0.0.1:9898",
			"/api/mf-model-api/v1/chat/completions":         "127.0.0.1:9898",
			"/mini-oss/blob/mini-local/k":                   "127.0.0.1:8095",
		} {
			if got := upstreamFor(routes, path); got != want {
				t.Errorf("safe=%v: %s goes to %q, want %q", safe, path, got, want)
			}
		}
		// Internal routes trust identity headers instead of a token: the
		// platform's ingress does not open them, nor does mini.
		for _, path := range []string{
			"/api/bkn-backend/in/v1/knowledge-networks",
			"/api/vega-backend/in/v1/catalogs",
			"/api/ontology-query/in/v1/knowledge-networks/k/x",
			"/api/agent-retrieval/in/v1/kn/search",
			"/api/agent-operator-integration/internal-v1/skills/s",
			"/api/ontology-manager/in/v1/knowledge-networks",
			"/api/bkn-backend/v1/../in/v1/knowledge-networks",
			"/api/bkn-backend/v1x",
		} {
			if got := upstreamFor(routes, path); got != "" {
				t.Errorf("safe=%v: %s is reachable through the gateway (%s)", safe, path, got)
			}
		}
	}
}

func TestPublicOpensEachPathAndWhatIsUnderIt(t *testing.T) {
	routes := Public("u:1", "/api/x/v1", "/api/x/v2")
	for path, want := range map[string]string{
		"/api/x/v1": "u:1", "/api/x/v1/a/b": "u:1", "/api/x/v2/a": "u:1",
		"/api/x/v3": "", "/api/x/in/v1/a": "", "/api/x/v10": "",
	} {
		if got := upstreamFor(routes, path); got != want {
			t.Errorf("%s goes to %q, want %q", path, got, want)
		}
	}
}
