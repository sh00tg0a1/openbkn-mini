package safe

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func testConfig(t *testing.T) Config {
	t.Helper()
	return Config{
		PublicURL:       "http://127.0.0.1:8010",
		SafeAddr:        "127.0.0.1:3000",
		HydraPublicAddr: "127.0.0.1:4444",
		HydraAdminAddr:  "127.0.0.1:4445",
		HydraBin:        "hydra",
		HydraDir:        t.TempDir(),
		Database:        Database{Host: "127.0.0.1", Port: "3306", User: "root", Password: "from-config"},
		Upstreams: Upstreams{
			BKNBackend: "127.0.0.1:13015", ExecutionFactory: "127.0.0.1:9000",
			VegaBackend: "127.0.0.1:13014", OntologyQuery: "127.0.0.1:13018",
		},
	}
}

func TestSetenvKeepsWhatIsAlreadySet(t *testing.T) {
	cfg := testConfig(t)
	for k := range Env(cfg) {
		// Restores the variable after the test whatever Setenv does to it.
		t.Setenv(k, "")
		_ = os.Unsetenv(k)
	}
	t.Setenv("SAFE_DB_PASSWORD", "mine")
	t.Setenv("SAFE_LICENSE_SERVER_URL", "https://license.example")
	Setenv(cfg)
	for k, want := range map[string]string{
		"SAFE_DB_PASSWORD":              "mine",
		"SAFE_LICENSE_SERVER_URL":       "https://license.example",
		"SAFE_DB_HOST":                  "127.0.0.1",
		"SAFE_DB_USER":                  "root",
		"SAFE_HTTP_ADDR":                "127.0.0.1:3000",
		"SAFE_HYDRA_ADMIN_URL":          "http://127.0.0.1:4445",
		"SAFE_HYDRA_BROWSER_PUBLIC_URL": "http://127.0.0.1:8010",
		"SAFE_BKN_BACKEND_BASE_URL":     "http://127.0.0.1:13015",
	} {
		if got := os.Getenv(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	var redirects []string
	if err := json.Unmarshal([]byte(os.Getenv("SAFE_OAUTH_STUDIO_BASELINE_REDIRECT_URIS")), &redirects); err != nil ||
		!slices.Equal(redirects, []string{"http://127.0.0.1:8010/studio/callback"}) {
		t.Errorf("studio redirects = %v, %v", redirects, err)
	}
}

func TestSetenvTurnsTheLicenceServerOffByDefault(t *testing.T) {
	t.Setenv("SAFE_LICENSE_SERVER_URL", "")
	_ = os.Unsetenv("SAFE_LICENSE_SERVER_URL")
	Setenv(testConfig(t))
	// bkn-safe keeps its built-in server unless the variable is set, even empty.
	if v, set := os.LookupEnv("SAFE_LICENSE_SERVER_URL"); !set || v != "" {
		t.Errorf("SAFE_LICENSE_SERVER_URL = %q, set %v; want set and empty", v, set)
	}
}

func TestEnsureDatabaseRefusesAnUnsafeName(t *testing.T) {
	t.Setenv("SAFE_DB_NAME", "safe`; DROP DATABASE openbkn; --")
	if err := EnsureDatabase(); err == nil || !strings.Contains(err.Error(), "SAFE_DB_NAME") {
		t.Fatalf("err = %v, want the name refused", err)
	}
}

func TestSystemSecretIsCreatedOnceAndKept(t *testing.T) {
	path := filepath.Join(t.TempDir(), "system-secret")
	first, err := systemSecret(path)
	if err != nil || len(first) < 32 {
		t.Fatalf("systemSecret() = %q, %v", first, err)
	}
	second, err := systemSecret(path)
	if err != nil || second != first {
		t.Fatalf("second read = %q, %v; want the first secret", second, err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("secret file mode = %v, %v; want 0600", info.Mode().Perm(), err)
	}
	if err := os.WriteFile(path, []byte("short\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := systemSecret(path); err == nil {
		t.Error("a damaged secret file was accepted")
	}
}

func TestHydraEnvPointsAtMiniAndPassesNothingElse(t *testing.T) {
	t.Setenv("DSN", "postgres://elsewhere")
	t.Setenv("SAFE_DB_PASSWORD", "mine")
	cfg := testConfig(t)
	env, err := hydraEnv(cfg)
	if err != nil {
		t.Fatal(err)
	}
	dir, _ := filepath.Abs(cfg.HydraDir)
	for _, want := range []string{
		"DSN=sqlite://" + filepath.ToSlash(dir) + "/hydra.sqlite?_fk=true",
		"URLS_SELF_ISSUER=http://127.0.0.1:8010",
		"URLS_LOGIN=http://127.0.0.1:8010/login",
		"URLS_CONSENT=http://127.0.0.1:8010/consent",
		"URLS_DEVICE_VERIFICATION=http://127.0.0.1:8010/device",
		"SERVE_PUBLIC_HOST=127.0.0.1", "SERVE_PUBLIC_PORT=4444",
		"SERVE_ADMIN_HOST=127.0.0.1", "SERVE_ADMIN_PORT=4445",
	} {
		if !slices.Contains(env, want) {
			t.Errorf("hydra environment lacks %s", want)
		}
	}
	for _, kv := range env {
		if strings.HasPrefix(kv, "SAFE_") || kv == "DSN=postgres://elsewhere" {
			t.Errorf("hydra environment carries %s", kv)
		}
	}
}

func TestStartHydraNamesAMissingProgram(t *testing.T) {
	cfg := testConfig(t)
	cfg.HydraBin = filepath.Join(t.TempDir(), "no-such-hydra")
	if _, err := StartHydra(cfg, nil); err == nil || !strings.Contains(err.Error(), "no-such-hydra") {
		t.Fatalf("err = %v, want the missing program named", err)
	}
}

func TestSeedClientsCreatesOnlyMissingClients(t *testing.T) {
	var created []oauthClient
	admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/admin/clients/openbkn-studio":
			w.WriteHeader(http.StatusOK) // already there: must be left alone
		case r.Method == http.MethodGet:
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodPost && r.URL.Path == "/admin/clients":
			var c oauthClient
			body, _ := io.ReadAll(r.Body)
			if err := json.Unmarshal(body, &c); err != nil {
				t.Errorf("client is not JSON: %v", err)
			}
			created = append(created, c)
			w.WriteHeader(http.StatusCreated)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer admin.Close()
	cfg := testConfig(t)
	cfg.HydraAdminAddr = strings.TrimPrefix(admin.URL, "http://")
	if err := SeedClients(cfg); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, c := range created {
		ids = append(ids, c.ClientID)
		if c.TokenEndpointAuthMethod != "none" || !slices.Equal(c.Audience, []string{"bkn-safe"}) {
			t.Errorf("%s: auth method %q, audience %v; want a public client for bkn-safe",
				c.ClientID, c.TokenEndpointAuthMethod, c.Audience)
		}
	}
	if !slices.Equal(ids, []string{"openbkn-sdk", "openbkn-cli"}) {
		t.Errorf("created %v, want openbkn-sdk and openbkn-cli", ids)
	}
}

func TestStudioClientReturnsToThePublicURL(t *testing.T) {
	for _, c := range clients("http://127.0.0.1:8010") {
		if c.ClientID != "openbkn-studio" {
			continue
		}
		if !slices.Equal(c.RedirectURIs, []string{"http://127.0.0.1:8010/studio/callback"}) ||
			!slices.Equal(c.PostLogoutRedirectURIs, []string{"http://127.0.0.1:8010/studio"}) {
			t.Errorf("redirects %v, logout %v", c.RedirectURIs, c.PostLogoutRedirectURIs)
		}
		return
	}
	t.Fatal("no openbkn-studio client")
}

func TestSeedClientsReportsHydraRefusal(t *testing.T) {
	admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		http.Error(w, "bad client", http.StatusBadRequest)
	}))
	defer admin.Close()
	cfg := testConfig(t)
	cfg.HydraAdminAddr = strings.TrimPrefix(admin.URL, "http://")
	if err := SeedClients(cfg); err == nil || !strings.Contains(err.Error(), "bad client") {
		t.Fatalf("err = %v, want Hydra's answer", err)
	}
}

func TestDatabaseFromReadsTheServiceConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	config := "server: {httpPort: 13015}\ndepServices:\n  rds: {host: 127.0.0.1, port: 3306, user: root, password: from-config, type: MariaDB}\n"
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := DatabaseFrom(path)
	want := Database{Host: "127.0.0.1", Port: "3306", User: "root", Password: "from-config"}
	if err != nil || db != want {
		t.Fatalf("DatabaseFrom() = %+v, %v; want %+v", db, err, want)
	}
	if err := os.WriteFile(path, []byte("depServices: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := DatabaseFrom(path); err == nil {
		t.Error("a config without a database server was accepted")
	}
}

// The copy of the config mini ships must keep the entry mini reads.
func TestDatabaseFromReadsMinisOwnConfig(t *testing.T) {
	db, err := DatabaseFrom("../../dev/run/config/bkn-backend-config.yaml")
	if err != nil || db.Host == "" || db.Port == "" {
		t.Fatalf("DatabaseFrom(mini's bkn-backend config) = %+v, %v", db, err)
	}
}
