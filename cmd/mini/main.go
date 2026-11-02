// Command mini runs the OpenBKN core Go services in one process (spike).
package main

import (
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	bknapp "github.com/openbkn-ai/bkn-foundry/adp/bkn/bkn-backend/server/app"
	oqapp "github.com/openbkn-ai/bkn-foundry/adp/bkn/ontology-query/server/app"
	clapp "github.com/openbkn-ai/bkn-foundry/adp/context-loader/agent-retrieval/server/app"
	efapp "github.com/openbkn-ai/bkn-foundry/adp/execution-factory/operator-integration/server/app"
	vegaapp "github.com/openbkn-ai/bkn-foundry/vega/vega-backend/server/app"

	"github.com/sh00tg0a1/openbkn-mini/internal/fakes"
	"github.com/sh00tg0a1/openbkn-mini/internal/gateway"
)

func main() {
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

	var directModels []fakes.DirectModel
	if path := os.Getenv("MINI_MODELS_FILE"); path != "" {
		models, err := fakes.LoadDirectModels(path)
		must(err, "load models")
		directModels = models
		logf("models: %d from %s", len(models), path)
	}

	must(fakes.Start(fakes.Config{
		SafeAddr:    "127.0.0.1:3000",
		HydraAddr:   "127.0.0.1:4445",
		ModelAddr:   "127.0.0.1:9898",
		NSQAddr:     "127.0.0.1:4151",
		TraceAddr:   "127.0.0.1:8081",
		SandboxAddr: "127.0.0.1:8090",
		OSSAddr:     "127.0.0.1:8095",
		OSSDir:      env("MINI_OSS_DIR", "./oss-data"),
		PublicURL:   "http://" + env("MINI_GATEWAY_ADDR", "127.0.0.1:8000"),
		Models: fakes.ModelUpstream{
			BaseURL:  os.Getenv("MINI_MODEL_UPSTREAM"),
			AppKey:   os.Getenv("MINI_MODEL_UPSTREAM_APPKEY"),
			Insecure: os.Getenv("MINI_MODEL_UPSTREAM_INSECURE") == "1",
		},
		DirectModels: directModels,
		RegistryFile: env("MINI_AUTHZ_REGISTRY", "./authorization-registry.json"),
	}), "start fakes")
	for _, addr := range []string{"127.0.0.1:3000", "127.0.0.1:4445", "127.0.0.1:9898", "127.0.0.1:4151", "127.0.0.1:8081", "127.0.0.1:8090", "127.0.0.1:8095"} {
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
	start("vega", func() { must(vega.Run(), "vega run") }, env("MINI_VEGA_ADDR", "127.0.0.1:13014"))
	start("ontology-query", oq.Run, env("MINI_OQ_ADDR", "127.0.0.1:13018"))
	start("context-loader", func() { must(cl.Run(), "context-loader run") }, env("MINI_CL_ADDR", "127.0.0.1:30779"))
	start("bkn-backend", bkn.Run, env("MINI_BKN_ADDR", "127.0.0.1:13015"))
	start("execution-factory", func() { must(ef.Run(), "execution-factory run") }, env("MINI_EF_ADDR", "127.0.0.1:9000"))

	gwAddr := env("MINI_GATEWAY_ADDR", "127.0.0.1:8000")
	gateway.Start(gwAddr, gateway.DefaultRoutes())
	waitListening("gateway", gwAddr, 10*time.Second)
	logf("all services started; gateway on http://%s", gwAddr)

	// Each service also listens for these signals and shuts its own server
	// down; give them a moment, then end the process.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	logf("shutting down")
	time.Sleep(3 * time.Second)
	os.Exit(0)
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
