package fakes

import (
	"archive/zip"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

func (s *sandbox) uploadFile(w http.ResponseWriter, r *http.Request) {
	sess := s.session(r.PathValue("id"))
	if sess == nil {
		notFound(w, "Session")
		return
	}
	if s.cfg.Workspace == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"detail": "openbkn mini runs without a function executor"})
		return
	}
	q := r.URL.Query()
	dir, err := relPath(q.Get("path"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	extract := q.Get("extract") == "true"
	overwrite := q.Get("overwrite") == "true"

	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes+1<<20)
	file, header, err := r.FormFile("file")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "multipart field 'file' is required"})
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxUploadBytes+1))
	if err != nil || len(data) > maxUploadBytes {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{"detail": "upload exceeds 100 MiB"})
		return
	}
	root := filepath.Join(s.cfg.Workspace, sessionDir(sess.id), filepath.FromSlash(dir))

	if !extract {
		name, err := relPath(filepath.Base(header.Filename))
		if err != nil || name == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "invalid file name"})
			return
		}
		target := filepath.Join(root, name)
		if err := writeFile(target, data, overwrite); err != nil && !os.IsExist(err) {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"session_id": sess.id, "mode": "file", "file_path": joinRel(dir, name), "size": len(data),
		})
		return
	}

	if !strings.HasSuffix(strings.ToLower(header.Filename), ".zip") &&
		!strings.Contains(header.Header.Get("Content-Type"), "zip") {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"detail": "extract=true needs a zip archive"})
		return
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"detail": "not a valid zip archive"})
		return
	}
	if len(zr.File) > maxArchiveEntries {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "archive has too many entries"})
		return
	}
	var total uint64
	for _, f := range zr.File {
		total += f.UncompressedSize64
		if f.Mode()&os.ModeSymlink != 0 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "archive must not contain symlinks: " + f.Name})
			return
		}
		if _, err := relPath(f.Name); err != nil && !f.FileInfo().IsDir() {
			writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "unsafe archive entry " + f.Name + ": " + err.Error()})
			return
		}
	}
	if total > maxArchiveBytes {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "archive expands beyond 512 MiB"})
		return
	}
	extracted, skipped := 0, 0
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		name, _ := relPath(f.Name)
		rc, err := f.Open()
		if err != nil {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"detail": err.Error()})
			return
		}
		content, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"detail": err.Error()})
			return
		}
		switch err := writeFile(filepath.Join(root, filepath.FromSlash(name)), content, overwrite); {
		case err == nil:
			extracted++
		case os.IsExist(err):
			skipped++
		default:
			writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"session_id": sess.id, "mode": "archive_extract", "extract_path": dir,
		"extracted_file_count": extracted, "skipped_file_count": skipped, "size": len(data),
	})
}

func joinRel(dir, name string) string {
	if dir == "" {
		return name
	}
	return dir + "/" + name
}

// writeFile writes data at path; with overwrite false an existing file is
// left alone and os.ErrExist is returned.
func writeFile(path string, data []byte, overwrite bool) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	flags := os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	if !overwrite {
		flags |= os.O_EXCL
	}
	f, err := os.OpenFile(path, flags, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func executionID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return "exec_" + time.Now().UTC().Format("20060102150405") + "_" + hex.EncodeToString(b)
}

// fastAPIError answers like FastAPI's request validation, which the
// execution factory parses for the code-too-long case.
func fastAPIError(w http.ResponseWriter, typ, field, msg string) {
	writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
		"detail": []any{map[string]any{"type": typ, "loc": []any{"body", field}, "msg": msg}},
	})
}

func (s *sandbox) executeSync(w http.ResponseWriter, r *http.Request) {
	sess := s.session(r.PathValue("id"))
	if sess == nil {
		notFound(w, "Session")
		return
	}
	var req struct {
		Code             string         `json:"code"`
		Language         string         `json:"language"`
		Timeout          *int           `json:"timeout"`
		Event            any            `json:"event"`
		EnvVars          map[string]any `json:"env_vars"`
		WorkingDirectory string         `json:"working_directory"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"detail": "invalid JSON body"})
		return
	}
	switch n := utf8.RuneCountInString(req.Code); {
	case n == 0:
		fastAPIError(w, "string_too_short", "code", "String should have at least 1 character")
		return
	case n > maxCodeLength:
		fastAPIError(w, "string_too_long", "code", fmt.Sprintf("String should have at most %d characters", maxCodeLength))
		return
	}
	if req.Language == "" {
		req.Language = "python"
	}
	if req.Language != "python" && req.Language != "javascript" && req.Language != "shell" {
		fastAPIError(w, "enum", "language", "Input should be 'python', 'javascript' or 'shell'")
		return
	}
	timeout := 30
	if req.Timeout != nil {
		timeout = *req.Timeout
	}
	if timeout < 1 || timeout > 3600 {
		fastAPIError(w, "less_than_equal", "timeout", "timeout must be between 1 and 3600")
		return
	}
	wd, err := relPath(req.WorkingDirectory)
	if err != nil {
		fastAPIError(w, "value_error", "working_directory", err.Error())
		return
	}
	syncTimeout := defaultSyncTimeout
	if v := r.URL.Query().Get("sync_timeout"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 10 && n <= 3600 {
			syncTimeout = n
		}
	}

	id := executionID()
	created := time.Now()
	if s.cfg.ExecutorURL == "" {
		code := 1
		writeJSON(w, http.StatusOK, executionView(id, sess.id, req.Code, req.Language, timeout, created, execResult{
			Status: "failed", ExitCode: &code,
			Stderr: "openbkn mini runs without a function executor",
		}))
		return
	}

	env := map[string]string{}
	for k, v := range sess.envVars {
		env[k] = v
	}
	for k, v := range req.EnvVars {
		env[k] = fmt.Sprint(v)
	}
	// mini has no sign-in: give in-sandbox SDK calls a token the stand-ins accept.
	if env["BKN_TOKEN"] == "" {
		env["BKN_TOKEN"] = "mini"
	}
	event := req.Event
	if event == nil {
		event = map[string]any{}
	}

	// Register before submitting: the executor may call back at once, and it
	// drops a result the control plane answers with 404.
	done := make(chan execResult, 1)
	s.mu.Lock()
	s.pending[id] = done
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
	}()

	body, _ := json.Marshal(map[string]any{
		"execution_id": id, "session_id": sess.id, "code": req.Code, "language": req.Language,
		"event": event, "timeout": timeout, "env_vars": env,
		// Sessions share one executor workspace, so each gets its own directory.
		"working_directory": filepath.ToSlash(filepath.Join(sessionDir(sess.id), wd)),
	})
	if err := s.submitExecution(body); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "Internal Server Error", "message": err.Error(), "detail": err.Error()})
		return
	}
	select {
	case res := <-done:
		writeJSON(w, http.StatusOK, executionView(id, sess.id, req.Code, req.Language, timeout, created, res))
	case <-time.After(time.Duration(syncTimeout) * time.Second):
		writeJSON(w, http.StatusRequestTimeout, map[string]any{"detail": fmt.Sprintf("Execution %s did not finish within %d seconds", id, syncTimeout)})
	case <-r.Context().Done():
	}
}

// submitExecution posts to the executor's /execute, retrying connection
// errors and 5xx like the control plane's executor client.
func (s *sandbox) submitExecution(body []byte) error {
	var last error
	for attempt, delay := range []time.Duration{0, 500 * time.Millisecond, time.Second} {
		time.Sleep(delay)
		resp, err := s.submit.Post(s.cfg.ExecutorURL+"/execute", "application/json", bytes.NewReader(body))
		if err != nil {
			last = fmt.Errorf("executor unavailable: %w", err)
			continue
		}
		data, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		switch {
		case resp.StatusCode == http.StatusOK:
			return nil
		case resp.StatusCode >= 500:
			last = fmt.Errorf("executor answered %d (attempt %d): %s", resp.StatusCode, attempt+1, data)
		default:
			return fmt.Errorf("executor rejected the execution (%d): %s", resp.StatusCode, data)
		}
	}
	return last
}

// executionView is the execute-sync response, shaped like the control plane's.
func executionView(id, sessionID, code, language string, timeout int, created time.Time, res execResult) map[string]any {
	status := res.Status
	switch status {
	case "success":
		status = "completed"
	case "":
		status = "failed"
	}
	view := map[string]any{
		"id": id, "session_id": sessionID, "status": status, "code": code, "language": language,
		"timeout": timeout, "exit_code": res.ExitCode, "error_message": nil, "execution_time": nil,
		"stdout": res.Stdout, "stderr": res.Stderr, "artifacts": []any{}, "retry_count": 0,
		"created_at": cpTime(created), "started_at": nil, "completed_at": cpTime(time.Now()),
		"return_value": nil, "metrics": nil,
	}
	switch status {
	case "completed":
		view["return_value"] = rawOrNil(res.ReturnValue)
		view["metrics"] = rawOrNil(res.Metrics)
		if len(res.Stdout)+len(res.Stderr)+len(res.ReturnValue) > maxOutputBytes {
			one := 1
			view["status"], view["exit_code"] = "failed", &one
			view["stdout"], view["stderr"], view["return_value"], view["metrics"] = "",
				"Sandbox.Execution.OutputLimitExceeded: output exceeds 4 MiB", nil, nil
		}
	case "timeout":
		view["stdout"], view["stderr"], view["exit_code"] = "", "", nil
	}
	return view
}

func rawOrNil(raw json.RawMessage) any {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	return raw
}

func (s *sandbox) receiveResult(w http.ResponseWriter, r *http.Request) {
	var res execResult
	if err := json.NewDecoder(r.Body).Decode(&res); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "invalid result payload"})
		return
	}
	switch res.Status {
	case "success", "failed", "timeout", "crashed":
	default:
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "unknown status " + res.Status})
		return
	}
	s.mu.Lock()
	done, ok := s.pending[r.PathValue("id")]
	s.mu.Unlock()
	if !ok {
		notFound(w, "Execution")
		return
	}
	select {
	case done <- res:
		writeJSON(w, http.StatusCreated, map[string]any{"message": "Result recorded successfully"})
	default:
		writeJSON(w, http.StatusOK, map[string]any{"message": "Result already recorded"})
	}
}
