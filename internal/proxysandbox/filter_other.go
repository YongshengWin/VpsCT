//go:build !linux

package proxysandbox

import "fmt"

func launch(string, []string) error { return fmt.Errorf("proxy sandbox requires Linux") }
func Install() error                { return fmt.Errorf("proxy sandbox requires Linux") }
func InstallGuard() error           { return fmt.Errorf("proxy sandbox requires Linux") }
