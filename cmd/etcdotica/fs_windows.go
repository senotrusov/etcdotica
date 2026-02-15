// Copyright 2025-2026 Stanislav Senotrusov
//
// This work is dual-licensed under the Apache License, Version 2.0 and the MIT License.
// See LICENSE-APACHE and LICENSE-MIT in the top-level directory for details.
//
// SPDX-License-Identifier: Apache-2.0 OR MIT

//go:build windows

package main

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// setupUmask is a no-op on Windows as it doesn't use umask in the same way.
func setupUmask(_ string) os.FileMode {
	return 0
}

// lockFile acquires an exclusive or shared lock on the file.
// It matches Unix Flock behavior by blocking until the lock is acquired.
func lockFile(fd uintptr, exclusive bool) error {
	var flags uint32
	if exclusive {
		flags = windows.LOCKFILE_EXCLUSIVE_LOCK
	}
	// windows.LockFileEx with 0 flags (no FAIL_IMMEDIATELY) blocks until acquired.

	var ov windows.Overlapped
	// Lock the entire file. Region: 0 to Max (High/Low 0xFFFFFFFF)
	err := windows.LockFileEx(windows.Handle(fd), flags, 0, 0xFFFFFFFF, 0xFFFFFFFF, &ov)
	if err != nil {
		return fmt.Errorf("LockFileEx: %v", err)
	}
	return nil
}

// ensureStateOwnership is a no-op on Windows.
func ensureStateOwnership(_ *os.File, _ string) {}

// calculatePerms returns the source permissions as-is for Windows.
// Complex permission mapping is skipped to fit Windows file attributes.
func calculatePerms(srcMode os.FileMode, _ os.FileMode, _ bool) os.FileMode {
	return srcMode.Perm()
}

// ensureExecBits is a no-op on Windows.
// Executability on Windows is determined by file extension, not permission bits.
func ensureExecBits(_ string, _ []string, _ os.FileMode) {}
