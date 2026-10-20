//go:build !linux

package openrc

func EnsureMeter(string) error                       { return unsupported() }
func ReadMeter(string) (int64, int64, string, error) { return 0, 0, "", unsupported() }
func RemoveMeter(string) error                       { return unsupported() }

func RemoveAllMeters() error { return unsupported() }
