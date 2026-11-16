//go:build unix

package capabilities

import "golang.org/x/sys/unix"

// DiskFree returns bytes available to unprivileged users on the filesystem
// containing path.
func DiskFree(path string) (uint64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, err
	}
	return uint64(st.Bavail) * uint64(st.Bsize), nil //nolint:unconvert // field types differ per OS
}
