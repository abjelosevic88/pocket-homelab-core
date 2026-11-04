package installer

import (
	"errors"
	"io/fs"
)

// The installer agent runs on Linux; these only let the binary build.

func freeBytes(string) (int64, error) { return 0, errors.ErrUnsupported }

func fileOwner(fs.FileInfo) (int, int, bool) { return 0, 0, false }
