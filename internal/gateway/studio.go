package gateway

import (
	"encoding/json"
	"fmt"
)

// Navigation studio leaves out in every mini: services mini does not run (BKN
// project, Trace, BKN Agent, object metrics), the graph explorer upgrade entry
// (enterprise only), and the lab's sandbox runtime entry, which repeats the one
// under the execution factory.
var hiddenNavigation = []string{
	"bkn-project", "observability", "experience-agent", "metrics", "graph_explorer",
	"execution-factory-lab-sandbox-runtime",
}

// The local user only carries the permissions of the modules mini runs, so
// studio's own permission filter hides the rest of the console.
var basePermissions = []string{
	"knowledge-network:view", "knowledge-network:create", "knowledge-network:edit",
	"knowledge-network:delete", "knowledge-network:import", "knowledge-network:export",
	"knowledge-network:preview", "knowledge-network:concept-group:view",
	"catalog:view_detail", "catalog:create", "catalog:modify", "catalog:delete",
	"catalog:task_manage", "catalog:resource_manage", "resource:view_detail",
	// Execution factory: API tools, MCP and Skill.
	"execution-factory:toolbox:view", "execution-factory:toolbox:create",
	"execution-factory:toolbox:edit", "execution-factory:toolbox:publish",
	"execution-factory:toolbox:delete", "execution-factory:toolbox:debug",
	"execution-factory:tool:view", "execution-factory:tool:create",
	"execution-factory:tool:edit", "execution-factory:tool:delete", "execution-factory:tool:debug",
	"execution-factory:mcp:view", "execution-factory:mcp:create", "execution-factory:mcp:edit",
	"execution-factory:mcp:publish", "execution-factory:mcp:delete", "execution-factory:mcp:debug",
	"execution-factory:skill:view", "execution-factory:skill:create", "execution-factory:skill:edit",
	"execution-factory:skill:publish", "execution-factory:skill:delete",
	"execution-factory:impex:export", "execution-factory:impex:import",
}

// sandboxPermissions come with a function executor: Function tools and the
// sandbox runtime page.
var sandboxPermissions = []string{
	"execution-factory:function:view", "execution-factory:function:create",
	"execution-factory:function:edit", "execution-factory:function:delete",
	"execution-factory:function:publish", "execution-factory:function:debug",
	"execution-factory-lab:sandbox-runtime:view",
}

// There is no login in mini: leave the licence, account and sign-out entries
// (and the divider above sign-out) out of the user menu.
const userMenuScript = `(function () {
  var menu = ".ant-dropdown-menu:has(.console-user-menu-version) > ";
  var hidden = ["subscription", "account", "logout"].map(function (key) {
    return menu + 'li[data-menu-id$="-' + key + '"]';
  });
  hidden.push(menu + 'li.ant-dropdown-menu-item-divider:has(+ li[data-menu-id$="-logout"])');
  var style = document.createElement("style");
  style.textContent = hidden.join(",\n") + " { display: none !important; }";
  document.head.appendChild(style);
})();
`

type studioUser struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	IsAdmin      bool     `json:"isAdmin"`
	IsSuperAdmin bool     `json:"isSuperAdmin"`
	Roles        []string `json:"roles"`
	Permissions  []string `json:"permissions"`
}

type studioRuntime struct {
	// Hosted mode turns off studio's OAuth gate, for mini without a login.
	// Left out, studio signs the user in and reads their permissions itself.
	Mode             string         `json:"mode,omitempty"`
	HiddenNavigation []string       `json:"hiddenNavigation"`
	Features         map[string]any `json:"features,omitempty"`
	CurrentUser      *studioUser    `json:"currentUser,omitempty"`
}

// safeHiddenNavigation are entries that a signed-in administrator's
// permissions would show, for services mini does not run: models are set in
// models.json, not in the platform's model management.
var safeHiddenNavigation = []string{"model-resource-management", "model-statistics"}

// studioConfigJS is studio's config.js: its runtime settings and the user
// menu style.
func studioConfigJS(opts Options) string {
	rt := studioRuntime{HiddenNavigation: append([]string{}, hiddenNavigation...)}
	if opts.Safe {
		rt.HiddenNavigation = append(rt.HiddenNavigation, safeHiddenNavigation...)
	} else {
		rt.Mode = "hosted"
		rt.CurrentUser = &studioUser{
			ID:          "266c6a42-6131-4d62-8f39-853e7093701c",
			Name:        "admin",
			Roles:       []string{},
			Permissions: append([]string{}, basePermissions...),
		}
	}
	if opts.Sandbox {
		if rt.CurrentUser != nil {
			rt.CurrentUser.Permissions = append(rt.CurrentUser.Permissions, sandboxPermissions...)
		}
	} else {
		rt.HiddenNavigation = append(rt.HiddenNavigation, "functions")
		rt.Features = map[string]any{"executionFactoryLab": map[string]any{"function": false}}
	}
	data, err := json.MarshalIndent(rt, "", "  ")
	if err != nil {
		panic(err) // plain data: cannot fail
	}
	js := fmt.Sprintf("window.__BKN_STUDIO_RUNTIME__ = %s;\n", data)
	if opts.Safe {
		return js // the account and sign-out entries are real
	}
	return js + userMenuScript
}
