package safe

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

// Hydra is the running ORY Hydra process. bkn-safe is its login and consent
// provider; the services check tokens against its admin port.
type Hydra struct {
	cmd      *exec.Cmd
	started  atomic.Bool
	stopping atomic.Bool
}

// StartHydra creates or upgrades Hydra's database, starts Hydra and waits until
// it answers. Hydra keeps its data in SQLite under cfg.HydraDir. onExit is
// called if Hydra ends while mini still runs.
func StartHydra(cfg Config, onExit func(error)) (*Hydra, error) {
	bin, err := exec.LookPath(cfg.HydraBin)
	if err != nil {
		return nil, fmt.Errorf("hydra program %q not found: %w", cfg.HydraBin, err)
	}
	env, err := hydraEnv(cfg)
	if err != nil {
		return nil, err
	}

	migrate := exec.Command(bin, "migrate", "sql", "-e", "--yes")
	migrate.Env = env
	if out, err := migrate.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("hydra migrate: %w\n%s", err, tail(string(out), 2000))
	}

	// --dev lets Hydra serve an http issuer: mini is reached on this machine.
	cmd := exec.Command(bin, "serve", "all", "--dev")
	cmd.Env = env
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start hydra: %w", err)
	}
	h := &Hydra{cmd: cmd}
	go prefixLines(out, "[hydra] ")
	exited := make(chan error, 1)
	go func() {
		err := cmd.Wait()
		exited <- err
		if h.started.Load() && !h.stopping.Load() && onExit != nil {
			onExit(err)
		}
	}()

	ready := "http://" + cfg.HydraAdminAddr + "/health/ready"
	client := http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(60 * time.Second)
	for {
		select {
		case err := <-exited:
			return nil, fmt.Errorf("hydra ended while starting: %v", err)
		default:
		}
		if resp, err := client.Get(ready); err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				h.started.Store(true)
				return h, nil
			}
		}
		if time.Now().After(deadline) {
			h.Stop()
			return nil, fmt.Errorf("hydra did not answer at %s", ready)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// Stop ends Hydra.
func (h *Hydra) Stop() {
	if h == nil || h.stopping.Swap(true) {
		return
	}
	_ = h.cmd.Process.Signal(syscall.SIGTERM)
}

// hydraEnv is Hydra's whole environment: it reads its settings from there, so
// nothing else of mini's environment is passed on.
func hydraEnv(cfg Config) ([]string, error) {
	dir, err := filepath.Abs(cfg.HydraDir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	secret, err := systemSecret(filepath.Join(dir, "system-secret"))
	if err != nil {
		return nil, err
	}
	publicHost, publicPort, err := net.SplitHostPort(cfg.HydraPublicAddr)
	if err != nil {
		return nil, fmt.Errorf("hydra public address: %w", err)
	}
	adminHost, adminPort, err := net.SplitHostPort(cfg.HydraAdminAddr)
	if err != nil {
		return nil, fmt.Errorf("hydra admin address: %w", err)
	}
	return []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + dir,
		"TZ=" + os.Getenv("TZ"),
		"DSN=sqlite://" + filepath.ToSlash(filepath.Join(dir, "hydra.sqlite")) + "?_fk=true",
		"SECRETS_SYSTEM=" + secret,
		"URLS_SELF_ISSUER=" + cfg.PublicURL,
		"URLS_LOGIN=" + cfg.PublicURL + "/login",
		"URLS_CONSENT=" + cfg.PublicURL + "/consent",
		"URLS_DEVICE_VERIFICATION=" + cfg.PublicURL + "/device",
		"URLS_DEVICE_SUCCESS=" + cfg.PublicURL + "/device/success",
		"SERVE_PUBLIC_HOST=" + publicHost,
		"SERVE_PUBLIC_PORT=" + publicPort,
		"SERVE_ADMIN_HOST=" + adminHost,
		"SERVE_ADMIN_PORT=" + adminPort,
		"LOG_LEVEL=" + envOr("MINI_HYDRA_LOG_LEVEL", "warn"),
		"SQA_OPT_OUT=true",
	}, nil
}

// systemSecret returns the secret Hydra signs and encrypts with, creating it
// on first use. It must stay the same for as long as Hydra's database lives.
func systemSecret(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		if s := strings.TrimSpace(string(data)); len(s) >= 32 {
			return s, nil
		}
		return "", fmt.Errorf("%s: shorter than 32 characters", path)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	secret := hex.EncodeToString(raw)
	if err := os.WriteFile(path, []byte(secret+"\n"), 0o600); err != nil {
		return "", err
	}
	return secret, nil
}

func prefixLines(r io.Reader, prefix string) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		fmt.Fprintln(os.Stderr, prefix+scanner.Text())
	}
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
