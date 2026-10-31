package fakes

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// newTrace stands in for the BKN Trace core. It always behaves as a healthy,
// capture-enabled core so Context Loader keeps issuing bkn_context ids; any
// evidence it receives is dropped. State lives in memory only.
func newTrace() http.Handler {
	t := &traceStore{
		interactions: map[string]map[string]any{},
		operations:   map[string]map[string]any{},
		receipts:     map[string]map[string]any{},
	}
	mux := http.NewServeMux()
	const p = "/api/agent-observability/v1"

	mux.HandleFunc("POST "+p+"/conversations:ensure-current", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Key string `json:"external_conversation_key"`
		}
		readJSON(r, &req)
		id := newUUID()
		if req.Key != "" {
			id = stableID("conversation", req.Key)
		}
		now := time.Now().UTC()
		writeJSON(w, http.StatusOK, map[string]any{
			"conversation_id": id, "owner": traceOwner(), "external_conversation_key": req.Key,
			"status": "active", "generation": 1, "one_shot": false, "row_version": 1,
			"created_at": now, "updated_at": now,
		})
	})
	mux.HandleFunc("POST "+p+"/conversations/{cid}/interactions", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, t.newInteraction(r.PathValue("cid")))
	})
	mux.HandleFunc("GET "+p+"/interactions/{iid}", func(w http.ResponseWriter, r *http.Request) {
		if it := t.interaction(r.PathValue("iid")); it != nil {
			writeJSON(w, http.StatusOK, it)
			return
		}
		writeJSON(w, http.StatusNotFound, map[string]any{"code": "interaction_required", "message": "unknown interaction"})
	})
	mux.HandleFunc("POST "+p+"/interactions/{iid}/finish", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Outcome string `json:"outcome"`
		}
		readJSON(r, &req)
		// Core persists "cancelled" as "canceled"; the caller checks the echo.
		status := req.Outcome
		if status == "cancelled" {
			status = "canceled"
		}
		it := t.interaction(r.PathValue("iid"))
		if it == nil {
			it = map[string]any{"interaction_id": r.PathValue("iid")}
		}
		out := map[string]any{}
		for k, v := range it {
			out[k] = v
		}
		if status != "" {
			out["execution_status"] = status
		}
		out["terminal_at"] = time.Now().UTC()
		writeJSON(w, http.StatusOK, out)
	})
	mux.HandleFunc("POST "+p+"/conversations/{cid}/interactions/{iid}/operations:ensure", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			OperationKey      string   `json:"operation_key"`
			ToolName          string   `json:"tool_name"`
			ParentOperationID string   `json:"parent_operation_id"`
			CausationEventIDs []string `json:"causation_event_ids"`
		}
		readJSON(r, &req)
		writeJSON(w, http.StatusOK, t.ensureOperation(r.PathValue("cid"), r.PathValue("iid"), req.OperationKey, req.ToolName, req.ParentOperationID))
	})
	mux.HandleFunc("GET "+p+"/receipts/{rid}", func(w http.ResponseWriter, r *http.Request) {
		if rc := t.receipt(r.PathValue("rid")); rc != nil {
			writeJSON(w, http.StatusOK, rc)
			return
		}
		writeJSON(w, http.StatusNotFound, map[string]any{"code": "not_found"})
	})
	// /operations/{oid}, /operations/{oid}/attempts, /operations/{oid}/attempts/{n}:{complete|fail}
	mux.HandleFunc(p+"/operations/", func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, p+"/operations/")
		parts := strings.Split(rest, "/")
		op := t.operation(parts[0])
		if op == nil {
			writeJSON(w, http.StatusNotFound, map[string]any{"code": "not_found"})
			return
		}
		switch {
		case len(parts) == 1 && r.Method == http.MethodGet:
			writeJSON(w, http.StatusOK, op)
		case len(parts) == 2 && parts[1] == "attempts" && r.Method == http.MethodPost:
			writeJSON(w, http.StatusOK, t.result(parts[0], "running", "pending", true, false))
		case len(parts) == 3 && parts[1] == "attempts" && r.Method == http.MethodPost:
			action := parts[2][strings.LastIndex(parts[2], ":")+1:]
			status := "succeeded"
			if action == "fail" {
				status = "failed"
			}
			writeJSON(w, http.StatusOK, t.result(parts[0], status, "issued", false, false))
		default:
			notImplemented(w, r)
		}
	})
	// Evidence artifact ingest: accept and drop.
	mux.HandleFunc("POST /artifacts", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"artifact_ref": "mini://dropped/" + newUUID()})
	})
	mux.HandleFunc("/", notImplemented)
	return mux
}

type traceStore struct {
	mu           sync.Mutex
	interactions map[string]map[string]any
	operations   map[string]map[string]any
	receipts     map[string]map[string]any
	ordinal      uint64
}

func (t *traceStore) newInteraction(conversationID string) map[string]any {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.ordinal++
	now := time.Now().UTC()
	capture := true
	it := map[string]any{
		"interaction_id": newUUID(), "conversation_id": conversationID, "capture_enabled": &capture,
		"ordinal": t.ordinal, "execution_status": "active", "evidence_status": "pending",
		"lease_token": newUUID(), "lease_epoch": 1, "lease_version": 1,
		"lease_expires_at": now.Add(24 * time.Hour), "row_version": 1,
		"created_at": now, "updated_at": now,
	}
	t.interactions[it["interaction_id"].(string)] = it
	return it
}

func (t *traceStore) interaction(id string) map[string]any {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.interactions[id]
}

func (t *traceStore) operation(id string) map[string]any {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.operations[id]
}

func (t *traceStore) receipt(id string) map[string]any {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.receipts[id]
}

func (t *traceStore) ensureOperation(cid, iid, key, tool, parent string) map[string]any {
	t.mu.Lock()
	opID := stableID("operation", iid, key)
	_, exists := t.operations[opID]
	if !exists {
		now := time.Now().UTC()
		t.operations[opID] = map[string]any{
			"operation_id": opID, "conversation_id": cid, "interaction_id": iid,
			"operation_key": key, "tool_name": tool, "parent_operation_id": parent,
			"attempt": 1, "attempt_status": "running", "retryable": false, "row_version": 1,
			"created_at": now, "updated_at": now,
		}
		t.receipts[stableID("receipt", opID)] = map[string]any{
			"receipt_id": stableID("receipt", opID), "schema_version": "1", "owner": traceOwner(),
			"conversation_id": cid, "interaction_id": iid, "operation_id": opID, "attempt": 1,
			"operation_key": key, "tool_name": tool, "receipt_status": "pending",
			"evidence_durability": "pending", "required": true, "request_id": "", "trace_id": "",
			"causation_event_ids": []string{}, "observed_evidence_refs": []string{},
			"business_refs": []any{}, "artifact_refs": []string{}, "partial_reasons": []string{},
			"row_version": 1, "issued_at": now,
		}
	}
	t.mu.Unlock()
	return t.result(opID, "running", "pending", true, !exists)
}

func (t *traceStore) result(opID, attemptStatus, receiptStatus string, execute, created bool) map[string]any {
	t.mu.Lock()
	defer t.mu.Unlock()
	op := t.operations[opID]
	op["attempt_status"] = attemptStatus
	rc := t.receipts[stableID("receipt", opID)]
	rc["receipt_status"] = receiptStatus
	return map[string]any{"operation": op, "receipt": rc, "created": created, "execute": execute}
}

func traceOwner() map[string]any {
	return map[string]any{
		"application_principal_id": "openbkn-mini",
		"effective_subject_type":   "user",
		"effective_subject_id":     SuperAdminID,
	}
}

func newUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

var _ = strconv.Itoa
