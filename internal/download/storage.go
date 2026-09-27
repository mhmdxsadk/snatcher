package download

import (
	"errors"
	"io/fs"
	"path/filepath"
	"syscall"
)

// Leave headroom within the default 2 GiB tmpfs for runtime files and writes
// between size checks. One worker reserves maxJobBytes before starting a job.
const DefaultStorageLimit int64 = 1792 << 20

var ErrStorage = errors.New("download storage is full; retry after completed jobs expire")

func directorySize(dir string) (int64, error) {
	var size int64
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		size += info.Size()
		return nil
	})
	return size, err
}

func availableBytes(dir string) (int64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, err
	}
	return int64(st.Bavail) * int64(st.Bsize), nil
}
