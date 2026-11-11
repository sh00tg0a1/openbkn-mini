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
