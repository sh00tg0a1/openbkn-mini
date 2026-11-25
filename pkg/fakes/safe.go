package fakes

import (
	"net/http"
	"strings"
)

const proxyPrefix = "mini-proxy-"

// newSafe stands in for bkn-safe: every authorization check is allowed, row
// filters are TRUE, properties are fully readable and resource scope is
// unrestricted. Managed proxy accounts are stateless: the id encodes the
// knowledge network id.
func newSafe(reg *registry) http.Handler {
	mux := http.NewServeMux()

	// ---- service-to-service authorization ----
	mux.HandleFunc("POST /api/safe/v1/authz/checks", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Checks []struct {
				Resource struct {
					Type string `json:"type"`
					ID   string `json:"id"`
				} `json:"resource"`
				Operation string `json:"operation"`
			} `json:"checks"`
		}
		readJSON(r, &req)
		results := make([]map[string]any, 0, len(req.Checks))
		for _, c := range req.Checks {
			results = append(results, map[string]any{
				"resource_type": c.Resource.Type, "resource_id": c.Resource.ID,
				"operation": c.Operation, "allowed": true,
			})
		}
		writeJSON(w, http.StatusOK, map[string]any{"allowed": true, "results": results})
	})
	mux.HandleFunc("POST /api/safe/v1/authz/resource-filter", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Resources []struct {
				Type string `json:"type"`
				ID   string `json:"id"`
			} `json:"resources"`
			IncludeOperations bool `json:"include_operations"`
		}
		readJSON(r, &req)
		out := make([]map[string]any, 0, len(req.Resources))
		for _, res := range req.Resources {
			ops := []string{}
			if req.IncludeOperations {
				ops = reg.operations(res.Type)
			}
			out = append(out, map[string]any{"resource_id": res.ID, "resource_type": res.Type, "operations": ops})
		}
		writeJSON(w, http.StatusOK, map[string]any{"resources": out})
	})
	mux.HandleFunc("GET /api/safe/v1/authz/resources", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ids": []string{}, "unrestricted": true, "requires_candidate_filter": false})
	})
	mux.HandleFunc("POST /api/safe/v1/authz/row-filters", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ObjectTypeRefs []string `json:"object_type_refs"`
		}
		readJSON(r, &req)
		entries := make([]map[string]any, 0, len(req.ObjectTypeRefs))
		for _, ref := range req.ObjectTypeRefs {
			entries = append(entries, map[string]any{
				"object_type_ref":             ref,
				"predicate":                   map[string]any{"kind": "true"},
				"effective_row_filter_digest": "mini-unrestricted",
			})
		}
		writeJSON(w, http.StatusOK, map[string]any{"entries": entries})
	})
	mux.HandleFunc("POST /api/safe/v1/authz/property-levels", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Items []struct {
				ObjectTypeRef string   `json:"object_type_ref"`
				Properties    []string `json:"properties"`
			} `json:"items"`
		}
		readJSON(r, &req)
		entries := make([]map[string]any, 0, len(req.Items))
		for _, it := range req.Items {
			props := make([]map[string]any, 0, len(it.Properties))
			for _, p := range it.Properties {
				props = append(props, map[string]any{"name": p, "level": "full", "source": "object_type"})
			}
			entries = append(entries, map[string]any{"object_type_ref": it.ObjectTypeRef, "properties": props})
		}
		writeJSON(w, http.StatusOK, map[string]any{"entries": entries})
	})
	noContent := func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }
	mux.HandleFunc("POST /api/safe/v1/authz/policies", noContent)
	mux.HandleFunc("DELETE /api/safe/v1/authz/policies", noContent)
	mux.HandleFunc("PUT /api/safe/v1/authz/resource-parents", noContent)
	mux.HandleFunc("DELETE /api/safe/v1/authz/resource-parents", noContent)
	mux.HandleFunc("GET /api/safe/v1/authz/resource-parents", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"items": []any{}, "total": 0})
	})
	mux.HandleFunc("POST /api/safe/v1/api-keys/introspect", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"active": false})
	})

	// ---- directory ----
	mux.HandleFunc("POST /api/safe/v1/directory/names", func(w http.ResponseWriter, r *http.Request) {
		var req map[string][]string
		readJSON(r, &req)
		names := func(ids []string) []map[string]string {
			out := make([]map[string]string, 0, len(ids))
			for _, id := range ids {
				name := id
				if id == SuperAdminID {
					name = SuperAdminName
				}
				out = append(out, map[string]string{"id": id, "name": name})
			}
			return out
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"user_names":       names(req["user_ids"]),
			"app_names":        names(req["app_ids"]),
			"contactor_names":  names(req["contactor_ids"]),
			"department_names": names(req["department_ids"]),
			"group_names":      names(req["group_ids"]),
		})
	})
	mux.HandleFunc("POST /api/safe/v1/directory/users-detail", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"users": []any{superAdminUser()}})
	})
	mux.HandleFunc("GET /api/safe/v1/directory/users/{id}", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, superAdminUser())
	})

	// ---- licence: 404 means community ----
	mux.HandleFunc("GET /api/safe/v1/internal/license/current", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "no licence installed"})
	})

	// ---- managed proxy accounts (stateless) ----
	mux.HandleFunc("POST /api/safe/in/v1/managed-proxy-accounts", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ManagedResourceType string `json:"managed_resource_type"`
			ManagedResourceID   string `json:"managed_resource_id"`
			Name                string `json:"name"`
		}
		readJSON(r, &req)
		writeJSON(w, http.StatusOK, proxyAccount(proxyPrefix+req.ManagedResourceID, "active", req.Name))
	})
	mux.HandleFunc("GET /api/safe/in/v1/managed-proxy-accounts/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if !strings.HasPrefix(id, proxyPrefix) {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "not found"})
			return
		}
		writeJSON(w, http.StatusOK, proxyAccount(id, "active", ""))
	})
	mux.HandleFunc("POST /api/safe/in/v1/managed-proxy-accounts/{id}/{action}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		switch r.PathValue("action") {
		case "disable":
			writeJSON(w, http.StatusOK, proxyAccount(id, "disabling", ""))
		case "archive":
			writeJSON(w, http.StatusOK, proxyAccount(id, "archived", ""))
		case "restore":
			writeJSON(w, http.StatusOK, proxyAccount(id, "active", ""))
		default:
			notImplemented(w, r)
		}
	})

	// ---- proxy grant sources: allow everything, nothing to sync ----
	mux.HandleFunc("POST /api/safe/in/v1/proxy-grant-sources/check", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"allowed": true})
	})
	resolve := func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			GrantorID string           `json:"grantor_id"`
			Sources   []map[string]any `json:"sources"`
			Upserts   []map[string]any `json:"upserts"`
		}
		readJSON(r, &req)
		resolved := []map[string]any{}
		for _, s := range append(req.Sources, req.Upserts...) {
			grantor := req.GrantorID
			if grantor == "" {
				grantor = SuperAdminID
			}
			s["granted_by"] = grantor
			resolved = append(resolved, s)
		}
		writeJSON(w, http.StatusOK, map[string]any{"denied_sources": []any{}, "resolved_sources": resolved})
	}
	mux.HandleFunc("POST /api/safe/in/v1/proxy-grant-sources/check-batch", resolve)
	mux.HandleFunc("POST /api/safe/in/v1/proxy-grant-sources/check-delta", resolve)
	syncResult := func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"added": 0, "transferred": 0, "revoked": 0, "unchanged": 0})
	}
	mux.HandleFunc("POST /api/safe/in/v1/proxy-grant-sources/sync", syncResult)
	mux.HandleFunc("POST /api/safe/in/v1/proxy-grant-sources/sync-delta", syncResult)
	mux.HandleFunc("POST /api/safe/in/v1/proxy-grant-sources/reconcile", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"policies_restored": 0, "policies_removed": 0, "markers_created": 0,
			"markers_removed": 0, "untracked_policies": 0, "invalid_sources": 0,
		})
	})

	// ---- user-facing (studio, CLI) ----
	mux.HandleFunc("GET /api/safe/v1/me", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, superAdminUser())
	})
	mux.HandleFunc("GET /api/safe/v1/me/permissions", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"is_admin":    true,
			"permissions": []any{map[string]any{"resource": map[string]any{"type": "*", "id": "*"}, "operations": []string{"*"}}},
		})
	})
	mux.HandleFunc("GET /api/safe/v1/me/authorization-registry", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(reg.raw)
	})
	mux.HandleFunc("GET /api/safe/v1/capabilities", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			// fallback_community: running community features without a licence,
			// so studio does not show the "not activated" banner.
			"licensed": false, "edition": "community", "state": "fallback_community",
			"features": []any{}, "capabilities": []any{}, "limits": map[string]any{}, "extensions": []any{},
		})
	})
	mux.HandleFunc("POST /api/safe/v1/me/logout", noContent)

	mux.HandleFunc("/", notImplemented)
	return mux
}

func superAdminUser() map[string]any {
	return map[string]any{
		"id": SuperAdminID, "account": SuperAdminName, "name": SuperAdminName,
		"roles": []string{"super_admin"}, "role_ids": []string{SuperAdminRoleID},
		"account_type": "user", "enabled": true,
	}
}

func proxyAccount(id, status, name string) map[string]any {
	knID := strings.TrimPrefix(id, proxyPrefix)
	if name == "" {
		name = "kn-" + knID
	}
	return map[string]any{
		"proxy_account_id":            id,
		"account_type":                "app",
		"name":                        name,
		"managed_by":                  "bkn",
		"managed_resource_type":       "knowledge_network",
		"managed_resource_id":         knID,
		"lifecycle_status":            status,
		"enabled":                     status == "active",
		"login_enabled":               false,
		"credential_issuance_enabled": false,
		"version":                     1,
	}
}
