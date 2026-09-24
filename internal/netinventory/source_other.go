//go:build !linux

package netinventory

func newSource() (func() (reading, error), func() error) {
	return func() (reading, error) { return reading{}, errUnsupported }, func() error { return nil }
}
