package main

import (
	"strings"
	"testing"
)

func TestCheckGatewayPortRefusesAPortTakenInside(t *testing.T) {
	inside := map[string]string{"127.0.0.1:8080": "the function executor", "127.0.0.1:9000": "execution-factory"}
	err := checkGatewayPort("0.0.0.0:8080", inside)
	if err == nil || !strings.Contains(err.Error(), "the function executor") {
		t.Fatalf("err = %v, want the executor named", err)
	}
	if err := checkGatewayPort("0.0.0.0:8010", inside); err != nil {
		t.Fatalf("free port refused: %v", err)
	}
}

func TestInsideListenersIncludeExecutorAndForwards(t *testing.T) {
	t.Setenv("MINI_EXECUTOR_URL", "http://127.0.0.1:8080")
	t.Setenv("MINI_FORWARD", "127.0.0.1:3306=mariadb:3306, 127.0.0.1:9200=opensearch:9200")
	inside := insideListeners()
	for addr, want := range map[string]string{
		"127.0.0.1:8080": "the function executor",
		"127.0.0.1:3306": "the forward to mariadb:3306",
		"127.0.0.1:9200": "the forward to opensearch:9200",
		"127.0.0.1:9000": "execution-factory",
	} {
		if inside[addr] != want {
			t.Errorf("%s = %q, want %q", addr, inside[addr], want)
		}
	}
	// The default gateway port is free.
	if err := checkGatewayPort("0.0.0.0:8000", inside); err != nil {
		t.Errorf("default port refused: %v", err)
	}
}

func TestSafeOnReadsTheSwitch(t *testing.T) {
	for value, want := range map[string]bool{"": false, "off": false, "no": false, "on": true, "ON": true, " true ": true, "1": true} {
		t.Setenv("MINI_SAFE", value)
		if got := safeOn(); got != want {
			t.Errorf("MINI_SAFE=%q: safeOn() = %v, want %v", value, got, want)
		}
	}
}

func TestInsideListenersWithSafeNameTheRealServices(t *testing.T) {
	t.Setenv("MINI_SAFE", "on")
	inside := insideListeners()
	for addr, want := range map[string]string{
		"127.0.0.1:3000": "bkn-safe",
		"127.0.0.1:4445": "Hydra (admin)",
		"127.0.0.1:4444": "Hydra (public)",
	} {
		if inside[addr] != want {
			t.Errorf("%s = %q, want %q", addr, inside[addr], want)
		}
	}
	if err := checkGatewayPort("0.0.0.0:4444", inside); err == nil {
		t.Error("gateway allowed on Hydra's public port")
	}
	t.Setenv("MINI_SAFE", "off")
	if _, taken := insideListeners()["127.0.0.1:4444"]; taken {
		t.Error("Hydra's public port reserved although Hydra does not run")
	}
}
