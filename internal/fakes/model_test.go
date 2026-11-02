package fakes

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// provider records what mini sent and answers like an OpenAI-style service.
type provider struct {
	srv  *httptest.Server
	got  map[string]map[string]any // path -> last body
	auth map[string]string         // path -> Authorization
}

func newProvider(t *testing.T) *provider {
	p := &provider{got: map[string]map[string]any{}, auth: map[string]string{}}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		p.got[r.URL.Path], p.auth[r.URL.Path] = body, r.Header.Get("Authorization")
		switch r.URL.Path {
		case "/v1/embeddings":
			writeJSON(w, 200, map[string]any{"data": []any{map[string]any{"object": "embedding", "index": 0, "embedding": []float64{0.1, 0.2}}}})
		case "/v1/rerank":
			writeJSON(w, 200, map[string]any{"results": []any{map[string]any{"index": 1, "relevance_score": 0.9}}})
		case "/dashscope/rerank":
			writeJSON(w, 200, map[string]any{"output": map[string]any{"results": []any{
				map[string]any{"index": 1, "relevance_score": 0.8}, map[string]any{"index": 0, "relevance_score": 0.2}}}})
		case "/v1/chat/completions":
			if body["stream"] == true {
				w.Header().Set("Content-Type", "text/event-stream")
				for _, c := range []string{"he", "llo"} {
					_, _ = io.WriteString(w, `data: {"choices":[{"delta":{"content":"`+c+`"}}]}`+"\n\n")
					w.(http.Flusher).Flush()
				}
				_, _ = io.WriteString(w, "data: [DONE]\n\n")
				return
			}
			writeJSON(w, 200, map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": "hi"}}}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(p.srv.Close)
	return p
}

func writeModels(t *testing.T, base string, rerankFormat, rerankPath string) []DirectModel {
	t.Setenv("MINI_TEST_KEY", "sk-env")
	doc := map[string]any{"models": []any{
		map[string]any{"name": "text-embedding-v4", "type": "embedding", "url": base + "/v1/embeddings", "api_key": "sk-emb", "embedding_dim": 1024, "batch_size": 10, "max_tokens": 512},
		map[string]any{"name": "gte-rerank-v2", "type": "reranker", "url": base + rerankPath, "api_key_env": "MINI_TEST_KEY", "format": rerankFormat},
		map[string]any{"name": "deepseek", "type": "llm", "url": base + "/v1/chat/completions", "api_key": "sk-llm", "api_model": "deepseek-chat"},
	}}
	data, _ := json.Marshal(doc)
	path := filepath.Join(t.TempDir(), "models.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	models, err := LoadDirectModels(path)
	if err != nil {
		t.Fatal(err)
	}
	return models
}

func call(t *testing.T, h http.Handler, method, path string, body any) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		rd = strings.NewReader(string(data))
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, rd))
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec, out
}

func TestDirectModelLookups(t *testing.T) {
	p := newProvider(t)
	h := newModel(ModelUpstream{}, writeModels(t, p.srv.URL, "", "/v1/rerank"))

	_, def := call(t, h, "GET", "/api/private/mf-model-manager/v1/small-model/get_default?model_type=embedding", nil)
	if def["model_name"] != "text-embedding-v4" || def["embedding_dim"] != float64(1024) || def["batch_size"] != float64(10) {
		t.Fatalf("default embedding = %v", def)
	}
	_, byID := call(t, h, "GET", "/api/private/mf-model-manager/v1/small-model/get?model_id="+def["model_id"].(string), nil)
	if byID["model_name"] != "text-embedding-v4" {
		t.Fatalf("get by id = %v", byID)
	}
	if _, ok := byID["api_key"]; ok {
		t.Fatal("lookup leaks the api key")
	}
	if rec, _ := call(t, h, "GET", "/api/private/mf-model-manager/v1/small-model/get_by_name?model_name=nope", nil); rec.Code != 404 {
		t.Fatalf("unknown name status = %d, want 404", rec.Code)
	}
	// The execution factory's fallback asks for model_name=embedding (a substring).
	_, list := call(t, h, "GET", "/api/private/mf-model-manager/v1/small-model/list?model_name=embedding&model_type=embedding", nil)
	if list["count"] != float64(1) {
		t.Fatalf("list = %v", list)
	}
	// LLMs are not small models.
	_, all := call(t, h, "GET", "/api/private/mf-model-manager/v1/small-model/list", nil)
	if all["count"] != float64(2) {
		t.Fatalf("list all = %v", all)
	}
}

func TestDirectEmbeddingsAndFlatRerank(t *testing.T) {
	p := newProvider(t)
	h := newModel(ModelUpstream{}, writeModels(t, p.srv.URL, "", "/v1/rerank"))

	// bkn-backend and vega send model_id with an empty model.
	_, def := call(t, h, "GET", "/api/private/mf-model-manager/v1/small-model/get_default?model_type=embedding", nil)
	rec, out := call(t, h, "POST", "/api/private/mf-model-api/v1/small-model/embeddings", map[string]any{"model": "", "model_id": def["model_id"], "input": []string{"a"}})
	if rec.Code != 200 || len(out["data"].([]any)) != 1 {
		t.Fatalf("embeddings = %d %v", rec.Code, out)
	}
	sent := p.got["/v1/embeddings"]
	if sent["model"] != "text-embedding-v4" || p.auth["/v1/embeddings"] != "Bearer sk-emb" {
		t.Fatalf("provider got %v auth %q", sent, p.auth["/v1/embeddings"])
	}
	if _, ok := sent["model_id"]; ok {
		t.Fatal("model_id forwarded to the provider")
	}

	rec, out = call(t, h, "POST", "/api/private/mf-model-api/v1/small-model/reranker", map[string]any{"model": "", "query": "q", "documents": []string{"x", "y"}})
	if rec.Code != 200 || out["results"].([]any)[0].(map[string]any)["index"] != float64(1) {
		t.Fatalf("rerank = %d %v", rec.Code, out)
	}
	if p.auth["/v1/rerank"] != "Bearer sk-env" || p.got["/v1/rerank"]["model"] != "gte-rerank-v2" {
		t.Fatalf("rerank provider got %v auth %q", p.got["/v1/rerank"], p.auth["/v1/rerank"])
	}
}

func TestDirectDashScopeRerank(t *testing.T) {
	p := newProvider(t)
	h := newModel(ModelUpstream{}, writeModels(t, p.srv.URL, "dashscope", "/dashscope/rerank"))

	rec, out := call(t, h, "POST", "/api/private/mf-model-api/v1/small-model/reranker", map[string]any{"query": "q", "documents": []string{"x", "y"}})
	if rec.Code != 200 {
		t.Fatalf("status = %d %s", rec.Code, rec.Body)
	}
	results := out["results"].([]any)
	if len(results) != 2 || results[0].(map[string]any)["index"] != float64(1) {
		t.Fatalf("results = %v", results)
	}
	input := p.got["/dashscope/rerank"]["input"].(map[string]any)
	if input["query"] != "q" || len(input["documents"].([]any)) != 2 {
		t.Fatalf("dashscope got %v", p.got["/dashscope/rerank"])
	}
}

func TestDirectChat(t *testing.T) {
	p := newProvider(t)
	h := newModel(ModelUpstream{}, writeModels(t, p.srv.URL, "", "/v1/rerank"))

	// The execution factory sends every field, zero values included.
	rec, out := call(t, h, "POST", "/api/private/mf-model-api/v1/chat/completions", map[string]any{
		"model": "", "model_id": "", "messages": []any{map[string]any{"role": "user", "content": "hi"}},
		"stream": false, "top_k": 0, "top_p": 0, "max_tokens": 0, "temperature": 0,
	})
	if rec.Code != 200 || out["choices"] == nil {
		t.Fatalf("chat = %d %v", rec.Code, out)
	}
	sent := p.got["/v1/chat/completions"]
	if sent["model"] != "deepseek-chat" || p.auth["/v1/chat/completions"] != "Bearer sk-llm" {
		t.Fatalf("provider got %v", sent)
	}
	for _, k := range []string{"model_id", "top_k", "top_p", "max_tokens"} {
		if _, ok := sent[k]; ok {
			t.Fatalf("%s forwarded: %v", k, sent)
		}
	}
	if _, ok := sent["temperature"]; !ok {
		t.Fatal("temperature 0 dropped")
	}
}

func TestDirectChatStreamsThroughLogWrapper(t *testing.T) {
	p := newProvider(t)
	srv := httptest.NewServer(logged("model", newModel(ModelUpstream{}, writeModels(t, p.srv.URL, "", "/v1/rerank"))))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/private/mf-model-api/v1/chat/completions", "application/json",
		strings.NewReader(`{"model":"deepseek","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("content type = %q", resp.Header.Get("Content-Type"))
	}
	var lines []string
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		if sc.Text() != "" {
			lines = append(lines, sc.Text())
		}
	}
	if len(lines) != 3 || lines[2] != "data: [DONE]" {
		t.Fatalf("stream = %q", lines)
	}
}

func TestLoadDirectModelsRejectsBadEntries(t *testing.T) {
	for name, body := range map[string]string{
		"missing url": `{"models":[{"name":"a","type":"llm"}]}`,
		"bad type":    `{"models":[{"name":"a","type":"chat","url":"http://x"}]}`,
		"no dim":      `{"models":[{"name":"a","type":"embedding","url":"http://x"}]}`,
		"bad format":  `{"models":[{"name":"a","type":"reranker","url":"http://x","format":"cohere"}]}`,
	} {
		path := filepath.Join(t.TempDir(), "m.json")
		_ = os.WriteFile(path, []byte(body), 0o600)
		if _, err := LoadDirectModels(path); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}
