//go:build !unix

package capabilities

import "errors"

// DiskFree is not supported on this platform.
func DiskFree(string) (uint64, error) { return 0, errors.New("disk free: unsupported platform") }
