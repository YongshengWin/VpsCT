//go:build !linux

package openrc

import (
	"context"
	"errors"
)

func unsupported() error                     { return errors.New("OpenRC services require Linux") }
func trustedDirectory(string, bool) error    { return unsupported() }
func CheckEnvironment(context.Context) error { return unsupported() }
func budget(string) (int, error)             { return 0, unsupported() }
func ensureSlice(string) error               { return unsupported() }
func Stop(string) error                      { return unsupported() }
func MainPID(string) (int, error)            { return 0, unsupported() }
func populated(string) bool                  { return false }
func helperCurrent(string) (bool, error)     { return false, unsupported() }
func processStart(int) (string, error)       { return "", unsupported() }
func VerifyProxy(string, string) error       { return unsupported() }
func Exec(string) error                      { return unsupported() }
