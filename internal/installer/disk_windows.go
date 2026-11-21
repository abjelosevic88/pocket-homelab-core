package installer

import "errors"

// DiskSpace is not read on Windows.
func DiskSpace(path string) (*DiskUsage, error) {
	return nil, errors.New("not on windows")
}
