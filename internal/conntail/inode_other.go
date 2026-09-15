//go:build !unix

package conntail

import "os"

func inodeOf(_ os.FileInfo) uint64 { return 0 }
