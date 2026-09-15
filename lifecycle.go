// Package lifecycle contains the same lifecycle scripts shipped to operators.
// Keeping one copy makes web maintenance and terminal maintenance agree.
package lifecycle

import _ "embed"

//go:embed install.sh
var Install []byte

//go:embed uninstall.sh
var Uninstall []byte
