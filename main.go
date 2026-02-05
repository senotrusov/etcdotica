//  Copyright 2025 Stanislav Senotrusov
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
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// stringArray implements flag.Value to handle repeated arguments
type stringArray []string

func (s *stringArray) String() string {
	return strings.Join(*s, ", ")
}

func (s *stringArray) Set(value string) error {
	*s = append(*s, value)
	return nil
}

// Config holds command line configuration
type Config struct {
	Watch    bool
	BinDirs  []string
	Everyone bool
}

// fileMeta stores metadata for change detection
type fileMeta struct {
	ModTime time.Time
	Size    int64
	Mode    os.FileMode
}

// Global logger setup
var (
	logger = log.New(os.Stderr, "", 0) // No timestamp, plain output to stderr
)

// Regex for detecting section files: e.g. "etc/fstab.external-disks-section"
// Group 1: Target base path ("etc/fstab")
// Group 2: Section name ("external-disks")
var sectionFileRx = regexp.MustCompile(`^(.+)\.([^./]+)-section$`)

// Regex for detecting section markers in content
var (
	beginSectionRx = regexp.MustCompile(`^# BEGIN (.+)$`)
	endSectionRx   = regexp.MustCompile(`^# END (.+)$`)
)

func main() {
	// 1. Setup Flags
	watchFlag := flag.Bool("watch", false, "Watch mode: scan continuously for changes")
	srcFlag := flag.String("src", "", "Source directory (default: current working directory)")
	dstFlag := flag.String("dst", "", "Destination directory (default: user home directory, or / if root)")
	umaskFlag := flag.String("umask", "", "Set process umask (octal, e.g. 077)")
	everyoneFlag := flag.Bool("everyone", false, "Set permissions to world-readable (and executable if user-executable), disregarding source group/other bits")
	var binDirs stringArray
	flag.Var(&binDirs, "bindir", "Directory relative to source directory where files must be executable (can be repeated)")
	flag.Parse()

	// 2. Configure Umask
	var processUmask os.FileMode
	if *umaskFlag != "" {
		val, err := strconv.ParseUint(*umaskFlag, 8, 32)
		if err != nil {
			logger.Fatalf("Error parsing umask flag: %v", err)
		}
		sysMask := int(val)
		syscall.Umask(sysMask)
		processUmask = os.FileMode(sysMask)
	} else {
		// syscall.Umask returns the old mask and sets the new one.
		// We read it and immediately restore it.
		sysMask := syscall.Umask(0)
		syscall.Umask(sysMask)
		processUmask = os.FileMode(sysMask)
	}

	// 3. Determine Source and Destination Paths
	var absSrc string
	var err error

	if *srcFlag != "" {
		absSrc, err = filepath.Abs(*srcFlag)
		if err != nil {
			logger.Fatalf("Error resolving source path: %v", err)
		}
	} else {
		absSrc, err = os.Getwd()
		if err != nil {
			logger.Fatalf("Error getting current working directory: %v", err)
		}
	}

	var absDst string
	if *dstFlag != "" {
		absDst, err = filepath.Abs(*dstFlag)
		if err != nil {
			logger.Fatalf("Error resolving destination path: %v", err)
		}
	} else {
		currentUser, err := user.Current()
		if err != nil {
			logger.Fatalf("Error getting current user info: %v", err)
		}
		isRoot := currentUser.Uid == "0"

		absDst = currentUser.HomeDir
		if isRoot {
			absDst = "/"
		}
	}

	// Safety check: prevent operations where source and destination are the same
	if absSrc == absDst {
		logger.Fatalf("Error: source and destination directories are the same (%s). Operation canceled to prevent unintended modifications.", absSrc)
	}

	cfg := Config{
		Watch:    *watchFlag,
		BinDirs:  binDirs,
		Everyone: *everyoneFlag,
	}

	// 4. Setup State File Path
	stateFilePath := filepath.Join(absSrc, ".etcdotica")

	// 5. Initial State Load
	currentState, err := loadState(stateFilePath)
	if err != nil {
		currentState = make(map[string]struct{})
	}

	// 6. Run Loop
	// Cache stores metadata to detect changes in watch mode
	metaCache := make(map[string]fileMeta)

	for {
		// Ensure executable bits are set in specified bin directories before syncing
		ensureExecBits(absSrc, cfg.BinDirs, processUmask)

		// Perform Sync
		newState, changed, err := runSync(absSrc, absDst, cfg, currentState, metaCache, processUmask)
		if err != nil {
			logger.Printf("Sync error: %v", err)
			if !cfg.Watch {
				os.Exit(1)
			}
		} else {
			// Update in-memory state for the next iteration
			currentState = newState

			// Save State only if changes occurred
			if changed {
				if err := saveState(stateFilePath, currentState); err != nil {
					logger.Printf("Error saving state: %v", err)
				}
			}
		}

		if !cfg.Watch {
			break
		}

		time.Sleep(2 * time.Second)
	}
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
	// Example: if umask is 077, ^umask masks out Group and Other, so we only enforce User exec (0100).
	targetModeBits := os.FileMode(0111) & ^umask

	for _, relDir := range binDirs {
		absDir := filepath.Join(srcRoot, relDir)

		// Check if the directory exists; if not, just skip it.
		info, err := os.Stat(absDir)
		if err != nil || !info.IsDir() {
			continue
		}

		// Walk the directory to process files
		err = filepath.Walk(absDir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				// Skip unreadable files/directories
				return nil
			}

			// We only care about ensuring executable bits on files
			if info.IsDir() {
				return nil
			}

			// filepath.Walk uses Lstat. We use Stat here to follow symlinks.
			// If a symlink exists in the binDir, we generally want the target to be executable.
			realInfo, err := os.Stat(path)
			if err != nil {
				return nil
			}

			if realInfo.IsDir() {
				return nil
			}

			currentMode := realInfo.Mode()

			// Check if the required executable bits are present.
			// (currentMode & targetModeBits) == targetModeBits implies all bits in targetModeBits are set.
			if currentMode&targetModeBits != targetModeBits {
				// We don't unset any bits; we only add the required ones.
				newMode := currentMode | targetModeBits
				if err := os.Chmod(path, newMode); err != nil {
					logger.Printf("Warning: failed to set exec bit on %s: %v", path, err)
				}
			}
			return nil
		})

		if err != nil {
			logger.Printf("Warning: error scanning bindir %s: %v", absDir, err)
		}
	}
}

// runSync performs the core synchronization logic.
func runSync(src, dst string, cfg Config, oldState map[string]struct{}, metaCache map[string]fileMeta, umask os.FileMode) (map[string]struct{}, bool, error) {
	newState := make(map[string]struct{})
	processedFiles := make(map[string]bool)
	changed := false

	// Walk Source
	err := filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Calculate relative path
		relPath, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}

		if relPath == "." {
			return nil
		}

		// Handle .etcdotica
		if relPath == ".etcdotica" {
			if info.IsDir() {
				return fmt.Errorf("conflict: .etcdotica source path is a directory, expected state file")
			}
			return nil
		}

		// Ignore .git directory
		if info.IsDir() && info.Name() == ".git" {
			return filepath.SkipDir
		}

		// Resolve Symlinks
		// filepath.Walk uses Lstat (gets link info). We must use Stat (follow link)
		// to get the actual file info for correct mtime comparison and permission copying.
		realInfo, err := os.Stat(path)
		if err != nil {
			logger.Printf("Warning: skipping unreadable file or broken link %s: %v", relPath, err)
			// Mark processed to prevent pruning on read error
			processedFiles[relPath] = true
			return nil
		}

		targetPath := filepath.Join(dst, relPath)

		// Capture current metadata
		currentMeta := fileMeta{
			ModTime: realInfo.ModTime(),
			Size:    realInfo.Size(),
			Mode:    realInfo.Mode(),
		}

		// Check if this is a Special Section File
		if !realInfo.IsDir() {
			sectionMatch := sectionFileRx.FindStringSubmatch(relPath)
			if sectionMatch != nil {
				// It is a section file.
				// Group 1 is the target relative path (e.g. "etc/fstab")
				// Group 2 is the section name (e.g. "external-disks")
				targetRelPath := sectionMatch[1]
				sectionName := sectionMatch[2]
				targetAbsPath := filepath.Join(dst, targetRelPath)

				// We treat the section source file as "processed" so it is not pruned,
				// but we do NOT copy it as a file to the destination.
				// Instead, we merge its content into the target file.
				newState[relPath] = struct{}{}
				processedFiles[relPath] = true

				// Watch optimization for section files
				if cfg.Watch {
					lastMeta, known := metaCache[path]
					if known &&
						lastMeta.ModTime.Equal(currentMeta.ModTime) &&
						lastMeta.Size == currentMeta.Size &&
						lastMeta.Mode == currentMeta.Mode {
						return nil
					}
					metaCache[path] = currentMeta
				}

				// Perform the Merge
				didChange, err := mergeSection(path, targetAbsPath, sectionName, realInfo, umask, cfg.Everyone)
				if err != nil {
					logger.Printf("Failed to merge section %s into %s: %v", sectionName, targetAbsPath, err)
				} else if didChange {
					changed = true
				}
				return nil
			}
		}

		// Watch optimization for standard files: skip if metadata hasn't changed
		if cfg.Watch {
			lastMeta, known := metaCache[path]
			if known &&
				lastMeta.ModTime.Equal(currentMeta.ModTime) &&
				lastMeta.Size == currentMeta.Size &&
				lastMeta.Mode == currentMeta.Mode {
				// We still need to record it in newState to prevent pruning
				if _, ok := oldState[relPath]; ok {
					newState[relPath] = struct{}{}
					processedFiles[relPath] = true
					return nil
				}
			}
			metaCache[path] = currentMeta
		}

		// Calculate the expected permissions
		var expectedPerms os.FileMode
		if cfg.Everyone {
			// Start with base read for everyone (0444)
			permBits := os.FileMode(0444)

			// If source has User Write (0200), add User Write
			if currentMeta.Mode&0200 != 0 {
				permBits |= 0200
			}

			// If source has User Exec (0100), add Exec for User, Group, Other (0111)
			if currentMeta.Mode&0100 != 0 {
				permBits |= 0111
			}

			// Apply umask
			expectedPerms = permBits & ^umask
		} else {
			// Standard behavior: mask source perms with umask
			expectedPerms = currentMeta.Mode.Perm() & ^umask
		}

		// Handle Directory
		if realInfo.IsDir() {
			// Check for conflict: Dest exists and is a file.
			// We use Stat to follow symlinks. If dst is a symlink to a directory,
			// Stat returns IsDir() == true, and we allow it.
			dstInfo, err := os.Stat(targetPath)
			if err == nil && !dstInfo.IsDir() {
				logger.Printf("Conflict: src is dir, dst is file. Skipping %s", targetPath)
				return nil
			}

			// Create the directory if it doesn't exist.
			// If it exists (even as a symlink to a dir), MkdirAll returns nil (success).
			if err := os.MkdirAll(targetPath, expectedPerms); err != nil {
				logger.Printf("Failed to create dir %s: %v", targetPath, err)
				return nil
			}
			return nil
		}

		// Handle File
		processedFiles[relPath] = true

		// Use Lstat to check destination state so we can detect symlinks
		dstInfo, err := os.Lstat(targetPath)
		dstExists := err == nil

		if dstExists {
			// If destination is a symlink, we must remove it.
			// - If it links to a file: writing would overwrite the target (bad).
			// - If it links to a dir: we want to replace it with the source file.
			if dstInfo.Mode()&os.ModeSymlink != 0 {
				if err := os.Remove(targetPath); err != nil {
					logger.Printf("Error removing destination symlink %s: %v", targetPath, err)
					return nil
				}
				// We treated the symlink as an invalid state and removed it.
				// Now we proceed as if the file does not exist.
				dstExists = false
			} else if dstInfo.IsDir() {
				// Conflict Check: Dest exists and is a directory
				logger.Printf("Conflict: src is file, dst is dir. Skipping %s", targetPath)
				return nil
			}
		}

		// Record state
		newState[relPath] = struct{}{}

		// 1. File does not exist: Full install
		if !dstExists {
			if err := installFile(path, targetPath, realInfo, expectedPerms); err != nil {
				logger.Printf("Failed to install %s: %v", targetPath, err)
				return nil
			}
			changed = true
			return nil
		}

		// 2. File exists: Check Content (Size & Mtime)
		contentMismatch := false
		if currentMeta.Size != dstInfo.Size() || !currentMeta.ModTime.Equal(dstInfo.ModTime()) {
			contentMismatch = true
		}

		if contentMismatch {
			if err := installFile(path, targetPath, realInfo, expectedPerms); err != nil {
				logger.Printf("Failed to update %s: %v", targetPath, err)
				return nil
			}
			changed = true
			return nil
		}

		// 3. Content matches: Check Permissions
		// If the destination permissions differ from expected, sync them.
		if dstInfo.Mode().Perm() != expectedPerms {
			if err := os.Chmod(targetPath, expectedPerms); err != nil {
				logger.Printf("Warning: failed to chmod %s: %v", targetPath, err)
			}
			// Chmod might not affect mtime, but we ensure consistency
			if err := os.Chtimes(targetPath, currentMeta.ModTime, currentMeta.ModTime); err != nil {
				logger.Printf("Warning: failed to chtimes %s: %v", targetPath, err)
			}
			changed = true
		}

		return nil
	})

	if err != nil {
		return nil, false, err
	}

	// Pruning
	for oldRelPath := range oldState {
		if !processedFiles[oldRelPath] {
			targetPath := filepath.Join(dst, oldRelPath)

			// Remove orphaned file. Do not remove directories.
			err := os.Remove(targetPath)
			if err == nil {
				changed = true
			} else if !os.IsNotExist(err) {
				logger.Printf("Failed to remove orphaned file %s: %v", targetPath, err)
			}
		}
	}

	return newState, changed, nil
}

// installFile copies content and forces the specific calculated permissions.
// It acquires an exclusive lock on the destination file during the write operation
// to prevent concurrent modifications.
func installFile(src, dst string, info os.FileInfo, perm os.FileMode) error {
	s, err := os.Open(src)
	if err != nil {
		return err
	}
	defer s.Close()

	// 1. Create/Write file.
	// We use O_WRONLY|O_CREATE but explicitly AVOID O_TRUNC here.
	// If we used O_TRUNC, we might wipe the file while another process holds the lock
	// but hasn't finished writing, or before we strictly own the lock.
	d, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE, perm)
	if err != nil {
		return err
	}

	// 2. Acquire Exclusive Lock
	// We must lock before we modify the content (truncate/write).
	if err := syscall.Flock(int(d.Fd()), syscall.LOCK_EX); err != nil {
		d.Close()
		return err
	}

	// 3. Truncate
	// Now that we possess the exclusive lock, it is safe to reset the file size.
	if err := d.Truncate(0); err != nil {
		d.Close()
		return err
	}

	// 4. Copy Content
	if _, err := io.Copy(d, s); err != nil {
		d.Close()
		return err
	}

	// 5. Close (Releases Lock)
	// Explicitly closing allows us to check for write errors, and implicitly releases the flock.
	if err := d.Close(); err != nil {
		return err
	}

	// 6. Sync Permissions
	// OpenFile only applies mode on creation. If the file existed, mode is ignored.
	// We explicit chmod to the calculated permission to handle updates and ensure correctness.
	if err := os.Chmod(dst, perm); err != nil {
		logger.Printf("Warning: failed to chmod %s: %v", dst, err)
	}

	// 7. Sync Mtime
	if err := os.Chtimes(dst, info.ModTime(), info.ModTime()); err != nil {
		logger.Printf("Warning: failed to set mtime on %s: %v", dst, err)
	}

	return nil
}

// chunk represents a part of the file, either raw text or a named section.
type chunk struct {
	isSection bool
	name      string // empty if raw text
	lines     []string
}

// mergeSection reads the source section file and merges it into the target file.
// It respects the alphabetical ordering of sections and safety checks for broken tags.
func mergeSection(srcPath, dstPath, sectionName string, srcInfo os.FileInfo, umask os.FileMode, everyone bool) (bool, error) {
	// 1. Read the new section content from source
	srcBytes, err := os.ReadFile(srcPath)
	if err != nil {
		return false, err
	}
	srcLines := strings.Split(string(srcBytes), "\n")
	// Trim trailing empty line if resulting from split
	if len(srcLines) > 0 && srcLines[len(srcLines)-1] == "" {
		srcLines = srcLines[:len(srcLines)-1]
	}

	// 2. Determine Expected Permissions
	// If the destination file exists, we want to preserve its current permissions
	// (sanitized by umask). If it doesn't exist, we calculate permissions based
	// on the source file and configuration, similar to runSync.
	var expectedPerms os.FileMode

	// We use os.Stat (follows symlinks) because if it's a symlink, we care about the target's mode.
	dstInfo, err := os.Stat(dstPath)
	if err == nil {
		// Conflict Check: If destination exists and is a directory, we cannot merge into it.
		if dstInfo.IsDir() {
			return false, fmt.Errorf("conflict: target path %s is a directory", dstPath)
		}
		// Dest exists: Keep existing mode, but strip umask bits
		expectedPerms = dstInfo.Mode().Perm() & ^umask
	} else {
		// Dest does not exist (or is broken link): Calculate from source
		if everyone {
			// Start with base read for everyone (0444)
			permBits := os.FileMode(0444)

			// If source has User Write (0200), add User Write
			if srcInfo.Mode()&0200 != 0 {
				permBits |= 0200
			}

			// If source has User Exec (0100), add Exec for User, Group, Other (0111)
			if srcInfo.Mode()&0100 != 0 {
				permBits |= 0111
			}

			// Apply umask
			expectedPerms = permBits & ^umask
		} else {
			// Standard behavior: mask source perms with umask
			expectedPerms = srcInfo.Mode().Perm() & ^umask
		}
	}

	// 3. Open Destination File (Read/Write, Create if missing)
	// We need to lock it to prevent race conditions.
	// os.OpenFile uses expectedPerms only if the file is created.
	f, err := os.OpenFile(dstPath, os.O_RDWR|os.O_CREATE, expectedPerms)
	if err != nil {
		// If failure is due to missing dir, try creating dir first
		if os.IsNotExist(err) {
			if err := os.MkdirAll(filepath.Dir(dstPath), 0755 & ^umask); err != nil {
				return false, err
			}
			f, err = os.OpenFile(dstPath, os.O_RDWR|os.O_CREATE, expectedPerms)
			if err != nil {
				return false, err
			}
		} else {
			return false, err
		}
	}
	defer f.Close()

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return false, err
	}

	// 4. Read existing content
	// ReadAll reads from current offset (0)
	content, err := io.ReadAll(f)
	if err != nil {
		return false, err
	}
	oldLines := strings.Split(string(content), "\n")
	// Handle case where file is empty or ends with newline
	if len(oldLines) > 0 && oldLines[len(oldLines)-1] == "" {
		oldLines = oldLines[:len(oldLines)-1]
	}

	// 5. Parse Blocks
	blocks, err := parseBlocks(oldLines, sectionName)
	if err != nil {
		// If specific error (e.g. broken target section), we abort
		return false, fmt.Errorf("parsing target file: %v", err)
	}

	// 6. Construct New Section Chunk
	newSectionLines := make([]string, 0, len(srcLines)+2)
	newSectionLines = append(newSectionLines, fmt.Sprintf("# BEGIN %s", sectionName))
	newSectionLines = append(newSectionLines, srcLines...)
	newSectionLines = append(newSectionLines, fmt.Sprintf("# END %s", sectionName))

	newChunk := chunk{
		isSection: true,
		name:      sectionName,
		lines:     newSectionLines,
	}

	// 7. Merge Logic
	var newBlocks []chunk
	inserted := false

	// Strategy:
	// Iterate through existing blocks.
	// If we find our section -> Replace it.
	// If we find a section strictly GREATER than ours -> Insert before it.
	// If raw -> Keep.
	for _, b := range blocks {
		if inserted {
			// Already inserted/replaced, just append the rest, skipping if it was the old version of our section
			if b.isSection && b.name == sectionName {
				continue
			}
			newBlocks = append(newBlocks, b)
			continue
		}

		if b.isSection {
			if b.name == sectionName {
				// Found existing section: Replace
				newBlocks = append(newBlocks, newChunk)
				inserted = true
			} else if sectionName < b.name {
				// Found a section that comes alphabetically AFTER ours.
				// We must insert ours BEFORE this one.
				newBlocks = append(newBlocks, newChunk)
				newBlocks = append(newBlocks, b)
				inserted = true
			} else {
				// Current section is smaller (before) ours. Keep looking.
				newBlocks = append(newBlocks, b)
			}
		} else {
			// Raw text block
			newBlocks = append(newBlocks, b)
		}
	}

	if !inserted {
		// If we reached the end without inserting, append to the end
		newBlocks = append(newBlocks, newChunk)
	}

	// 8. Serialize
	var buf bytes.Buffer
	for i, b := range newBlocks {
		for _, line := range b.lines {
			buf.WriteString(line)
			buf.WriteByte('\n')
		}
		// Try to avoid excessive newlines between sections if raw blocks are empty,
		// but typically we just reconstruct exactly what was there.
		// The simple reconstruction above puts a newline after every line.
		// NOTE: If the original file had no trailing newline, this adds one.
		_ = i
	}

	// Check if content actually changed
	newBytes := buf.Bytes()
	contentChanged := !bytes.Equal(content, newBytes)

	// 9. Write Back
	if contentChanged {
		if err := f.Truncate(0); err != nil {
			return false, err
		}
		if _, err := f.Seek(0, 0); err != nil {
			return false, err
		}
		if _, err := f.Write(newBytes); err != nil {
			return false, err
		}
	}

	// 10. Check and Sync Permissions
	// Even if content matches, we must ensure permissions are correct (like runSync).
	// We use f.Stat() and f.Chmod() which operate on the open file handle (and follow symlinks correctly).
	// If dstPath is a symlink, f refers to the target file, so we chmod the target file.
	currentStat, err := f.Stat()
	if err == nil {
		if currentStat.Mode().Perm() != expectedPerms {
			if err := f.Chmod(expectedPerms); err != nil {
				logger.Printf("Warning: failed to chmod %s: %v", dstPath, err)
			} else {
				// Mark as changed so state is saved
				contentChanged = true
			}
		}
	}

	return contentChanged, nil
}

// parseBlocks reads lines and groups them into chunks (Raw vs Named Sections).
// It validates that if the specific targetSectionName is present, it is well-formed.
// Other malformed sections are treated as raw text to avoid destruction.
func parseBlocks(lines []string, targetSectionName string) ([]chunk, error) {
	var blocks []chunk

	// Scan first to validate the target section isn't broken
	// A broken target section (e.g. BEGIN X without END X) causes an abort.
	// A broken unrelated section (e.g. BEGIN Y without END Y) is treated as raw text.
	// To implement this, we identify valid ranges first.
	type span struct {
		start, end int // inclusive
		name       string
	}
	validSections := []span{}

	for i := 0; i < len(lines); i++ {
		match := beginSectionRx.FindStringSubmatch(lines[i])
		if match != nil {
			name := match[1]
			// Look ahead for END
			foundEnd := -1
			// Nested sections are not supported/expected, scan linearly
			for j := i + 1; j < len(lines); j++ {
				endMatch := endSectionRx.FindStringSubmatch(lines[j])
				if endMatch != nil && endMatch[1] == name {
					foundEnd = j
					break
				}
				// If we see a BEGIN for the SAME name nested, that's definitely broken
				beginMatch := beginSectionRx.FindStringSubmatch(lines[j])
				if beginMatch != nil && beginMatch[1] == name {
					break
				}
			}

			if foundEnd != -1 {
				validSections = append(validSections, span{i, foundEnd, name})
				i = foundEnd // Advance outer loop
			} else {
				// Opening tag without closing tag
				if name == targetSectionName {
					return nil, fmt.Errorf("found opening tag for section '%s' at line %d but no closing tag", name, i+1)
				}
				// Treat as raw text
			}
		} else {
			// Check for orphaned END tags of target
			endMatch := endSectionRx.FindStringSubmatch(lines[i])
			if endMatch != nil && endMatch[1] == targetSectionName {
				return nil, fmt.Errorf("found orphaned closing tag for section '%s' at line %d", targetSectionName, i+1)
			}
		}
	}

	// Now build blocks based on valid sections
	lineIdx := 0
	for _, sec := range validSections {
		// Add raw text before this section
		if sec.start > lineIdx {
			blocks = append(blocks, chunk{
				isSection: false,
				lines:     lines[lineIdx:sec.start],
			})
		}
		// Add the section
		blocks = append(blocks, chunk{
			isSection: true,
			name:      sec.name,
			lines:     lines[sec.start : sec.end+1],
		})
		lineIdx = sec.end + 1
	}

	// Add remaining raw text
	if lineIdx < len(lines) {
		blocks = append(blocks, chunk{
			isSection: false,
			lines:     lines[lineIdx:],
		})
	}

	return blocks, nil
}

// loadState reads the state file.
func loadState(path string) (map[string]struct{}, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	// Acquire a shared lock to allow concurrent reads but block during writes
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_SH); err != nil {
		return nil, err
	}

	state := make(map[string]struct{})
	scanner := bufio.NewScanner(f)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			state[line] = struct{}{}
		}
	}

	return state, scanner.Err()
}

// saveState writes the relative source paths to the state file, one per line.
// It sorts the keys to ensure deterministic output.
func saveState(path string, state map[string]struct{}) error {
	// Open with O_RDWR and O_CREATE to ensure existence and writeability,
	// but avoid O_TRUNC here to prevent data loss before locking.
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0666)
	if err != nil {
		return err
	}
	defer f.Close()

	// Acquire an exclusive lock to prevent concurrent writes or reads
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}

	// If running as root, attempt to match the file ownership to the parent directory.
	// This ensures that if sudo is used to run the tool, the state file remains
	// owned by the user who owns the source directory, keeping it readable/writable
	// for them in the future.
	if os.Getuid() == 0 {
		dir := filepath.Dir(path)
		if info, err := os.Stat(dir); err == nil {
			if stat, ok := info.Sys().(*syscall.Stat_t); ok {
				// Best-effort attempt to change ownership. We ignore errors
				// (e.g. filesystem quirks) to avoid blocking the sync operation.
				_ = f.Chown(int(stat.Uid), int(stat.Gid))
			}
		}
	}

	// Now that we have the lock, truncate the file to overwrite content
	if err := f.Truncate(0); err != nil {
		return err
	}

	// Ensure we are at the beginning of the file
	if _, err := f.Seek(0, 0); err != nil {
		return err
	}

	// Sort keys to prevent random file changes
	keys := make([]string, 0, len(state))
	for k := range state {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, srcPath := range keys {
		line := fmt.Sprintf("%s\n", srcPath)
		if _, err := f.WriteString(line); err != nil {
			return err
		}
	}

	// Flush writes to stable storage
	return f.Sync()
}
