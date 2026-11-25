package fakes

import "net/http"

// newHydra answers token introspection: every token, including an empty one,
// is an active super-admin user token. All ext fields must be present because
// comm-go reads them with unchecked type assertions.
func newHydra() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /admin/oauth2/introspect", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"active":     true,
			"sub":        SuperAdminID,
			"scope":      "openid offline all",
			"client_id":  "openbkn-mini",
			"token_type": "Bearer",
			"exp":        4102444800,
			"iat":        1700000000,
			"ext": map[string]any{
				"visitor_type": "realname",
				"login_ip":     "127.0.0.1",
				"udid":         "openbkn-mini",
				"account_type": "other",
				"client_type":  "web",
			},
		})
	})
	mux.HandleFunc("/", notImplemented)
	return mux
}
