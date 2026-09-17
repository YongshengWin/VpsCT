package core

import (
	"os"
	"path/filepath"
)

// The durable marker bridges worker installation and coordinator activation.
// It is only removed once every affected proxy instance has converged.
func markActivation(binary string) error {
	if _, err := WriteIfChanged(binary+".activate", []byte("pending\n"), 0600); err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(binary))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
func activationPending(binary string) bool {
	_, err := os.Stat(binary + ".activate")
	return !os.IsNotExist(err)
}
func completeActivation(binary string) error {
	err := os.Remove(binary + ".activate")
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
