//go:build !windows

package installer

import "syscall"

// DiskSpace is the size and free space of the filesystem holding path.
func DiskSpace(path string) (*DiskUsage, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return nil, err
	}
	bs := uint64(st.Bsize) //nolint:unconvert // int64 on linux, uint32 on darwin
	return &DiskUsage{Path: path, Total: uint64(st.Blocks) * bs, Free: uint64(st.Bavail) * bs}, nil
}
