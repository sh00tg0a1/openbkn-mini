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
	vegaapp "github.com/openbkn-ai/bkn-foundry/vega/vega-backend/server/app"

	"github.com/sh00tg0a1/openbkn-mini/internal/fakes"
	"github.com/sh00tg0a1/openbkn-mini/internal/gateway"
	"github.com/sh00tg0a1/openbkn-mini/internal/unfreeze"
)

func main() {
	// Process-wide settings the platform services read from the environment.
	defaults := map[string]string{
		"BKN_SAFE_URL":                         "http://127.0.0.1:3000",
		"BKN_TRACE_CORE_URL":                   "http://127.0.0.1:8081",
		"BKN_TRACE_ARTIFACT_ENDPOINT":          "http://127.0.0.1:8081/artifacts",
		"BKN_TRACE_EVIDENCE_PUBLISHER_ENABLED": "false",
		"CONFIG_PROFILE":                       "./cl",
		"TZ":                                   "Asia/Shanghai",
	}
	for k, v := range defaults {
		if os.Getenv(k) == "" {
			_ = os.Setenv(k, v)
		}
	}

	must(fakes.Start(fakes.Config{
		SafeAddr:     "127.0.0.1:3000",
		HydraAddr:    "127.0.0.1:4445",
		ModelAddr:    "127.0.0.1:9898",
		NSQAddr:      "127.0.0.1:4151",
		TraceAddr:    "127.0.0.1:8081",
		RegistryFile: env("MINI_AUTHZ_REGISTRY", "./authorization-registry.json"),
	}), "start fakes")
	for _, addr := range []string{"127.0.0.1:3000", "127.0.0.1:4445", "127.0.0.1:9898", "127.0.0.1:4151", "127.0.0.1:8081"} {
		waitListening("fake", addr, 10*time.Second)
	}

	logf("booting services")
	vega, err := vegaapp.Boot(vegaapp.Options{})
	must(err, "vega boot")
	bkn := bknapp.Boot(bknapp.Options{})
	oq := oqapp.Boot(oqapp.Options{})
	cl, err := clapp.Boot(clapp.Options{})
	must(err, "context-loader boot")

	// Vega first: bkn-backend calls it during Run and panics if it is down.
	start("vega", func() { must(vega.Run(), "vega run") }, env("MINI_VEGA_ADDR", "127.0.0.1:13014"))
	start("ontology-query", oq.Run, env("MINI_OQ_ADDR", "127.0.0.1:13018"))
	start("context-loader", func() { must(cl.Run(), "context-loader run") }, env("MINI_CL_ADDR", "127.0.0.1:30779"))
	start("bkn-backend", bkn.Run, env("MINI_BKN_ADDR", "127.0.0.1:13015"))

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

// start runs one service in a goroutine and waits until it has frozen the
// entitlement registry and is listening, then clears the flag for the next one.
func start(name string, run func(), addr string) {
	unfreeze.Clear()
	go run()
	deadline := time.Now().Add(2 * time.Minute)
	for !unfreeze.Frozen() {
		if time.Now().After(deadline) {
			fail(name + ": did not freeze in time")
		}
		time.Sleep(20 * time.Millisecond)
	}
	waitListening(name, addr, time.Until(deadline))
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
