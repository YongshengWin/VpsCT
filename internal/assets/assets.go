// Package assets embeds operator-facing scripts served by ctlvpsd.
package assets

import _ "embed"

// InstallAgent is the agent bootstrap script served at /install-agent.sh.
//
//go:embed install-agent.sh
var InstallAgent []byte
