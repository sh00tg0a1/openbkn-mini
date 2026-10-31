package fakes

import "net/http"

// newModel stands in for mf-model-manager and mf-model-api. Spike version:
// no models registered yet, so lookups report "none" in the shapes callers
// treat as absent, and inference routes are not implemented.
func newModel() http.Handler {
	mux := http.NewServeMux()
	notFound := func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "model not found"})
	}
	for _, base := range []string{"/api/private/mf-model-manager/v1", "/api/mf-model-manager/v1"} {
		mux.HandleFunc("GET "+base+"/small-model/get", notFound)
		mux.HandleFunc("GET "+base+"/small-model/get_by_name", notFound)
		mux.HandleFunc("GET "+base+"/small-model/get_default", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusOK, map[string]any{})
		})
		mux.HandleFunc("GET "+base+"/small-model/list", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusOK, map[string]any{"count": 0, "data": []any{}})
		})
		mux.HandleFunc("GET "+base+"/llm/list", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusOK, map[string]any{"count": 0, "data": []any{}})
		})
	}
	// No reranker configured: keep the caller's order with descending scores.
	mux.HandleFunc("POST /api/private/mf-model-api/v1/small-model/reranker", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Documents []any `json:"documents"`
		}
		readJSON(r, &req)
		results := make([]map[string]any, 0, len(req.Documents))
		for i, doc := range req.Documents {
			results = append(results, map[string]any{
				"index": i, "relevance_score": 1.0 / float64(i+1), "document": doc,
			})
		}
		writeJSON(w, http.StatusOK, map[string]any{"results": results})
	})
	mux.HandleFunc("/", notImplemented)
	return mux
}

// newNSQ accepts nsqd HTTP publishes (POST /pub?topic=...) and drops them.
func newNSQ() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /pub", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("OK"))
	})
	mux.HandleFunc("POST /mpub", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("OK"))
	})
	mux.HandleFunc("GET /ping", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("OK"))
	})
	mux.HandleFunc("/", notImplemented)
	return mux
}
