//go:build linux || darwin

package main

import (
	"os"
	"syscall"
)

func freeSpace(path string) (int64, error) {
	var stat syscall.Statfs_t
	e := syscall.Statfs(path, &stat)
	return int64(stat.Bavail) * int64(stat.Bsize), e
}
func lockWorkspace(path string) (*os.File, error) {
	f, e := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	if e = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		f.Close()
		return nil, e
	}
	return f, nil
}
