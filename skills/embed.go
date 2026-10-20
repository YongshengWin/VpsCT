// Package skills ships the agent integration alongside the control plane.
package skills

import _ "embed"

//go:embed vpsct/SKILL.md
var VpsCT []byte

//go:embed vpsct/scripts/vpsct.py
var Client []byte

//go:embed vpsct/references/api.md
var Reference []byte

//go:embed install_vpsct.py
var Installer []byte
