//go:build !linux

package core

import "fmt"

func proxyIdentity(string) (uint32, uint32, error) {
	return 0, 0, fmt.Errorf("proxy isolation requires Linux")
}
