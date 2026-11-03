// Command mini runs the OpenBKN core Go services in one process.
package main

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
	"time"

	bknapp "github.com/openbkn-ai/bkn-foundry/adp/bkn/bkn-backend/server/app"
	oqapp "github.com/openbkn-ai/bkn-foundry/adp/bkn/ontology-query/server/app"
	clapp "github.com/openbkn-ai/bkn-foundry/adp/context-loader/agent-retrieval/server/app"
	efapp "github.com/openbkn-ai/bkn-foundry/adp/execution-factory/operator-integration/server/app"
	vegaapp "github.com/openbkn-ai/bkn-foundry/vega/vega-backend/server/app"

	"github.com/sh00tg0a1/openbkn-mini/internal/fakes"
	"github.com/sh00tg0a1/openbkn-mini/internal/forward"
	"github.com/sh00tg0a1/openbkn-mini/internal/gateway"
)

// Where mini's stand-ins listen: the platform configs under dev/run point here.
const (
	safeAddr    = "127.0.0.1:3000"
	hydraAddr   = "127.0.0.1:4445"
	modelAddr   = "127.0.0.1:9898"
	nsqAddr     = "127.0.0.1:4151"
	traceAddr   = "127.0.0.1:8081"
	sandboxAddr = "127.0.0.1:8090"
	ossAddr     = "127.0.0.1:8095"
)

// Where the platform services listen, as their configs say.
var (
	vegaAddr = env("MINI_VEGA_ADDR", "127.0.0.1:13014")
	oqAddr   = env("MINI_OQ_ADDR", "127.0.0.1:13018")
	clAddr   = env("MINI_CL_ADDR", "127.0.0.1:30779")
	bknAddr  = env("MINI_BKN_ADDR", "127.0.0.1:13015")
	efAddr   = env("MINI_EF_ADDR", "127.0.0.1:9000")
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(healthcheck())
	}

	gwAddr := env("MINI_GATEWAY_ADDR", "127.0.0.1:8000")
	must(checkGatewayPort(gwAddr, insideListeners()), "gateway")

	// Process-wide settings the platform services read from the environment.
	defaults := map[string]string{
		"BKN_SAFE_URL":                         "http://127.0.0.1:3000",
		"BKN_TRACE_CORE_URL":                   "http://127.0.0.1:8081",
		"BKN_TRACE_ARTIFACT_ENDPOINT":          "http://127.0.0.1:8081/artifacts",
		"BKN_TRACE_EVIDENCE_PUBLISHER_ENABLED": "false",
		"CONFIG_PROFILE":                       "./profile",
		"TZ":                                   "Asia/Shanghai",
	}
	for k, v := range defaults {
		if os.Getenv(k) == "" {
			_ = os.Setenv(k, v)
		}
	}

	// In the container, MariaDB and OpenSearch run as other compose services.
	if spec := os.Getenv("MINI_FORWARD"); spec != "" {
		must(forward.Start(spec), "forward")
		logf("forwarding %s", spec)
	}

	directModels := loadModels(env("MINI_MODELS_FILE", "./models.json"))
	// bkn-backend embeds concepts with the default embedding model only when
	// told to, and fails writes when told to without one.
	must(setYAMLBool("./config/bkn-backend-config.yaml", "defaultSmallModelEnabled", hasEmbedding(directModels)),
		"bkn-backend model flag")

	must(fakes.Start(fakes.Config{
		SafeAddr:    safeAddr,
		HydraAddr:   hydraAddr,
		ModelAddr:   modelAddr,
		NSQAddr:     nsqAddr,
		TraceAddr:   traceAddr,
		SandboxAddr: sandboxAddr,
		OSSAddr:     ossAddr,
		OSSDir:      env("MINI_OSS_DIR", "./oss-data"),
		PublicURL:   env("MINI_PUBLIC_URL", "http://127.0.0.1:8000"),
		Models: fakes.ModelUpstream{
			BaseURL:  os.Getenv("MINI_MODEL_UPSTREAM"),
			AppKey:   os.Getenv("MINI_MODEL_UPSTREAM_APPKEY"),
			Insecure: os.Getenv("MINI_MODEL_UPSTREAM_INSECURE") == "1",
		},
		DirectModels: directModels,
		Sandbox: fakes.SandboxConfig{
			ExecutorURL: os.Getenv("MINI_EXECUTOR_URL"),
			Workspace:   env("WORKSPACE_PATH", "./workspace"),
		},
		RegistryFile: env("MINI_AUTHZ_REGISTRY", "./authorization-registry.json"),
	}), "start fakes")
	for _, addr := range []string{safeAddr, hydraAddr, modelAddr, nsqAddr, traceAddr, sandboxAddr, ossAddr} {
		waitListening("fake", addr, 10*time.Second)
	}

	logf("booting services")
	vega, err := vegaapp.Boot(vegaapp.Options{})
	must(err, "vega boot")
	bkn := bknapp.Boot(bknapp.Options{})
	oq := oqapp.Boot(oqapp.Options{})
	cl, err := clapp.Boot(clapp.Options{})
	must(err, "context-loader boot")
	ef, err := efapp.Boot(efapp.Options{})
	must(err, "execution-factory boot")

	// Vega first: bkn-backend calls it during Run and panics if it is down.
	start("vega", func() { must(vega.Run(), "vega run") }, vegaAddr)
	start("ontology-query", oq.Run, oqAddr)
	start("context-loader", func() { must(cl.Run(), "context-loader run") }, clAddr)
	start("bkn-backend", bkn.Run, bknAddr)
	start("execution-factory", func() { must(ef.Run(), "execution-factory run") }, efAddr)

	sandbox := os.Getenv("MINI_EXECUTOR_URL") != ""
	logf("function executor: %v", sandbox)
	gateway.Start(gwAddr, gateway.DefaultRoutes(), gateway.Options{Sandbox: sandbox})
	waitListening("gateway", gwAddr, 10*time.Second)
	logf("all services started; open %s", env("MINI_PUBLIC_URL", "http://127.0.0.1:8000"))

	// Each service also listens for these signals and shuts its own server
	// down; give them a moment, then end the process.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	logf("shutting down")
	time.Sleep(3 * time.Second)
	os.Exit(0)
}

// healthcheck is the container's health probe: the gateway serves studio.
func healthcheck() int {
	_, port, err := net.SplitHostPort(env("MINI_GATEWAY_ADDR", "127.0.0.1:8000"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	client := http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://127.0.0.1:" + port + "/studio/")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintln(os.Stderr, resp.Status)
		return 1
	}
	return 0
}

// insideListeners names what else listens inside mini, by address.
func insideListeners() map[string]string {
	inside := map[string]string{
		safeAddr:         "the bkn-safe stand-in",
		hydraAddr:        "the Hydra stand-in",
		modelAddr:        "the model stand-in",
		nsqAddr:          "the NSQ stand-in",
		traceAddr:        "the trace stand-in",
		sandboxAddr:      "the sandbox stand-in",
		ossAddr:          "the OSS stand-in",
		vegaAddr:         "vega",
		oqAddr:           "ontology-query",
		clAddr:           "context-loader",
		bknAddr:          "bkn-backend",
		efAddr:           "execution-factory",
		"127.0.0.1:6379": "Redis", // started by docker/entrypoint.sh
	}
	if u, err := url.Parse(os.Getenv("MINI_EXECUTOR_URL")); err == nil && u.Host != "" {
		inside[u.Host] = "the function executor"
	}
	for _, pair := range strings.Split(os.Getenv("MINI_FORWARD"), ",") {
		if listen, target, ok := strings.Cut(pair, "="); ok {
			inside[strings.TrimSpace(listen)] = "the forward to " + strings.TrimSpace(target)
		}
	}
	return inside
}

// checkGatewayPort refuses a gateway port another part of mini listens on.
// With docker compose the gateway listens on MINI_PORT inside the container as
// well as outside it, so the links mini hands out work in both places.
func checkGatewayPort(gateway string, inside map[string]string) error {
	_, port, err := net.SplitHostPort(gateway)
	if err != nil {
		return err
	}
	for addr, name := range inside {
		if _, p, err := net.SplitHostPort(addr); err == nil && p == port {
			return fmt.Errorf("port %s is taken inside mini by %s; set MINI_PORT to another port", port, name)
		}
	}
	return nil
}

// loadModels reads the models file if there is one. Without it mini runs
// with no models (MINI_MODEL_UPSTREAM can still forward to a platform).
func loadModels(path string) []fakes.DirectModel {
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		logf("models: none (no %s)", path)
		return nil
	}
	models, err := fakes.LoadDirectModels(path)
	must(err, "load models")
	logf("models: %d from %s", len(models), path)
	return models
}

func hasEmbedding(models []fakes.DirectModel) bool {
	for _, m := range models {
		if m.Type == "embedding" {
			return true
		}
	}
	return false
}

// setYAMLBool rewrites "key: true|false" in a service config file when it
// differs, since the services read these flags only from their files.
func setYAMLBool(path, key string, value bool) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	re := regexp.MustCompile(`(?m)^(\s*` + regexp.QuoteMeta(key) + `:\s*)(true|false)\s*$`)
	if !re.Match(data) {
		return fmt.Errorf("%s: no %q flag", path, key)
	}
	updated := re.ReplaceAll(data, []byte(fmt.Sprintf("${1}%t", value)))
	if string(updated) == string(data) {
		return nil
	}
	logf("%s: %s = %t", path, key, value)
	return os.WriteFile(path, updated, 0o644)
}

// start runs one service in a goroutine and waits until it is listening.
func start(name string, run func(), addr string) {
	go run()
	waitListening(name, addr, 2*time.Minute)
	logf("%s listening on %s", name, addr)
}

func waitListening(name, addr string, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for {
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return
		}
		if time.Now().After(deadline) {
			fail(name + ": not listening on " + addr)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func must(err error, what string) {
	if err != nil {
		fail(fmt.Sprintf("%s: %v", what, err))
	}
}

func logf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "[mini] "+format+"\n", args...)
}

func fail(msg string) {
	logf("FATAL %s", msg)
	os.Exit(1)
}
