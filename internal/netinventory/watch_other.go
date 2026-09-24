//go:build !linux

package netinventory

import "context"

func Watch(context.Context) (<-chan Change, error) { return nil, errUnsupported }
