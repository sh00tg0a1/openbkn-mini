package fakes

import (
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

const ossStorageID = "mini-local"

// OSSBlobPrefix is the path under which the gateway exposes stored files.
const OSSBlobPrefix = "/mini-oss/blob/"

// newOSS stands in for oss-gateway-backend. A client first asks the gateway
// for a signed request (method + URL) and then sends it; here the "signed" URL
// points back at this fake, which keeps the bytes in a local directory.
// publicBase is how clients reach the blob route (normally the mini gateway).
func newOSS(dir, publicBase string) http.Handler {
	mux := http.NewServeMux()
	const api = "/api/v1"

	mux.HandleFunc("GET "+api+"/storages", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"count": 1,
			"data":  []any{map[string]any{"storage_id": ossStorageID, "is_default": true, "is_enabled": true}},
		})
	})
	sign := func(method string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			blob := strings.TrimRight(publicBase, "/") + OSSBlobPrefix +
				url.PathEscape(r.PathValue("storage")) + "/" + url.PathEscape(r.PathValue("key"))
			writeJSON(w, http.StatusOK, map[string]any{
				"data": map[string]any{"method": method, "url": blob, "headers": map[string]string{}},
			})
		}
	}
	mux.HandleFunc("GET "+api+"/upload/{storage}/{key}", func(w http.ResponseWriter, r *http.Request) {
		method := r.URL.Query().Get("request_method")
		if method == "" {
			method = http.MethodPut
		}
		sign(method)(w, r)
	})
	mux.HandleFunc("GET "+api+"/download/{storage}/{key}", sign(http.MethodGet))
	mux.HandleFunc("GET "+api+"/delete/{storage}/{key}", sign(http.MethodDelete))

	blobPath := func(r *http.Request) (string, bool) {
		storage, key := r.PathValue("storage"), r.PathValue("key")
		if storage == "" || key == "" || strings.Contains(storage, "..") {
			return "", false
		}
		// The key may contain slashes; store it as one flat, escaped file name.
		return filepath.Join(dir, storage, url.PathEscape(key)), true
	}
	mux.HandleFunc("PUT "+OSSBlobPrefix+"{storage}/{key}", func(w http.ResponseWriter, r *http.Request) {
		p, ok := blobPath(r)
		if !ok {
			http.Error(w, "bad object path", http.StatusBadRequest)
			return
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		f, err := os.Create(p)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer f.Close()
		if _, err := io.Copy(f, r.Body); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET "+OSSBlobPrefix+"{storage}/{key}", func(w http.ResponseWriter, r *http.Request) {
		p, ok := blobPath(r)
		if !ok {
			http.Error(w, "bad object path", http.StatusBadRequest)
			return
		}
		http.ServeFile(w, r, p)
	})
	mux.HandleFunc("DELETE "+OSSBlobPrefix+"{storage}/{key}", func(w http.ResponseWriter, r *http.Request) {
		p, ok := blobPath(r)
		if !ok {
			http.Error(w, "bad object path", http.StatusBadRequest)
			return
		}
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/", notImplemented)
	return mux
}
