package fakes

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// DirectModel is one model mini calls straight at its provider, without a
// model factory. Embeddings and chat use the OpenAI wire format; rerankers
// use the flat Jina/Cohere format unless Format is "dashscope".
type DirectModel struct {
	Name         string `json:"name"`          // what OpenBKN services ask for
	Type         string `json:"type"`          // embedding | reranker | llm
	URL          string `json:"url"`           // full endpoint, e.g. .../v1/embeddings
	APIKey       string `json:"api_key"`       // or APIKeyEnv
	APIKeyEnv    string `json:"api_key_env"`   // environment variable holding the key
	APIModel     string `json:"api_model"`     // provider's model name; defaults to Name
	Format       string `json:"format"`        // rerankers only: "" (flat) or "dashscope"
	EmbeddingDim int    `json:"embedding_dim"` // embeddings only
	BatchSize    int    `json:"batch_size"`    // embeddings only; callers batch
	MaxTokens    int    `json:"max_tokens"`    // embeddings only; callers truncate
	MaxDocuments int    `json:"max_documents"` // rerankers only
	Default      bool   `json:"default"`       // default model of its type
}

func (m DirectModel) id() string { return stableID("mini-model", m.Name) }

func (m DirectModel) key() string {
	if m.APIKeyEnv != "" {
		return os.Getenv(m.APIKeyEnv)
	}
	return m.APIKey
}

func (m DirectModel) apiModel() string {
	if m.APIModel != "" {
		return m.APIModel
	}
	return m.Name
}

// asSmallModel is the mf-model-manager view callers decode.
func (m DirectModel) asSmallModel() smallModel {
	return smallModel{
		"model_id": m.id(), "model_name": m.Name, "model_type": m.Type,
		"embedding_dim": m.EmbeddingDim, "batch_size": m.BatchSize, "max_tokens": m.MaxTokens,
		"max_documents": m.MaxDocuments, "default": m.Default,
	}
}

// LoadDirectModels reads the model list file: {"models": [DirectModel, ...]}.
func LoadDirectModels(path string) ([]DirectModel, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read models file: %w", err)
	}
	var doc struct {
		Models []DirectModel `json:"models"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse models file %s: %w", path, err)
	}
	defaults := map[string]int{}
	for i, m := range doc.Models {
		switch {
		case m.Name == "" || m.URL == "":
			return nil, fmt.Errorf("models file %s: model %d needs name and url", path, i)
		case m.Type != "embedding" && m.Type != "reranker" && m.Type != "llm":
			return nil, fmt.Errorf("models file %s: model %q has type %q, want embedding, reranker or llm", path, m.Name, m.Type)
		case m.Type == "embedding" && m.EmbeddingDim <= 0:
			return nil, fmt.Errorf("models file %s: embedding model %q needs embedding_dim", path, m.Name)
		case m.Format != "" && m.Format != "dashscope":
			return nil, fmt.Errorf("models file %s: model %q has format %q, want \"\" or \"dashscope\"", path, m.Name, m.Format)
		}
		if m.APIKeyEnv != "" && os.Getenv(m.APIKeyEnv) == "" {
			fmt.Fprintf(os.Stderr, "[mini] model %q: environment variable %s is empty\n", m.Name, m.APIKeyEnv)
		}
		if m.Default {
			defaults[m.Type]++
		}
	}
	// A type with one model needs no default flag; that model is the default.
	for i, m := range doc.Models {
		if defaults[m.Type] == 0 && countType(doc.Models, m.Type) == 1 {
			doc.Models[i].Default = true
		}
	}
	return doc.Models, nil
}

func countType(models []DirectModel, typ string) int {
	n := 0
	for _, m := range models {
		if m.Type == typ {
			n++
		}
	}
	return n
}

type directModels []DirectModel

func (d directModels) smallModels() []smallModel {
	out := []smallModel{}
	for _, m := range d {
		if m.Type != "llm" {
			out = append(out, m.asSmallModel())
		}
	}
	return out
}

// pick resolves a request's model: by name, then by id, then the default of
// the type. An unknown name falls back to the default too, so a service
// configured with the platform's model names still works.
func (d directModels) pick(typ, name, id string) (DirectModel, bool) {
	var def *DirectModel
	for i, m := range d {
		if m.Type != typ {
			continue
		}
		if (name != "" && m.Name == name) || (id != "" && m.id() == id) {
			return m, true
		}
		if m.Default && def == nil {
			def = &d[i]
		}
	}
	if def != nil {
		return *def, true
	}
	return DirectModel{}, false
}

var directClient = &http.Client{Transport: &http.Transport{
	Proxy:                 http.ProxyFromEnvironment,
	ResponseHeaderTimeout: 3 * time.Minute,
	IdleConnTimeout:       90 * time.Second,
}}

func (d directModels) register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/private/mf-model-api/v1/small-model/embeddings", d.embeddings)
	mux.HandleFunc("POST /api/private/mf-model-api/v1/small-model/reranker", d.rerank)
	mux.HandleFunc("POST /api/private/mf-model-api/v1/chat/completions", d.chat)
	mux.HandleFunc("POST /api/mf-model-api/v1/chat/completions", d.chat)
}

func (d directModels) embeddings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Model   string `json:"model"`
		ModelID string `json:"model_id"`
		Input   []any  `json:"input"`
	}
	readJSON(r, &req)
	m, ok := d.pick("embedding", req.Model, req.ModelID)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "no embedding model configured in mini"})
		return
	}
	if len(req.Input) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "input is empty"})
		return
	}
	forward(w, r, m, map[string]any{"model": m.apiModel(), "input": req.Input})
}

func (d directModels) rerank(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Model     string   `json:"model"`
		ModelID   string   `json:"model_id"`
		Query     string   `json:"query"`
		Documents []string `json:"documents"`
	}
	body, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(body, &req)
	m, ok := d.pick("reranker", req.Model, req.ModelID)
	if !ok {
		// No reranker: keep the caller's order.
		r.Body = io.NopCloser(bytes.NewReader(body))
		rerankInOrder(w, r)
		return
	}
	if m.Format != "dashscope" {
		forward(w, r, m, map[string]any{"model": m.apiModel(), "query": req.Query, "documents": req.Documents})
		return
	}
	// DashScope's native text-rerank API nests input and output.
	resp, err := post(r, m, map[string]any{
		"model":      m.apiModel(),
		"input":      map[string]any{"query": req.Query, "documents": req.Documents},
		"parameters": map[string]any{"return_documents": false, "top_n": len(req.Documents)},
	})
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(data)
		return
	}
	var out struct {
		Output struct {
			Results []struct {
				Index          int     `json:"index"`
				RelevanceScore float64 `json:"relevance_score"`
			} `json:"results"`
		} `json:"output"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": "unexpected rerank response: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": out.Output.Results})
}

func (d directModels) chat(w http.ResponseWriter, r *http.Request) {
	var req map[string]any
	readJSON(r, &req)
	if req == nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "body is not a JSON object"})
		return
	}
	name, _ := req["model"].(string)
	id, _ := req["model_id"].(string)
	m, ok := d.pick("llm", name, id)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "no llm configured in mini"})
		return
	}
	req["model"] = m.apiModel()
	// Fields the model factory accepts but OpenAI-style providers may reject,
	// and zero values the factory would have replaced with its defaults.
	delete(req, "model_id")
	delete(req, "top_k")
	for _, k := range []string{"top_p", "max_tokens"} {
		if v, ok := req[k].(float64); ok && v <= 0 {
			delete(req, k)
		}
	}
	forward(w, r, m, req)
}

func post(r *http.Request, m DirectModel, body any) (*http.Response, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	out, err := http.NewRequestWithContext(r.Context(), http.MethodPost, m.URL, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	out.Header.Set("Content-Type", "application/json")
	if accept := r.Header.Get("Accept"); accept != "" {
		out.Header.Set("Accept", accept)
	}
	if key := m.key(); key != "" {
		out.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := directClient.Do(out)
	if err != nil {
		return nil, fmt.Errorf("model %q at %s: %w", m.Name, m.URL, err)
	}
	return resp, nil
}

// forward posts body to the model's endpoint and streams the answer back
// unchanged, flushing as it arrives (chat completions may be SSE).
func forward(w http.ResponseWriter, r *http.Request, m DirectModel, body any) {
	resp, err := post(r, m, body)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "[fake:model] %s %s -> upstream %d\n", m.Type, m.Name, resp.StatusCode)
	}
	w.WriteHeader(resp.StatusCode)
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 32*1024)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if err != nil {
			return
		}
	}
}
