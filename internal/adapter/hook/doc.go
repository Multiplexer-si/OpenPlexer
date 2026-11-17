// Package hook holds what every coding agent adapter shares: the shape of a
// hook call, the reading of a shell command and of a path, the translation of
// a tool call into policy intents, and the decision that folds the policy
// results of one tool call into one verdict. It knows no agent by name. It
// imports internal/policy and nothing from the daemon.
package hook
