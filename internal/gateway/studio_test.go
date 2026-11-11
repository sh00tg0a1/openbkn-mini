package gateway

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func parseStudioConfig(t *testing.T, js string) studioRuntime {
	t.Helper()
	const prefix = "window.__BKN_STUDIO_RUNTIME__ = "
	if !strings.HasPrefix(js, prefix) {
		t.Fatalf("config.js does not start with %q", prefix)
	}
	body := strings.TrimPrefix(js, prefix)
	end := strings.Index(body, "};\n")
	if end < 0 {
		t.Fatal("config.js has no end to the runtime object")
	}
	var rt studioRuntime
	if err := json.Unmarshal([]byte(body[:end+1]), &rt); err != nil {
		t.Fatalf("runtime object is not JSON: %v", err)
	}
	return rt
}

func TestStudioConfigWithSandboxShowsFunctions(t *testing.T) {
	rt := parseStudioConfig(t, studioConfigJS(Options{Sandbox: true}))
	if slices.Contains(rt.HiddenNavigation, "functions") {
		t.Error("functions hidden although an executor runs them")
	}
	for _, p := range []string{"execution-factory:function:create", "execution-factory-lab:sandbox-runtime:view"} {
		if !slices.Contains(rt.CurrentUser.Permissions, p) {
			t.Errorf("missing permission %s", p)
		}
	}
	if rt.Features != nil {
		t.Errorf("features = %v, want none (studio reads the lab flags from the backend)", rt.Features)
	}
}

func TestStudioConfigWithoutSandboxHidesFunctions(t *testing.T) {
	js := studioConfigJS(Options{})
	rt := parseStudioConfig(t, js)
	if !slices.Contains(rt.HiddenNavigation, "functions") {
		t.Error("functions shown without an executor")
	}
	for _, p := range rt.CurrentUser.Permissions {
		if strings.Contains(p, ":function:") || strings.Contains(p, "sandbox-runtime") {
			t.Errorf("permission %s granted without an executor", p)
		}
	}
	if !strings.Contains(js, `"function": false`) {
		t.Error("lab function capability not turned off")
	}
	if !strings.Contains(js, "console-user-menu-version") {
		t.Error("user menu style missing")
	}
}

func TestStudioConfigWithSafeLeavesSignInToStudio(t *testing.T) {
	js := studioConfigJS(Options{Sandbox: true, Safe: true})
	rt := parseStudioConfig(t, js)
	if rt.Mode != "" || rt.CurrentUser != nil {
		t.Errorf("mode %q, user %v: studio must sign the user in itself", rt.Mode, rt.CurrentUser)
	}
	if strings.Contains(js, `"mode"`) || strings.Contains(js, `"currentUser"`) {
		t.Error("config.js sets mode or currentUser")
	}
	if strings.Contains(js, "console-user-menu-version") {
		t.Error("account and sign-out are hidden although they work")
	}
	for _, key := range []string{"bkn-project", "model-resource-management"} {
		if !slices.Contains(rt.HiddenNavigation, key) {
			t.Errorf("%s shown although mini does not run it", key)
		}
	}
	if slices.Contains(rt.HiddenNavigation, "functions") {
		t.Error("functions hidden although an executor runs them")
	}
}

func TestStudioConfigWithSafeAndNoSandboxHidesFunctions(t *testing.T) {
	js := studioConfigJS(Options{Safe: true})
	if rt := parseStudioConfig(t, js); !slices.Contains(rt.HiddenNavigation, "functions") {
		t.Error("functions shown without an executor")
	}
	if !strings.Contains(js, `"function": false`) {
		t.Error("lab function capability not turned off")
	}
}
