// Package adapter lists the coding agents that OpenPlexer connects to. Each
// subpackage holds one agent, and package hook holds what they share.
package adapter

import (
	"github.com/multiplexer-si/openplexer/internal/adapter/claudecode"
	"github.com/multiplexer-si/openplexer/internal/adapter/codex"
	"github.com/multiplexer-si/openplexer/internal/adapter/hook"
)

// Agents are the coding agents that OpenPlexer connects to.
var Agents = []hook.Agent{claudecode.Agent, codex.Agent}

// Lookup returns the agent with the name.
func Lookup(name string) (hook.Agent, bool) {
	for _, agent := range Agents {
		if agent.Name == name {
			return agent, true
		}
	}
	return hook.Agent{}, false
}
