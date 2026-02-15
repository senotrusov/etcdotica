// Copyright 2025-2026 Stanislav Senotrusov
//
// This work is dual-licensed under the Apache License, Version 2.0 and the MIT License.
// See LICENSE-APACHE and LICENSE-MIT in the top-level directory for details.
//
// SPDX-License-Identifier: Apache-2.0 OR MIT

package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// syncFile copies content and forces the specific calculated permissions.
// It optimizes by checking if content is already identical (size & bytes) to avoid writing.
// It acquires an exclusive lock on the destination file during the operation.
func syncFile(src, dst string, info os.FileInfo, perm os.FileMode) error {
	logger.Debug("Syncing file", "src", src, "dst", dst)
	s, err := os.Open(src)
	if err != nil {
		return err
	}
	defer s.Close()

	// Acquire Shared Lock on Source
	if err := lockFile(s.Fd(), false); err != nil {
		return fmt.Errorf("locking source file: %v", err)
	}

	// 1. Open destination.
	// We use O_RDWR|O_CREATE to allow reading for content comparison optimization.
	// We explicitly AVOID O_TRUNC here to prevent wiping the file before we acquire the lock.
	d, err := os.OpenFile(dst, os.O_RDWR|os.O_CREATE, perm)
	if err != nil {
		return err
	}

	// 2. Acquire Exclusive Lock. Must lock before modifying content.
	if err := lockFile(d.Fd(), true); err != nil {
		d.Close()
		return err
	}

	// Optimization: Compare content if sizes match to avoid unnecessary writes.
	var sameContent bool
	if dInfo, err := d.Stat(); err == nil && dInfo.Size() == info.Size() {
		if match, err := contentsEqual(s, d); err == nil && match {
			sameContent = true
			logger.Debug("Skipping copy: content identical", "path", dst)
		}
		// Reset source cursor for subsequent operations (copy or verify)
		if _, err := s.Seek(0, 0); err != nil {
			d.Close()
			return fmt.Errorf("resetting source cursor: %v", err)
		}
	}

	if !sameContent {
		// 3. Truncate. Now that we possess the exclusive lock and confirmed content differs, it is safe to reset file size.
		if err := d.Truncate(0); err != nil {
			d.Close()
			return err
		}

		// Reset destination cursor (it may have been advanced by contentsEqual)
		if _, err := d.Seek(0, 0); err != nil {
			d.Close()
			return err
		}

		// 4. Copy Content
		if _, err := io.Copy(d, s); err != nil {
			d.Close()
			return err
		}
	}

	// 5. Sync Permissions
	// OpenFile only applies mode on creation. Use Fd to be safe against symlink races.
	if err := d.Chmod(perm); err != nil {
		d.Close()
		return err
	}

	// 6. Close (Releases Lock)
	if err := d.Close(); err != nil {
		return err
	}

	// 7. Sync Mtime
	// This is the critical moment where a race can happen.
	if err := os.Chtimes(dst, info.ModTime(), info.ModTime()); err != nil {
		logger.Warn("Failed to set mtime", "path", dst, "err", err)
	}

	// 8. Verification (Mitigate TOCTOU)
	return verifyContent(s, dst)
}

// verifyContent checks if the file on disk matches the source file byte-by-byte.
// If content differs (modification between Close and Chtimes), it touches the file
// to force a resync on the next run.
func verifyContent(src *os.File, dstPath string) error {
	// Reset source cursor
	if _, err := src.Seek(0, 0); err != nil {
		return fmt.Errorf("seeking source file for verification: %v", err)
	}

	d, err := os.Open(dstPath)
	if err != nil {
		return fmt.Errorf("verify open failed: %v", err)
	}
	defer d.Close()

	if err := lockFile(d.Fd(), false); err != nil {
		return fmt.Errorf("verify lock failed: %v", err)
	}

	match, err := contentsEqual(src, d)
	if err != nil {
		return fmt.Errorf("verify content check failed: %v", err)
	}

	if !match {
		// Mismatch detected
		logger.Warn("Content mismatch detected. Updating mtime to force sync.", "path", dstPath)
		now := time.Now()
		if err := os.Chtimes(dstPath, now, now); err != nil {
			return fmt.Errorf("failed to update mtime after content mismatch: %v", err)
		}
	}
	return nil
}

// contentsEqual compares two readers byte-by-byte.
func contentsEqual(r1, r2 io.Reader) (bool, error) {
	const chunkSize = 64 * 1024
	buf1 := make([]byte, chunkSize)
	buf2 := make([]byte, chunkSize)

	for {
		n1, err1 := r1.Read(buf1)
		n2, err2 := r2.Read(buf2)

		if err1 != nil || err2 != nil {
			if err1 == io.EOF && err2 == io.EOF {
				return true, nil // Files match
			}
			if err1 == io.EOF || err2 == io.EOF {
				return false, nil // Mismatch (length differs)
			}
			// Actual read error
			return false, fmt.Errorf("read error: src=%v, dst=%v", err1, err2)
		}

		if n1 != n2 || !bytes.Equal(buf1[:n1], buf2[:n2]) {
			return false, nil // Mismatch (content differs)
		}
	}
}

// readLines reads a file and splits it into lines.
func readLines(path string) ([]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return splitLines(b), nil
}

// writeContent rewrites the file from the beginning.
func writeContent(f *os.File, data []byte) error {
	if err := f.Truncate(0); err != nil {
		return err
	}
	if _, err := f.Seek(0, 0); err != nil {
		return err
	}
	_, err := f.Write(data)
	return err
}

// splitLines breaks a byte slice into individual lines using the newline character.
// If the input ends with a newline, the resulting trailing empty string is removed
// to ensure the slice reflects actual lines of content.
func splitLines(b []byte) []string {
	lines := strings.Split(string(b), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}
