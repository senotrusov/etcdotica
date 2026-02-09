//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"strconv"

	"golang.org/x/sys/unix"
)

// setupUmask configures the process umask using unix syscalls.
func setupUmask(umaskStr string) os.FileMode {
	if umaskStr != "" {
		val, err := strconv.ParseUint(umaskStr, 8, 32)
		if err != nil {
			logger.Fatalf("Error parsing umask flag: %v", err)
		}
		unix.Umask(int(val))
		return os.FileMode(val)
	}
	// unix.Umask returns the old mask and sets the new one.
	// We read it and immediately restore it.
	sysMask := unix.Umask(0)
	unix.Umask(sysMask)
	return os.FileMode(sysMask)
}

// lockFile acquires an advisory lock on the file descriptor.
// It blocks until the lock is obtained.
func lockFile(fd uintptr, exclusive bool) error {
	how := unix.LOCK_SH
	if exclusive {
		how = unix.LOCK_EX
	}
	return unix.Flock(int(fd), how)
}

// ensureStateOwnership attempts to set the ownership of the state file
// to match the parent directory if the process is running as root.
func ensureStateOwnership(f *os.File, path string) {
	if os.Getuid() != 0 {
		return
	}
	dir := filepath.Dir(path)

	// Use unix.Stat to avoid dependency on deprecated syscall package for Stat_t
	var stat unix.Stat_t
	if err := unix.Stat(dir, &stat); err != nil {
		return
	}
	// Best-effort attempt to change ownership.
	_ = f.Chown(int(stat.Uid), int(stat.Gid))
}

// calculatePerms determines the target file permissions based on Unix conventions.
func calculatePerms(srcMode os.FileMode, umask os.FileMode, everyone bool) os.FileMode {
	if !everyone {
		// Standard behavior: mask source perms with umask
		return srcMode.Perm() & ^umask
	}
	// Base read for everyone (0444)
	permBits := os.FileMode(0444)

	// Propagate write bit: if User Write (0200) -> add Write for all (0222)
	if srcMode&0200 != 0 {
		permBits |= 0222
	}
	// Propagate exec bit: if User Exec (0100) -> add Exec for all (0111)
	if srcMode&0100 != 0 {
		permBits |= 0111
	}
	return permBits & ^umask
}

// ensureExecBits iterates over provided directories and ensures files have
// the correct executable bits set, respecting the process umask.
func ensureExecBits(srcRoot string, binDirs []string, umask os.FileMode) {
	if len(binDirs) == 0 {
		return
	}
	// Calculate the executable bits we want to enforce.
	// 0111 are the standard executable bits for User, Group, Other.
	// We mask them with the inverse of the umask.
	targetModeBits := os.FileMode(0111) & ^umask

	for _, relDir := range binDirs {
		absDir := filepath.Join(srcRoot, relDir)
		if info, err := os.Stat(absDir); err != nil || !info.IsDir() {
			continue
		}
		processExecDir(absDir, targetModeBits)
	}
}

// processExecDir walks a single bin directory.
func processExecDir(dir string, targetBits os.FileMode) {
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // Skip unreadable
		}
		if info.IsDir() {
			return nil
		}
		// filepath.Walk uses Lstat. We use Stat here to follow symlinks.
		realInfo, err := os.Stat(path)
		if err != nil || realInfo.IsDir() {
			return nil
		}

		// Check if the required executable bits are present.
		if realInfo.Mode()&targetBits != targetBits {
			// We don't unset any bits; we only add the required ones.
			if err := os.Chmod(path, realInfo.Mode()|targetBits); err != nil {
				logger.Printf("Warning: failed to set exec bit on %s: %v", path, err)
			}
		}
		return nil
	})
	if err != nil {
		logger.Printf("Warning: error scanning bindir %s: %v", dir, err)
	}
}
