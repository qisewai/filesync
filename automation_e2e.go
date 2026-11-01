//go:build e2e

package main

// Opt-in diagnostics only; production builds do not expose automation.
import _ "github.com/egoist/mygo/plugins/automation"
