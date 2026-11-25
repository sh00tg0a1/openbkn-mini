package fakes

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// SandboxConfig points the sandbox stand-in at the platform's own function
// executor (infra/sandbox/runtime/executor). The stand-in plays the sandbox
// control plane for the execution factory: it keeps sessions, hands code to
// the executor and waits for the executor's result callback. With no
// ExecutorURL, nothing runs and every execution fails.
type SandboxConfig struct {
	ExecutorURL string // e.g. http://127.0.0.1:8080
	Workspace   string // the executor's WORKSPACE_PATH
}

const (
	maxCodeLength      = 102400
	maxOutputBytes     = 4 << 20
	maxUploadBytes     = 100 << 20
	maxArchiveEntries  = 10000
	maxArchiveBytes    = 512 << 20
	defaultSyncTimeout = 300
)

type sandboxSession struct {
	id, templateID string
	envVars        map[string]string
	timeout        int
	created        time.Time
}

type execResult struct {
	Status      string          `json:"status"`
	Stdout      string          `json:"stdout"`
	Stderr      string          `json:"stderr"`
	ExitCode    *int            `json:"exit_code"`
	ReturnValue json.RawMessage `json:"return_value"`
	Metrics     json.RawMessage `json:"metrics"`
}

type sandbox struct {
	cfg SandboxConfig

	mu        sync.Mutex
	sessions  map[string]*sandboxSession
	pending   map[string]chan execResult
	deps      map[string]string // lower-cased name -> pip spec, for the one shared executor
	installed []map[string]any
	installMu sync.Mutex // the executor wipes and reinstalls its venv on every sync

	submit  *http.Client
	install *http.Client
}

func newSandbox(cfg SandboxConfig) http.Handler {
	s := &sandbox{
		cfg:      cfg,
		sessions: map[string]*sandboxSession{},
		pending:  map[string]chan execResult{},
		deps:     map[string]string{},
		submit:   &http.Client{Timeout: 30 * time.Second},
		install:  &http.Client{Timeout: 10 * time.Minute},
	}
	mux := http.NewServeMux()
	const api = "/api/v1"
	mux.HandleFunc("POST "+api+"/sessions", s.createSession)
	mux.HandleFunc("GET "+api+"/sessions", s.listSessions)
	mux.HandleFunc("GET "+api+"/sessions/{id}", s.getSession)
	mux.HandleFunc("DELETE "+api+"/sessions/{id}", s.deleteSession)
	mux.HandleFunc("POST "+api+"/sessions/{id}/dependencies/install", s.installDependencies)
	mux.HandleFunc("POST "+api+"/sessions/{id}/files/upload", s.uploadFile)
	mux.HandleFunc("POST "+api+"/executions/sessions/{id}/execute-sync", s.executeSync)
	// Callbacks from the executor.
	mux.HandleFunc("POST "+api+"/internal/executions/{id}/result", s.receiveResult)
	mux.HandleFunc("POST "+api+"/internal/executions/{id}/heartbeat", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"message": "Heartbeat acknowledged"})
	})
	mux.HandleFunc("POST "+api+"/internal/containers/{event}", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"message": "Container event recorded"})
	})
	mux.HandleFunc("/", notImplemented)
	return mux
}

func cpTime(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000000") }

func notFound(w http.ResponseWriter, what string) {
	writeJSON(w, http.StatusNotFound, map[string]any{"error": "Not Found", "message": what + " not found", "detail": what + " not found"})
}

// sessionDir is where one session's files live inside the shared workspace.
func sessionDir(id string) string { return filepath.Join("sessions", id) }

func (s *sandbox) view(sess *sandboxSession) map[string]any {
	s.mu.Lock()
	installed := s.installed
	s.mu.Unlock()
	env := map[string]any{}
	for k, v := range sess.envVars {
		env[k] = v
	}
	now := cpTime(time.Now())
	v := map[string]any{
		"id": sess.id, "template_id": sess.templateID, "status": "running",
		"resource_limit": map[string]any{"cpu": "1", "memory": "512Mi", "disk": "1Gi", "max_processes": 128},
		"workspace_path": filepath.Join(s.cfg.Workspace, sessionDir(sess.id)),
		"runtime_type":   "process", "runtime_node": "mini", "pod_name": "mini",
		"env_vars": env, "timeout": sess.timeout,
		"created_at": cpTime(sess.created), "updated_at": now, "last_activity_at": now,
		"language_runtime": "python3.11",
	}
	if len(installed) > 0 {
		v["installed_dependencies"] = installed
		v["dependency_install_status"] = "completed"
	}
	return v
}

func (s *sandbox) createSession(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID         string            `json:"id"`
		TemplateID string            `json:"template_id"`
		Timeout    int               `json:"timeout"`
		EnvVars    map[string]string `json:"env_vars"`
	}
	readJSON(r, &req)
	if req.ID == "" {
		req.ID = "sess_" + strings.ReplaceAll(newUUID(), "-", "")[:16]
	}
	if req.TemplateID == "" {
		req.TemplateID = "python-basic"
	}
	if req.Timeout <= 0 {
		req.Timeout = 300
	}
	sess := &sandboxSession{id: req.ID, templateID: req.TemplateID, envVars: req.EnvVars, timeout: req.Timeout, created: time.Now()}
	s.mu.Lock()
	s.sessions[req.ID] = sess
	s.mu.Unlock()
	writeJSON(w, http.StatusCreated, s.view(sess))
}

func (s *sandbox) session(id string) *sandboxSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions[id]
}

func (s *sandbox) getSession(w http.ResponseWriter, r *http.Request) {
	sess := s.session(r.PathValue("id"))
	if sess == nil {
		notFound(w, "Session")
		return
	}
	writeJSON(w, http.StatusOK, s.view(sess))
}

func (s *sandbox) listSessions(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	all := make([]*sandboxSession, 0, len(s.sessions))
	for _, sess := range s.sessions {
		all = append(all, sess)
	}
	s.mu.Unlock()
	sort.Slice(all, func(i, j int) bool { return all[i].id < all[j].id })
	if st := r.URL.Query().Get("status"); st != "" && st != "running" {
		all = nil
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 50
	}
	total := len(all)
	offset = min(max(offset, 0), total)
	end := min(offset+limit, total)
	items := make([]any, 0, end-offset)
	for _, sess := range all[offset:end] {
		items = append(items, s.view(sess))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": total, "limit": limit, "offset": offset, "has_more": end < total})
}

func (s *sandbox) deleteSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.mu.Lock()
	_, ok := s.sessions[id]
	delete(s.sessions, id)
	s.mu.Unlock()
	if !ok {
		notFound(w, "Session")
		return
	}
	if s.cfg.Workspace != "" {
		_ = os.RemoveAll(filepath.Join(s.cfg.Workspace, sessionDir(id)))
	}
	w.WriteHeader(http.StatusNoContent)
}

// pipSpec turns {name, version} into a pip requirement, as the control plane does.
func pipSpec(name, version string) string {
	version = strings.TrimSpace(version)
	if version == "" {
		return name
	}
	for _, op := range []string{"===", "==", ">=", "<=", "~=", "!=", ">", "<"} {
		if strings.HasPrefix(version, op) {
			return name + version
		}
	}
	return name + "==" + version
}

func (s *sandbox) installDependencies(w http.ResponseWriter, r *http.Request) {
	sess := s.session(r.PathValue("id"))
	if sess == nil {
		notFound(w, "Session")
		return
	}
	var req struct {
		Dependencies []struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"dependencies"`
		IndexURL       string `json:"python_package_index_url"`
		InstallTimeout int    `json:"install_timeout"`
	}
	readJSON(r, &req)
	if len(req.Dependencies) == 0 || len(req.Dependencies) > 50 {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"detail": "dependencies must list 1 to 50 packages"})
		return
	}
	if s.cfg.ExecutorURL == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"detail": "openbkn mini runs without a function executor"})
		return
	}
	// One executor serves every session, and each sync replaces its whole
	// dependency directory, so send the union of everything asked for.
	s.installMu.Lock()
	defer s.installMu.Unlock()
	s.mu.Lock()
	for _, d := range req.Dependencies {
		s.deps[strings.ToLower(strings.TrimSpace(d.Name))] = pipSpec(strings.TrimSpace(d.Name), d.Version)
	}
	specs := make([]string, 0, len(s.deps))
	for _, spec := range s.deps {
		specs = append(specs, spec)
	}
	s.mu.Unlock()
	sort.Strings(specs)

	body, _ := json.Marshal(map[string]any{
		"session_id": sess.id, "language_runtime": "python3.11", "python_package_index_url": req.IndexURL,
		"dependencies": specs, "sync_mode": "merge",
	})
	resp, err := s.install.Post(s.cfg.ExecutorURL+"/internal/session-config/sync", "application/json", bytes.NewReader(body))
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"detail": "executor unavailable: " + err.Error()})
		return
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	switch {
	case resp.StatusCode == http.StatusOK:
	case resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnprocessableEntity:
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "dependency installation failed: " + string(data)})
		return
	default:
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"detail": fmt.Sprintf("executor answered %d: %s", resp.StatusCode, data)})
		return
	}
	var out struct {
		Installed []map[string]any `json:"installed_dependencies"`
	}
	_ = json.Unmarshal(data, &out)
	s.mu.Lock()
	s.installed = out.Installed
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, s.view(sess))
}

// relPath checks a client-supplied relative path the way the control plane does.
func relPath(p string) (string, error) {
	p = strings.TrimPrefix(strings.TrimSpace(p), "./")
	if p == "" {
		return "", nil
	}
	if strings.HasPrefix(p, "/") || strings.Contains(p, "\\") {
		return "", errors.New("path must be relative")
	}
	for _, seg := range strings.Split(strings.TrimSuffix(p, "/"), "/") {
		if seg == "" || seg == "." || seg == ".." {
			return "", errors.New("path must not contain empty, '.' or '..' segments")
		}
	}
	return strings.TrimSuffix(p, "/"), nil
}
