package fakes

import (
	"net/http"
	"sync"
	"time"
)

// newSandbox stands in for the sandbox control plane. mini does not execute
// functions or Skill scripts: sessions exist so execution-factory can start
// its session pool, and every execution request is answered as failed.
func newSandbox() http.Handler {
	var mu sync.Mutex
	sessions := map[string]map[string]any{}

	session := func(id string) map[string]any {
		now := time.Now().UTC()
		return map[string]any{
			"id": id, "status": "running", "template_id": "mini",
			"installed_dependencies": []any{}, "env_vars": map[string]string{},
			"workspace_path": "/workspace/" + id, "created_at": now, "updated_at": now,
		}
	}

	mux := http.NewServeMux()
	const api = "/api/v1"
	mux.HandleFunc("POST "+api+"/sessions", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID string `json:"id"`
		}
		readJSON(r, &req)
		if req.ID == "" {
			req.ID = newUUID()
		}
		mu.Lock()
		sessions[req.ID] = session(req.ID)
		s := sessions[req.ID]
		mu.Unlock()
		writeJSON(w, http.StatusCreated, s)
	})
	mux.HandleFunc("GET "+api+"/sessions/{id}", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		s, ok := sessions[r.PathValue("id")]
		mu.Unlock()
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "session not found"})
			return
		}
		writeJSON(w, http.StatusOK, s)
	})
	mux.HandleFunc("GET "+api+"/sessions", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		items := make([]any, 0, len(sessions))
		for _, s := range sessions {
			items = append(items, s)
		}
		mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{
			"items": items, "total": len(items), "limit": len(items), "offset": 0, "has_more": false,
		})
	})
	mux.HandleFunc("DELETE "+api+"/sessions/{id}", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		delete(sessions, r.PathValue("id"))
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	unsupported := func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"id": newUUID(), "status": "failed", "session_id": r.PathValue("id"),
			"exit_code": 1, "stdout": "",
			"stderr":       "openbkn mini does not run functions or Skill scripts",
			"return_value": nil, "metrics": map[string]any{}, "artifacts": []any{},
		})
	}
	mux.HandleFunc("POST "+api+"/executions/sessions/{id}/execute-sync", unsupported)
	mux.HandleFunc("POST "+api+"/sessions/{id}/dependencies/install", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "openbkn mini does not install function dependencies"})
	})
	mux.HandleFunc("POST "+api+"/sessions/{id}/files/upload", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "openbkn mini does not run Skill scripts"})
	})
	mux.HandleFunc("/", notImplemented)
	return mux
}
