//go:build !linux

package openrc

import "context"

const managerSocket = "/run/ctlvps-openrc/manage.sock"

func managedControl(context.Context, []string) (string, error) { return "", unsupported() }
func ensureManager(context.Context) error                      { return unsupported() }
func managerLoop() error                                       { return unsupported() }
