//go:build !linux

package openrc

func PrepareMaintenance(string) error { return unsupported() }
func MaintenanceActive(string) bool   { return false }

func PrepareMaintenanceHost(string) error { return unsupported() }
func KillMaintenance(string) error        { return unsupported() }
func StartAgent() error                   { return unsupported() }
