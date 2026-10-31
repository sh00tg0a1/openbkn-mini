// Package unfreeze is a spike-only workaround: every platform service calls
// entitlement.Freeze() when it starts serving, and a second call panics. In one
// process we clear the flag before starting the next service. To be replaced
// by an idempotent Freeze upstream.
package unfreeze

import (
	_ "unsafe" // for go:linkname

	"github.com/openbkn-ai/bkn-foundry/comm-go/entitlement"
)

//go:linkname frozen github.com/openbkn-ai/bkn-foundry/comm-go/entitlement.frozen
var frozen bool

// Frozen reports whether the registry is currently frozen.
func Frozen() bool { return entitlement.Frozen() }

// Clear resets the frozen flag so the next service's Run can freeze again.
func Clear() { frozen = false }
