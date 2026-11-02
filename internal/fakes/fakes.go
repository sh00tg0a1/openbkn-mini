// Package fakes provides in-process stand-ins for the platform services mini
// does not run: Hydra, bkn-safe, the model factory, NSQ and BKN Trace. Every
// caller is treated as the platform's built-in super admin.
package fakes

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	SuperAdminID     = "266c6a42-6131-4d62-8f39-853e7093701c"
	SuperAdminRoleID = "7dcfcc9c-ad02-11e8-aa06-000c29358ad6"
	SuperAdminName   = "admin"
)

// Config holds listen addresses and data files for the fakes.
type Config struct {
	SafeAddr     string        // bkn-safe
	HydraAddr    string        // Hydra admin
	ModelAddr    string        // mf-model-api and mf-model-manager
	NSQAddr      string        // nsqd HTTP publish
	TraceAddr    string        // BKN Trace core
	SandboxAddr  string        // sandbox control plane
	OSSAddr      string        // oss-gateway-backend
	OSSDir       string        // where the OSS fake keeps files
	PublicURL    string        // how clients reach mini (the gateway), for OSS download links
	Models       ModelUpstream // forward to another OpenBKN platform
	DirectModels []DirectModel // or call providers directly; wins over Models
	RegistryFile string        // bkn-safe authorization-registry.json
}

// Start launches every fake in its own goroutine.
func Start(cfg Config) error {
	reg, err := loadRegistry(cfg.RegistryFile)
	if err != nil {
		return err
	}
	servers := map[string]http.Handler{
		cfg.SafeAddr:    logged("safe", newSafe(reg)),
		cfg.HydraAddr:   logged("hydra", newHydra()),
		cfg.ModelAddr:   logged("model", newModel(cfg.Models, cfg.DirectModels)),
		cfg.NSQAddr:     logged("nsq", newNSQ()),
		cfg.TraceAddr:   logged("trace", newTrace()),
		cfg.SandboxAddr: logged("sandbox", newSandbox()),
		cfg.OSSAddr:     logged("oss", newOSS(cfg.OSSDir, cfg.PublicURL)),
	}
	for addr, h := range servers {
		srv := &http.Server{Addr: addr, Handler: h, ReadHeaderTimeout: 10 * time.Second}
		go func(addr string) {
			if err := srv.ListenAndServe(); err != nil {
				fmt.Fprintf(os.Stderr, "[mini] FATAL fake on %s: %v\n", addr, err)
				os.Exit(1)
			}
		}(addr)
	}
	return nil
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Flush and Unwrap keep streamed responses (SSE chat) flowing through the log wrapper.
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// logged prints unimplemented routes always, and every request when
// MINI_FAKE_LOG is set.
func logged(name string, h http.Handler) http.Handler {
	verbose := os.Getenv("MINI_FAKE_LOG") != ""
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w, status: 200}
		h.ServeHTTP(rec, r)
		if verbose || rec.status == http.StatusNotImplemented {
			fmt.Fprintf(os.Stderr, "[fake:%s] %s %s -> %d\n", name, r.Method, r.URL.RequestURI(), rec.status)
		}
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func readJSON(r *http.Request, v any) {
	body, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(body, v)
}

func notImplemented(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusNotImplemented, map[string]any{"error": "not implemented in mini", "path": r.URL.Path})
}

func stableID(parts ...string) string {
	sum := sha1.Sum([]byte(strings.Join(parts, "\x00")))
	h := hex.EncodeToString(sum[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

type registry struct {
	raw json.RawMessage
	ops map[string][]string // resource type -> operation ids
}

func loadRegistry(path string) (*registry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read authorization registry: %w", err)
	}
	var doc struct {
		ResourceTypes []struct {
			ID         string `json:"id"`
			Operations []struct {
				ID string `json:"id"`
			} `json:"operations"`
		} `json:"resource_types"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse authorization registry: %w", err)
	}
	reg := &registry{raw: data, ops: map[string][]string{}}
	for _, t := range doc.ResourceTypes {
		for _, op := range t.Operations {
			reg.ops[t.ID] = append(reg.ops[t.ID], op.ID)
		}
	}
	return reg, nil
}

func (r *registry) operations(resourceType string) []string {
	if ops, ok := r.ops[resourceType]; ok {
		return ops
	}
	return []string{}
}
