package fakes

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// fakeExecutor answers /execute like the platform executor: it reports the
// result to the control plane by callback, here before it even replies.
type fakeExecutor struct {
	srv      *httptest.Server
	cpURL    string
	mu       sync.Mutex
	executed []map[string]any
	synced   []map[string]any
	result   func(req map[string]any) map[string]any
}

func newFakeExecutor(t *testing.T) *fakeExecutor {
	e := &fakeExecutor{}
	e.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		switch r.URL.Path {
		case "/execute":
			e.mu.Lock()
			e.executed = append(e.executed, req)
			e.mu.Unlock()
			body, _ := json.Marshal(e.result(req))
			resp, err := http.Post(e.cpURL+"/api/v1/internal/executions/"+req["execution_id"].(string)+"/result", "application/json", bytes.NewReader(body))
			if err != nil || resp.StatusCode >= 300 {
				t.Errorf("callback failed: %v %v", err, resp)
			}
			writeJSON(w, 200, map[string]any{"execution_id": req["execution_id"], "status": "PENDING"})
		case "/internal/session-config/sync":
			e.mu.Lock()
			e.synced = append(e.synced, req)
			e.mu.Unlock()
			writeJSON(w, 200, map[string]any{"status": "completed", "installed_dependencies": []any{
				map[string]any{"name": "requests", "version": "2.31.0", "install_location": "/opt/sandbox-venv"},
			}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(e.srv.Close)
	return e
}

func newTestSandbox(t *testing.T) (*httptest.Server, *fakeExecutor, string) {
	exec := newFakeExecutor(t)
	workspace := t.TempDir()
	cp := httptest.NewServer(newSandbox(SandboxConfig{ExecutorURL: exec.srv.URL, Workspace: workspace}))
	t.Cleanup(cp.Close)
	exec.cpURL = cp.URL
	return cp, exec, workspace
}

func do(t *testing.T, method, url string, body any) (int, map[string]any) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		rd = bytes.NewReader(data)
	}
	req, _ := http.NewRequest(method, url, rd)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestSandboxSessionLifecycle(t *testing.T) {
	cp, _, _ := newTestSandbox(t)
	if code, _ := do(t, "GET", cp.URL+"/api/v1/sessions/sess_aoi_0", nil); code != 404 {
		t.Fatalf("missing session = %d, want 404 (the pool recreates on 404)", code)
	}
	code, s := do(t, "POST", cp.URL+"/api/v1/sessions", map[string]any{"id": "sess_aoi_0", "template_id": "python-basic", "timeout": 3600})
	if code != 201 || s["status"] != "running" {
		t.Fatalf("create = %d %v", code, s)
	}
	_, list := do(t, "GET", cp.URL+"/api/v1/sessions?limit=3", nil)
	if list["total"] != float64(1) {
		t.Fatalf("list = %v", list)
	}
	req, _ := http.NewRequest("DELETE", cp.URL+"/api/v1/sessions/sess_aoi_0", nil)
	resp, _ := http.DefaultClient.Do(req)
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 204 || len(body) != 0 {
		t.Fatalf("delete = %d %q, want 204 with no body", resp.StatusCode, body)
	}
	// The pool deletes and later re-creates the same id.
	if code, _ := do(t, "POST", cp.URL+"/api/v1/sessions", map[string]any{"id": "sess_aoi_0", "timeout": 3600}); code != 201 {
		t.Fatalf("re-create = %d", code)
	}
}

func TestSandboxExecuteSync(t *testing.T) {
	cp, exec, _ := newTestSandbox(t)
	exec.result = func(req map[string]any) map[string]any {
		return map[string]any{"status": "success", "stdout": "hi\n", "stderr": "", "exit_code": 0, "execution_time": 0.25,
			"return_value": map[string]any{"x": 1}, "metrics": map[string]any{"duration_ms": 250.0}}
	}
	do(t, "POST", cp.URL+"/api/v1/sessions", map[string]any{"id": "sess_aoi_0", "timeout": 3600, "env_vars": map[string]any{"source": "function_proxy"}})

	code, out := do(t, "POST", cp.URL+"/api/v1/executions/sessions/sess_aoi_0/execute-sync", map[string]any{
		"code": "def handler(event): return {'x': 1}", "language": "python", "timeout": 10,
		"event": map[string]any{"a": 1}, "env_vars": map[string]any{"BKN_TOKEN": "", "user_id": "u1"},
		"working_directory": "skills/s1",
	})
	if code != 200 || out["status"] != "completed" || out["stdout"] != "hi\n" {
		t.Fatalf("execute = %d %v", code, out)
	}
	if out["exit_code"] != float64(0) || out["return_value"].(map[string]any)["x"] != float64(1) {
		t.Fatalf("result = %v", out)
	}
	if out["execution_time"] != nil || len(out["artifacts"].([]any)) != 0 {
		t.Fatalf("execution_time/artifacts should mirror the control plane: %v", out)
	}
	sent := exec.executed[0]
	env := sent["env_vars"].(map[string]any)
	if env["source"] != "function_proxy" || env["user_id"] != "u1" || env["BKN_TOKEN"] != "mini" {
		t.Fatalf("env sent to executor = %v", env)
	}
	if sent["working_directory"] != "sessions/sess_aoi_0/skills/s1" {
		t.Fatalf("working_directory = %v", sent["working_directory"])
	}
}

func TestSandboxExecuteStatuses(t *testing.T) {
	cp, exec, _ := newTestSandbox(t)
	do(t, "POST", cp.URL+"/api/v1/sessions", map[string]any{"id": "s", "timeout": 60})
	for status, check := range map[string]func(map[string]any) bool{
		"failed": func(o map[string]any) bool {
			return o["status"] == "failed" && o["stderr"] == "boom" && o["return_value"] == nil
		},
		"timeout": func(o map[string]any) bool {
			return o["status"] == "timeout" && o["stdout"] == "" && o["exit_code"] == nil
		},
	} {
		exec.result = func(map[string]any) map[string]any {
			return map[string]any{"status": status, "stdout": "partial", "stderr": "boom", "exit_code": 1, "return_value": map[string]any{"x": 1}}
		}
		_, out := do(t, "POST", cp.URL+"/api/v1/executions/sessions/s/execute-sync", map[string]any{"code": "x", "language": "python"})
		if !check(out) {
			t.Errorf("%s: %v", status, out)
		}
	}
	if code, _ := do(t, "POST", cp.URL+"/api/v1/executions/sessions/nope/execute-sync", map[string]any{"code": "x"}); code != 404 {
		t.Errorf("unknown session = %d", code)
	}
	_, out := do(t, "POST", cp.URL+"/api/v1/executions/sessions/s/execute-sync", map[string]any{"code": strings.Repeat("x", maxCodeLength+1)})
	detail, _ := out["detail"].([]any)
	if len(detail) != 1 || detail[0].(map[string]any)["type"] != "string_too_long" {
		t.Errorf("code too long = %v", out)
	}
	if code, _ := do(t, "POST", cp.URL+"/api/v1/executions/sessions/s/execute-sync", map[string]any{"code": "x", "working_directory": "../etc"}); code != 422 {
		t.Errorf("escaping working_directory = %d", code)
	}
}

func TestSandboxDependenciesAreMerged(t *testing.T) {
	cp, exec, _ := newTestSandbox(t)
	do(t, "POST", cp.URL+"/api/v1/sessions", map[string]any{"id": "a", "timeout": 60})
	do(t, "POST", cp.URL+"/api/v1/sessions", map[string]any{"id": "b", "timeout": 60})
	do(t, "POST", cp.URL+"/api/v1/sessions/a/dependencies/install", map[string]any{
		"dependencies": []any{map[string]any{"name": "requests", "version": "2.31.0"}}, "python_package_index_url": "https://pypi.org/simple/"})
	code, out := do(t, "POST", cp.URL+"/api/v1/sessions/b/dependencies/install", map[string]any{
		"dependencies": []any{map[string]any{"name": "pandas", "version": ""}, map[string]any{"name": "Requests", "version": ">=2.32"}}})
	if code != 200 || len(out["installed_dependencies"].([]any)) != 1 {
		t.Fatalf("install = %d %v", code, out)
	}
	last := exec.synced[len(exec.synced)-1]["dependencies"].([]any)
	if len(last) != 2 || last[0] != "Requests>=2.32" || last[1] != "pandas" {
		t.Fatalf("second sync sent %v, want the union with the later spec winning", last)
	}
}

func uploadZip(t *testing.T, url string, files map[string]string) (int, map[string]any) {
	t.Helper()
	var zbuf bytes.Buffer
	zw := zip.NewWriter(&zbuf)
	for name, content := range files {
		f, _ := zw.Create(name)
		_, _ = f.Write([]byte(content))
	}
	_ = zw.Close()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, _ := mw.CreateFormFile("file", "skill.zip")
	_, _ = part.Write(zbuf.Bytes())
	_ = mw.Close()
	resp, err := http.Post(url, mw.FormDataContentType(), &body)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestSandboxUploadExtractsIntoTheSessionWorkspace(t *testing.T) {
	cp, _, workspace := newTestSandbox(t)
	do(t, "POST", cp.URL+"/api/v1/sessions", map[string]any{"id": "s", "timeout": 60})
	url := cp.URL + "/api/v1/sessions/s/files/upload?path=skills/k1&extract=true&overwrite=false"
	code, out := uploadZip(t, url, map[string]string{"SKILL.md": "# k1", "scripts/run.sh": "echo hi"})
	if code != 200 || out["extracted_file_count"] != float64(2) || out["extract_path"] != "skills/k1" {
		t.Fatalf("upload = %d %v", code, out)
	}
	data, err := os.ReadFile(filepath.Join(workspace, "sessions", "s", "skills", "k1", "scripts", "run.sh"))
	if err != nil || string(data) != "echo hi" {
		t.Fatalf("extracted file = %q %v", data, err)
	}
	// overwrite=false keeps what is there.
	_, out = uploadZip(t, url, map[string]string{"SKILL.md": "# changed"})
	if out["skipped_file_count"] != float64(1) {
		t.Fatalf("re-upload = %v", out)
	}
	if code, _ := uploadZip(t, url, map[string]string{"../escape.txt": "x"}); code != 400 {
		t.Fatalf("path traversal entry = %d, want 400", code)
	}
}

func TestSandboxWithoutExecutorFailsExecutions(t *testing.T) {
	cp := httptest.NewServer(newSandbox(SandboxConfig{}))
	defer cp.Close()
	do(t, "POST", cp.URL+"/api/v1/sessions", map[string]any{"id": "s", "timeout": 60})
	_, out := do(t, "POST", cp.URL+"/api/v1/executions/sessions/s/execute-sync", map[string]any{"code": "x"})
	if out["status"] != "failed" || out["exit_code"] != float64(1) {
		t.Fatalf("without executor = %v", out)
	}
}
