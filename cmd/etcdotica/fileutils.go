//  Copyright 2025-2026 Stanislav Senotrusov
//
//  Licensed under the Apache License, Version 2.0 (the "License");
//  you may not use this file except in compliance with the License.
//  You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
//  Unless required by applicable law or agreed to in writing, software
//  distributed under the License is distributed on an "AS IS" BASIS,
//  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
//  See the License for the specific language governing permissions and
//  limitations under the License.

package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// installFile copies content and forces the specific calculated permissions.
// It acquires an exclusive lock on the destination file during the write operation
// to prevent concurrent modifications.
func installFile(src, dst string, info os.FileInfo, perm os.FileMode) error {
	logger.Debug("Installing file", "src", src, "dst", dst)
	s, err := os.Open(src)
	if err != nil {
		return err
	}
	defer s.Close()

	// Acquire Shared Lock on Source
	if err := lockFile(s.Fd(), false); err != nil {
		return fmt.Errorf("locking source file: %v", err)
	}

	// 1. Create/Write file.
	// We use O_WRONLY|O_CREATE but explicitly AVOID O_TRUNC here.
	// If we used O_TRUNC, we might wipe the file while another process holds the lock
	// but hasn't finished writing, or before we strictly own the lock.
	d, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE, perm)
	if err != nil {
		return err
	}

	// 2. Acquire Exclusive Lock. Must lock before modifying content.
	if err := lockFile(d.Fd(), true); err != nil {
		d.Close()
		return err
	}

	// 3. Truncate. Now that we possess the exclusive lock, it is safe to reset file size.
	if err := d.Truncate(0); err != nil {
		d.Close()
		return err
	}

	// 4. Copy Content
	if _, err := io.Copy(d, s); err != nil {
		d.Close()
		return err
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

	const chunkSize = 64 * 1024
	srcBuf := make([]byte, chunkSize)
	dstBuf := make([]byte, chunkSize)

	for {
		n1, err1 := src.Read(srcBuf)
		n2, err2 := d.Read(dstBuf)

		if err1 != nil || err2 != nil {
			if err1 == io.EOF && err2 == io.EOF {
				return nil // Files match
			}
			if err1 == io.EOF || err2 == io.EOF {
				break // Mismatch (length differs)
			}
			// Actual read error
			return fmt.Errorf("verify read error: src=%v, dst=%v", err1, err2)
		}

		if n1 != n2 || !bytes.Equal(srcBuf[:n1], dstBuf[:n2]) {
			break // Mismatch (content differs)
		}
	}

	// Mismatch detected
	logger.Warn("Content mismatch detected. Updating mtime to force sync.", "path", dstPath)
	now := time.Now()
	if err := os.Chtimes(dstPath, now, now); err != nil {
		return fmt.Errorf("failed to update mtime after content mismatch: %v", err)
	}
	return nil
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
