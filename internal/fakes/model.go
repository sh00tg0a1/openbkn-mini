package fakes

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// ModelUpstream points the model stand-in at another OpenBKN platform whose
// model factory serves the actual models. Empty BaseURL means "no models".
type ModelUpstream struct {
	BaseURL  string // e.g. https://openbkn.example.com
	AppKey   string // bak_ key issued on that platform
	Insecure bool   // skip TLS verification (self-signed dev platforms)
}

// newModel stands in for mf-model-manager and mf-model-api.
//
// Models come from one of two places. With direct models (a models file),
// lookups are answered from that list and inference goes straight to each
// model's provider. With an upstream OpenBKN platform, lookups come from its
// small-model list and inference is forwarded to its model API with the
// AppKey. With neither, every lookup reports no model and the reranker keeps
// the caller's order.
func newModel(up ModelUpstream, direct []DirectModel) http.Handler {
	mux := http.NewServeMux()
	reg := &modelRegistry{up: up}
	list := reg.load
	if len(direct) > 0 {
		list = directModels(direct).smallModels
	}
	find := func(match func(smallModel) bool) smallModel {
		for _, m := range list() {
			if match(m) {
				return m
			}
		}
		return nil
	}

	for _, base := range []string{"/api/private/mf-model-manager/v1", "/api/mf-model-manager/v1"} {
		mux.HandleFunc("GET "+base+"/small-model/get", func(w http.ResponseWriter, r *http.Request) {
			if m := find(func(m smallModel) bool { return m.ID() == r.URL.Query().Get("model_id") }); m != nil {
				writeJSON(w, http.StatusOK, m)
				return
			}
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "model not found"})
		})
		mux.HandleFunc("GET "+base+"/small-model/get_by_name", func(w http.ResponseWriter, r *http.Request) {
			if m := find(func(m smallModel) bool { return m.Name() == r.URL.Query().Get("model_name") }); m != nil {
				writeJSON(w, http.StatusOK, m)
				return
			}
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "model not found"})
		})
		mux.HandleFunc("GET "+base+"/small-model/get_default", func(w http.ResponseWriter, r *http.Request) {
			typ := r.URL.Query().Get("model_type")
			if m := find(func(m smallModel) bool { return m.Type() == typ && m.Default() }); m != nil {
				writeJSON(w, http.StatusOK, m)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{})
		})
		mux.HandleFunc("GET "+base+"/small-model/list", func(w http.ResponseWriter, r *http.Request) {
			// Name matches as a substring, like the model factory's LIKE filter.
			name, typ := r.URL.Query().Get("model_name"), r.URL.Query().Get("model_type")
			items := []smallModel{}
			for _, m := range list() {
				if strings.Contains(m.Name(), name) && (typ == "" || m.Type() == typ) {
					items = append(items, m)
				}
			}
			writeJSON(w, http.StatusOK, map[string]any{"count": len(items), "data": items})
		})
	}

	if len(direct) > 0 {
		directModels(direct).register(mux)
		mux.HandleFunc("/", notImplemented)
		return mux
	}
	if up.BaseURL == "" {
		// No models: let the reranker keep the caller's order.
		mux.HandleFunc("POST /api/private/mf-model-api/v1/small-model/reranker", rerankInOrder)
		mux.HandleFunc("/", notImplemented)
		return mux
	}

	target, err := url.Parse(strings.TrimRight(up.BaseURL, "/"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "[mini] model upstream %q is not a URL: %v\n", up.BaseURL, err)
		mux.HandleFunc("/", notImplemented)
		return mux
	}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			// The private (service-to-service) routes are not exposed by the
			// upstream gateway; its public routes take the AppKey instead.
			pr.Out.URL.Path = strings.Replace(pr.Out.URL.Path, "/api/private/mf-model-api/", "/api/mf-model-api/", 1)
			pr.Out.URL.RawPath = ""
			pr.Out.Header.Set("Authorization", "Bearer "+up.AppKey)
			pr.Out.Host = target.Host
		},
		Transport:     upstreamTransport(up.Insecure),
		FlushInterval: -1, // stream chat completions as they arrive
	}
	for _, p := range []string{
		"POST /api/private/mf-model-api/v1/chat/completions",
		"POST /api/mf-model-api/v1/chat/completions",
		"POST /api/private/mf-model-api/v1/small-model/embeddings",
		"POST /api/private/mf-model-api/v1/small-model/reranker",
	} {
		mux.Handle(p, proxy)
	}
	mux.HandleFunc("/", notImplemented)
	return mux
}

func rerankInOrder(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Documents []any `json:"documents"`
	}
	readJSON(r, &req)
	results := make([]map[string]any, 0, len(req.Documents))
	for i, doc := range req.Documents {
		results = append(results, map[string]any{"index": i, "relevance_score": 1.0 / float64(i+1), "document": doc})
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": results})
}

func upstreamTransport(insecure bool) http.RoundTripper {
	t := http.DefaultTransport.(*http.Transport).Clone()
	if insecure {
		t.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // opt-in for self-signed dev platforms
	}
	return t
}

// smallModel is one entry of the upstream small-model list, kept as returned.
type smallModel map[string]any

func (m smallModel) str(k string) string { s, _ := m[k].(string); return s }
func (m smallModel) ID() string          { return m.str("model_id") }
func (m smallModel) Name() string        { return m.str("model_name") }
func (m smallModel) Type() string        { return m.str("model_type") }
func (m smallModel) Default() bool {
	for _, k := range []string{"is_default", "default"} {
		if b, ok := m[k].(bool); ok {
			return b
		}
	}
	return false
}

type modelRegistry struct {
	up      ModelUpstream
	mu      sync.Mutex
	models  []smallModel
	fetched time.Time
}

func (r *modelRegistry) load() []smallModel {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.up.BaseURL == "" {
		return nil
	}
	if r.models != nil && time.Since(r.fetched) < 5*time.Minute {
		return r.models
	}
	models, err := fetchSmallModels(r.up)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[mini] list upstream models: %v\n", err)
		return r.models
	}
	r.models, r.fetched = models, time.Now()
	return r.models
}

func fetchSmallModels(up ModelUpstream) ([]smallModel, error) {
	u := strings.TrimRight(up.BaseURL, "/") + "/api/mf-model-manager/v1/small-model/list?page=1&size=200"
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+up.AppKey)
	resp, err := (&http.Client{Transport: upstreamTransport(up.Insecure), Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var payload struct {
		Data []smallModel `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	for _, m := range payload.Data {
		delete(m, "model_config") // upstream connection details stay upstream
	}
	return payload.Data, nil
}

// newNSQ accepts nsqd HTTP publishes (POST /pub?topic=...) and drops them.
func newNSQ() http.Handler {
	mux := http.NewServeMux()
	ok := func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("OK")) }
	mux.HandleFunc("POST /pub", ok)
	mux.HandleFunc("POST /mpub", ok)
	mux.HandleFunc("GET /ping", ok)
	mux.HandleFunc("/", notImplemented)
	return mux
}
