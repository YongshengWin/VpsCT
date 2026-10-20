//go:build !linux

package udpsession

import (
	"context"
	"errors"
)

func Live(context.Context) (map[Flow]bool, error) {
	return nil, errors.New("UDP session management requires Linux")
}
func LiveFor(ctx context.Context, _ map[Flow]bool) (map[Flow]bool, error) { return Live(ctx) }

func Discover(int, map[int]bool) (string, error) {
	return "", errors.New("UDP session management requires Linux")
}
