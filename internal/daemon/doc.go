// Package daemon owns the local runtime on a developer's machine: the gate that asks
// policy whether an action is allowed, the policy source, the store of the last
// verified bundle, stale and offline behavior, local identity, and the audit log of
// each decision.
package daemon
