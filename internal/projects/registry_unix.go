//go:build unix

package projects

import (
	"io/fs"
	"os"
	"syscall"
)

func ownedWithMode(info fs.FileInfo, forbidden fs.FileMode) bool {
	return info.Mode().Perm()&forbidden == 0 && info.Sys().(*syscall.Stat_t).Uid == uint32(os.Geteuid())
}

func openRegistry(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
}

func lockRegistry(dir *os.File) error {
	return syscall.Flock(int(dir.Fd()), syscall.LOCK_EX)
}
