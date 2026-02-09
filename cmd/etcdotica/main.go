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
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
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
	Watch        bool
	BinDirs      []string
	Everyone     bool
	Src          string
	Dst          string
	ProcessUmask os.FileMode
}

// fileMeta stores metadata for change detection
type fileMeta struct {
	ModTime time.Time
	Size    int64
	Mode    os.FileMode
}

// Global configuration and logger setup
var (
	logger *slog.Logger

	// watchRetryInterval defines the duration the program waits between
	// synchronization attempts when in watch mode or when recovering from
	// transient filesystem errors.
	watchRetryInterval = 4 * time.Second

	// Number of iterations between full scans.
	// This forces a re-validation of all destination files against the source,
	// correcting any configuration drift caused by external processes.
	// With a 4-second interval, this triggers a full scan roughly every 4 minutes.
	fullScanIterations = 60
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
	cfg := parseFlags()

	// Initial validation: Source must exist and be a directory on startup.
	// We only strictly require existence at start. Transient failures later
	// (in watch mode) are handled in the loop.
	if err := validateSource(cfg.Src); err != nil {
		logger.Error("Error validating source", "err", err)
		os.Exit(1)
	}

	stateFilePath := filepath.Join(cfg.Src, ".etcdotica")

	runLoop(cfg, stateFilePath)
}

// parseFlags handles command line argument parsing and configuration setup.
func parseFlags() Config {
	watchFlag := flag.Bool("watch", false, "Watch mode: scan continuously for changes")
	srcFlag := flag.String("src", "", "Source directory (required)")
	dstFlag := flag.String("dst", "", "Destination directory (default: user home directory, or / if root)")
	umaskFlag := flag.String("umask", "", "Set process umask (octal, e.g. 077)")
	everyoneFlag := flag.Bool("everyone", false, "Set group and other permissions to the same permission bits as the owner, then apply the umask to the resulting mode.")
	logFormat := flag.String("log-format", "human", "Log format: human, text or json")
	logLevel := flag.String("log-level", "info", "Log level: debug, info, warn, error")

	var binDirs stringArray
	flag.Var(&binDirs, "bindir", "Directory relative to the source directory in which all files will be ensured to have the executable bit set (can be repeated)")
	flag.Parse()

	setupLogger(*logFormat, *logLevel)

	// Validation: src is required
	if *srcFlag == "" {
		flag.Usage()
		logger.Error("Error: -src argument is required")
		os.Exit(1)
	}

	umask := setupUmask(*umaskFlag)
	absSrc, absDst := resolvePaths(*srcFlag, *dstFlag)

	return Config{
		Watch:        *watchFlag,
		Src:          absSrc,
		Dst:          absDst,
		BinDirs:      binDirs,
		Everyone:     *everyoneFlag,
		ProcessUmask: umask,
	}
}

// resolvePaths determines absolute paths for source and destination.
func resolvePaths(src, dst string) (string, string) {
	absSrc, err := filepath.Abs(src)
	if err != nil {
		logger.Error("Error resolving source path", "err", err)
		os.Exit(1)
	}

	var absDst string
	if dst != "" {
		absDst, err = filepath.Abs(dst)
		if err != nil {
			logger.Error("Error resolving destination path", "err", err)
			os.Exit(1)
		}
	} else {
		absDst = getDefaultDest()
	}

	// Safety check: prevent operations where source and destination are the same
	if absSrc == absDst {
		logger.Error("Error: source and destination directories are the same. Operation canceled.", "path", absSrc)
		os.Exit(1)
	}
	return absSrc, absDst
}

// validateSource checks if the source directory exists and is valid.
func validateSource(src string) error {
	logger.Debug("Validating source directory", "path", src)
	info, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("accessing source directory %s: %v", src, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("source path %s is not a directory", src)
	}
	return nil
}

// getDefaultDest returns the default destination based on the current user.
func getDefaultDest() string {
	currentUser, err := user.Current()
	if err != nil {
		logger.Error("Error getting current user info", "err", err)
		os.Exit(1)
	}
	path := currentUser.HomeDir
	if currentUser.Uid == "0" {
		path = "/"
	}
	// Ensure absolute path even for defaults
	abs, err := filepath.Abs(path)
	if err != nil {
		logger.Error("Error resolving default destination", "err", err)
		os.Exit(1)
	}
	return abs
}

// runLoop executes the main synchronization loop.
func runLoop(cfg Config, stateFilePath string) {
	// Cache stores metadata to detect changes in watch mode.
	metaCache := make(map[string]fileMeta)

	// State cache variables to avoid re-parsing the state file if it hasn't changed.
	// These persist across loop iterations.
	var (
		cachedState     map[string]struct{}
		cachedStateMeta fileMeta
	)

	// Iteration counter for periodic full scans.
	var iterationCount int

	for {
		success := syncIteration(cfg, stateFilePath, &cachedState, &cachedStateMeta, metaCache)

		if !cfg.Watch {
			if !success {
				os.Exit(1) // Standard practice: exit with error code in one-shot mode
			}
			break
		}

		time.Sleep(watchRetryInterval)

		// Increment counter and check if we should drop the cache.
		iterationCount++
		if iterationCount >= fullScanIterations {
			// Dropping the cache forces the syncer to bypass the "source unchanged" optimization
			// and strictly compare source vs destination metadata (mtime, size, perms).
			// This detects and reverts external modifications to destination files.
			logger.Debug("Clearing metadata cache for periodic full scan")
			metaCache = make(map[string]fileMeta)
			iterationCount = 0
		}
	}
}

// syncIteration performs a single pass of synchronization.
// Returns true if successful (or recoverable), false if a fatal error occurred.
func syncIteration(cfg Config, stateFilePath string, cachedState *map[string]struct{}, cachedStateMeta *fileMeta, metaCache map[string]fileMeta) bool {
	logger.Debug("Starting sync iteration")

	// Open the state file with read/write permissions.
	// We hold the file handle and lock throughout the entire sync process to prevent race conditions.
	// If the source directory is transiently unavailable (e.g. network mount), this will fail.
	stateFile, err := openAndLockState(stateFilePath)
	if err != nil {
		logger.Error("Error accessing state file", "err", err)
		return false
	}
	defer stateFile.Close() // Releases lock

	// Ensure correct ownership if running as root
	ensureStateOwnership(stateFile, stateFilePath)

	// Load previous state (handling cache hits)
	currentState, err := loadStateWithCache(stateFile, cachedState, cachedStateMeta)
	if err != nil {
		// If load fails (e.g. corruption), we assume empty state for THIS run.
		// We log a warning so the user knows why pruning might be behaving as if the state is empty.
		logger.Warn("Failed to parse state file, assuming empty state", "err", err)
	}

	// Ensure executable bits are set in specified bin directories before syncing
	ensureExecBits(cfg.Src, cfg.BinDirs, cfg.ProcessUmask)

	// Perform Sync
	s := newSyncer(cfg, currentState, metaCache)
	if err := s.run(); err != nil {
		logger.Error("Sync error", "err", err)
		return false
	}

	// Save State only if changes occurred.
	// We do NOT update the cache here. If we wrote to the file, its mtime/size on disk has changed.
	// On the next iteration, the check at the top of the loop will fail (mismatch), causing a fresh read.
	if s.changed {
		if err := saveState(stateFile, s.newState); err != nil {
			logger.Error("Error saving state", "err", err)
		}
	}
	return true
}

// openAndLockState opens the state file and acquires an exclusive lock.
func openAndLockState(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0666)
	if err != nil {
		return nil, err
	}
	// Acquire an exclusive lock immediately. This blocks until the lock is obtained.
	if err := lockFile(f.Fd(), true); err != nil {
		f.Close()
		return nil, fmt.Errorf("locking state file: %v", err)
	}
	return f, nil
}

// loadStateWithCache loads the state, using cached values if the file hasn't changed.
func loadStateWithCache(f *os.File, cachedState *map[string]struct{}, cachedMeta *fileMeta) (map[string]struct{}, error) {
	info, statErr := f.Stat()
	if statErr != nil {
		*cachedState = nil
		return make(map[string]struct{}), statErr
	}

	// We check `cachedState != nil` to ensure we don't use an empty cache on the very first run.
	if *cachedState != nil &&
		info.ModTime().Equal(cachedMeta.ModTime) &&
		info.Size() == cachedMeta.Size {
		return *cachedState, nil
	}

	// Cache miss, first run, or file changed: Read from the beginning
	if _, err := f.Seek(0, 0); err != nil {
		return nil, fmt.Errorf("seeking state file: %v", err)
	}

	state, err := loadState(f)
	if err == nil {
		// Update cache
		*cachedState = state
		*cachedMeta = fileMeta{ModTime: info.ModTime(), Size: info.Size()}
	} else {
		// If Load failed, we can't reliably cache this result.
		*cachedState = nil
		state = make(map[string]struct{}) // Return empty state on failure so logic proceeds
	}

	return state, err
}

// syncer holds the context for a synchronization operation.
type syncer struct {
	cfg            Config
	oldState       map[string]struct{}
	metaCache      map[string]fileMeta
	newState       map[string]struct{}
	processedFiles map[string]bool
	changed        bool
}

func newSyncer(cfg Config, oldState map[string]struct{}, metaCache map[string]fileMeta) *syncer {
	return &syncer{
		cfg:            cfg,
		oldState:       oldState,
		metaCache:      metaCache,
		newState:       make(map[string]struct{}),
		processedFiles: make(map[string]bool),
	}
}

// run executes the sync logic: walk source, then prune orphans.
func (s *syncer) run() error {
	if err := filepath.Walk(s.cfg.Src, s.visit); err != nil {
		return err
	}
	s.prune()
	return nil
}

// visit is the filepath.Walk callback.
func (s *syncer) visit(path string, info os.FileInfo, err error) error {
	if err != nil {
		return err
	}

	relPath, err := filepath.Rel(s.cfg.Src, path)
	if err != nil {
		return err
	}

	if shouldSkip(relPath, info) {
		if info.IsDir() {
			return filepath.SkipDir
		}
		return nil
	}

	// Resolve Symlinks
	// filepath.Walk uses Lstat (gets link info). We must use Stat (follow link)
	// to get the actual file info for correct mtime comparison and permission copying.
	realInfo, err := os.Stat(path)
	if err != nil {
		logger.Warn("Skipping unreadable file or broken link", "path", relPath, "err", err)
		// Mark processed to prevent pruning on read error
		s.processedFiles[relPath] = true
		return nil
	}

	if realInfo.IsDir() {
		return s.handleDirectory(relPath, realInfo)
	}

	return s.handleFile(path, relPath, realInfo)
}

// shouldSkip checks for .git, .etcdotica, or root dir.
func shouldSkip(relPath string, info os.FileInfo) bool {
	if relPath == "." {
		return true
	}
	if relPath == ".etcdotica" {
		return true
	}
	if info.IsDir() && info.Name() == ".git" {
		return true
	}
	return false
}

// handleDirectory creates the directory at the destination.
func (s *syncer) handleDirectory(relPath string, info os.FileInfo) error {
	targetPath := filepath.Join(s.cfg.Dst, relPath)
	expectedPerms := calculatePerms(info.Mode(), s.cfg.ProcessUmask, s.cfg.Everyone)

	// MkdirAll will create the directory and any necessary parents.
	// Note that we do not prune directories or modify permissions on existing ones.
	if err := os.MkdirAll(targetPath, expectedPerms); err != nil {
		logger.Warn("Skipping source directory: failed to create", "path", targetPath, "err", err)
		return filepath.SkipDir
	}
	return nil
}

// handleFile delegates to section handling or regular file handling.
func (s *syncer) handleFile(srcPath, relPath string, info os.FileInfo) error {
	// Check for section file
	if match := sectionFileRx.FindStringSubmatch(relPath); match != nil {
		return s.processSection(srcPath, relPath, match[1], match[2], info)
	}
	return s.processRegularFile(srcPath, relPath, info)
}

// processSection handles merging section files.
func (s *syncer) processSection(srcPath, relPath, targetRel, sectionName string, info os.FileInfo) error {
	targetAbsPath := filepath.Join(s.cfg.Dst, targetRel)

	// We treat the section source file as "processed" so it is not pruned,
	// but we do NOT copy it as a file to the destination.
	s.newState[relPath] = struct{}{}
	s.processedFiles[relPath] = true

	// Watch optimization: skip if source hasn't changed
	if s.checkCache(srcPath, info) {
		return nil
	}

	logger.Debug("Processing section", "name", sectionName, "target", targetAbsPath)
	didChange, err := mergeSection(srcPath, targetAbsPath, sectionName, info, s.cfg.ProcessUmask, s.cfg.Everyone)
	if err != nil {
		logger.Error("Failed to merge section", "section", sectionName, "target", targetAbsPath, "err", err)
		// On error, invalidate cache so we retry this file on the next watch cycle
		delete(s.metaCache, srcPath)
	} else if didChange {
		logger.Debug("Section merged and content changed", "target", targetAbsPath)
		s.changed = true
	}
	return nil
}

// processRegularFile handles copying or updating standard files.
func (s *syncer) processRegularFile(srcPath, relPath string, info os.FileInfo) error {
	targetPath := filepath.Join(s.cfg.Dst, relPath)

	// Watch optimization for standard files: skip processing if the source metadata
	// matches our cache and the file was already successfully recorded in the state.
	if s.checkCache(srcPath, info) {
		if _, ok := s.oldState[relPath]; ok {
			s.newState[relPath] = struct{}{}
			s.processedFiles[relPath] = true
			return nil
		}
	}

	s.processedFiles[relPath] = true
	s.newState[relPath] = struct{}{}

	expectedPerms := calculatePerms(info.Mode(), s.cfg.ProcessUmask, s.cfg.Everyone)

	// If destination file differs, perform a full reinstall/update.
	// This is safer than separate checks (like a standalone chmod) as it mitigates TOCTOU.
	shouldUpdate, err := s.needsUpdate(targetPath, info, expectedPerms)
	if err != nil {
		logger.Error("Error checking destination state", "path", targetPath, "err", err)
		// On error, invalidate cache so we retry this file on the next watch cycle
		delete(s.metaCache, srcPath)
		return nil
	}

	if shouldUpdate {
		if err := installFile(srcPath, targetPath, info, expectedPerms); err != nil {
			logger.Error("Failed to update/install", "path", targetPath, "err", err)
			// On error, invalidate cache so we retry this file on the next watch cycle
			delete(s.metaCache, srcPath)
		} else {
			s.changed = true
		}
	}

	return nil
}

// checkCache returns true if the file hasn't changed since last scan (Watch mode).
func (s *syncer) checkCache(path string, info os.FileInfo) bool {
	if !s.cfg.Watch {
		return false
	}
	currentMeta := fileMeta{ModTime: info.ModTime(), Size: info.Size(), Mode: info.Mode()}
	lastMeta, known := s.metaCache[path]
	s.metaCache[path] = currentMeta

	return known &&
		lastMeta.ModTime.Equal(currentMeta.ModTime) &&
		lastMeta.Size == currentMeta.Size &&
		lastMeta.Mode == currentMeta.Mode
}

// needsUpdate checks if the destination file needs to be replaced.
// It returns true if an update is required, or false if the destination is up to date.
// It returns an error if the destination state cannot be determined or resolved (e.g. symlink removal failure).
func (s *syncer) needsUpdate(dstPath string, srcInfo os.FileInfo, expectedPerms os.FileMode) (bool, error) {
	// Use Lstat to check destination state so we can detect symlinks
	dstInfo, err := os.Lstat(dstPath)
	if err != nil {
		if os.IsNotExist(err) {
			return true, nil // Destination does not exist, install needed
		}
		return false, err // Error accessing destination
	}

	// If destination is a symlink, we must remove it.
	// - If it links to a file: writing would overwrite the target (bad).
	// - If it links to a dir: we want to replace it with the source file.
	if dstInfo.Mode()&os.ModeSymlink != 0 {
		if err := os.Remove(dstPath); err != nil {
			return false, fmt.Errorf("removing destination symlink: %v", err)
		}
		// We treated the symlink as an invalid state. Proceed to update.
		return true, nil
	}

	// Conflict Check: Dest exists and is a directory
	if dstInfo.IsDir() {
		return false, fmt.Errorf("conflict: src is file, dst is dir")
	}

	// Check Size, Mtime, Permissions
	return srcInfo.Size() != dstInfo.Size() ||
		!srcInfo.ModTime().Equal(dstInfo.ModTime()) ||
		dstInfo.Mode().Perm() != expectedPerms, nil
}

// prune removes files or sections that are no longer in the source.
func (s *syncer) prune() {
	for oldRelPath := range s.oldState {
		if s.processedFiles[oldRelPath] {
			continue
		}

		// Check if it's a section file
		if match := sectionFileRx.FindStringSubmatch(oldRelPath); match != nil {
			targetPath := filepath.Join(s.cfg.Dst, match[1])
			logger.Debug("Removing orphaned section", "section", match[2], "target", targetPath)
			if chg, err := removeSection(targetPath, match[2]); err != nil {
				logger.Error("Failed to remove section", "section", match[2], "target", targetPath, "err", err)
			} else if chg {
				s.changed = true
			}
			continue
		}

		// Regular file
		targetPath := filepath.Join(s.cfg.Dst, oldRelPath)
		// Remove orphaned file. Do not remove directories.
		logger.Debug("Removing orphaned file", "file", targetPath)
		if err := os.Remove(targetPath); err == nil {
			s.changed = true
		} else if !os.IsNotExist(err) {
			logger.Error("Failed to remove orphaned file", "file", targetPath, "err", err)
		}
	}
}

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

// chunk represents a part of the file, either raw text or a named section.
type chunk struct {
	isSection bool
	name      string // empty if raw text
	lines     []string
}

// mergeSection reads the source section file and merges it into the target file.
// It respects the alphabetical ordering of sections and safety checks for broken tags.
func mergeSection(srcPath, dstPath, sectionName string, srcInfo os.FileInfo, umask os.FileMode, everyone bool) (bool, error) {
	srcLines, err := readLines(srcPath)
	if err != nil {
		return false, err
	}

	// Check for directory conflict at destination.
	if info, err := os.Stat(dstPath); err == nil && info.IsDir() {
		return false, fmt.Errorf("conflict: target %s is a directory", dstPath)
	}

	// Determine Expected Permissions
	// We strictly enforce permissions based on the source, overwriting any existing destination permissions.
	expectedPerms := calculatePerms(srcInfo.Mode(), umask, everyone)

	// Open Destination File (Read/Write, Create if missing)
	f, err := os.OpenFile(dstPath, os.O_RDWR|os.O_CREATE, expectedPerms)
	if err != nil {
		return false, err
	}
	defer f.Close()

	if err := lockFile(f.Fd(), true); err != nil {
		return false, err
	}

	content, err := io.ReadAll(f)
	if err != nil {
		return false, err
	}

	newBytes, changed, err := computeMergedContent(content, srcLines, sectionName)
	if err != nil {
		return false, err
	}

	if changed {
		if err := writeContent(f, newBytes); err != nil {
			return false, err
		}
	}

	// Enforce permissions.
	// We do this regardless of content change to ensure the file complies with the desired mode.
	// Changing permissions does not trigger the changed indicator.
	if err := f.Chmod(expectedPerms); err != nil {
		logger.Warn("Failed to chmod", "path", dstPath, "err", err)
	}

	return changed, nil
}

// readLines reads a file and splits it into lines.
func readLines(path string) ([]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return splitLines(b), nil
}

// computeMergedContent parses existing content and merges the new section.
func computeMergedContent(oldContent []byte, srcLines []string, sectionName string) ([]byte, bool, error) {
	oldLines := splitLines(oldContent)

	blocks, err := parseBlocks(oldLines, sectionName)
	if err != nil {
		return nil, false, err
	}

	newChunk := chunk{
		isSection: true,
		name:      sectionName,
		lines:     wrapSection(srcLines, sectionName),
	}

	newBlocks := mergeBlocks(blocks, newChunk, sectionName)
	newBytes := serializeBlocks(newBlocks)

	return newBytes, !bytes.Equal(oldContent, newBytes), nil
}

func wrapSection(lines []string, name string) []string {
	res := make([]string, 0, len(lines)+2)
	res = append(res, fmt.Sprintf("# BEGIN %s", name))
	res = append(res, lines...)
	res = append(res, fmt.Sprintf("# END %s", name))
	return res
}

// mergeBlocks inserts the new chunk into the correct position.
func mergeBlocks(blocks []chunk, newChunk chunk, sectionName string) []chunk {
	var out []chunk
	inserted := false

	// Strategy:
	// Iterate through existing blocks.
	// If we find our section -> Replace it.
	// If we find a section strictly GREATER than ours -> Insert before it.
	// If raw -> Keep.

	for _, b := range blocks {
		if inserted {
			// Skip old version of the section if we encounter it later
			if b.isSection && b.name == sectionName {
				continue
			}
			out = append(out, b)
			continue
		}

		if b.isSection {
			if b.name == sectionName {
				out = append(out, newChunk) // Replace
				inserted = true
			} else if sectionName < b.name {
				// Found a section that comes alphabetically AFTER ours.
				// We must insert ours BEFORE this one.
				out = append(out, newChunk)
				out = append(out, b)
				inserted = true
			} else {
				// Current section is smaller (before) ours. Keep looking.
				out = append(out, b)
			}
		} else {
			// Raw text block
			out = append(out, b)
		}
	}
	if !inserted {
		// If we reached the end without inserting, append to the end
		out = append(out, newChunk)
	}
	return out
}

// serializeBlocks joins chunks back into bytes.
func serializeBlocks(blocks []chunk) []byte {
	var buf bytes.Buffer
	for _, b := range blocks {
		for _, line := range b.lines {
			buf.WriteString(line)
			buf.WriteByte('\n')
		}
	}
	return buf.Bytes()
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

// removeSection removes the named section from the target file.
func removeSection(dstPath, sectionName string) (bool, error) {
	f, err := os.OpenFile(dstPath, os.O_RDWR, 0666)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	defer f.Close()

	if err := lockFile(f.Fd(), true); err != nil {
		return false, err
	}

	content, err := io.ReadAll(f)
	if err != nil {
		return false, err
	}

	oldLines := splitLines(content)

	blocks, err := parseBlocks(oldLines, sectionName)
	if err != nil {
		return false, fmt.Errorf("parsing target file: %v", err)
	}

	// Filter out the section
	var newBlocks []chunk
	found := false
	for _, b := range blocks {
		if b.isSection && b.name == sectionName {
			found = true
			continue
		}
		newBlocks = append(newBlocks, b)
	}

	if !found {
		return false, nil
	}

	return true, writeContent(f, serializeBlocks(newBlocks))
}

// parseBlocks reads lines and groups them into chunks (Raw vs Named Sections).
// It validates that if the specific targetSectionName is present, it is well-formed.
// Other malformed sections are treated as raw text to avoid destruction.
func parseBlocks(lines []string, targetSectionName string) ([]chunk, error) {
	var blocks []chunk
	validSections, err := findValidSections(lines, targetSectionName)
	if err != nil {
		return nil, err
	}

	// Build blocks based on valid sections
	lineIdx := 0
	for _, sec := range validSections {
		// Add raw text before this section
		if sec.start > lineIdx {
			blocks = append(blocks, chunk{isSection: false, lines: lines[lineIdx:sec.start]})
		}
		// Add the section
		blocks = append(blocks, chunk{isSection: true, name: sec.name, lines: lines[sec.start : sec.end+1]})
		lineIdx = sec.end + 1
	}

	// Add remaining raw text
	if lineIdx < len(lines) {
		blocks = append(blocks, chunk{isSection: false, lines: lines[lineIdx:]})
	}
	return blocks, nil
}

type span struct {
	start, end int
	name       string
}

// findValidSections scans lines for valid BEGIN/END pairs.
// CRITICAL: It returns an error if the target section has malformed tags (orphaned begin or end).
// This prevents us from corrupting a file where the user might have manually edited the section tags.
func findValidSections(lines []string, targetName string) ([]span, error) {
	var sections []span

	for i := 0; i < len(lines); i++ {
		match := beginSectionRx.FindStringSubmatch(lines[i])
		if match == nil {
			// Check for orphaned END tags of target
			if endMatch := endSectionRx.FindStringSubmatch(lines[i]); endMatch != nil && endMatch[1] == targetName {
				return nil, fmt.Errorf("found orphaned closing tag for section '%s' at line %d", targetName, i+1)
			}
			continue
		}

		name := match[1]
		endIdx := findEndTag(lines, i+1, name)

		if endIdx != -1 {
			sections = append(sections, span{i, endIdx, name})
			i = endIdx // Advance outer loop
		} else {
			// Opening tag without closing tag
			if name == targetName {
				return nil, fmt.Errorf("found opening tag for section '%s' at line %d but no closing tag", name, i+1)
			}
			// Treat other malformed sections as raw text (safe fallback)
		}
	}
	return sections, nil
}

// findEndTag looks ahead for the matching END tag.
// It stops if it finds a nested BEGIN tag for the same name (which is considered broken/raw).
func findEndTag(lines []string, startIdx int, name string) int {
	for j := startIdx; j < len(lines); j++ {
		endMatch := endSectionRx.FindStringSubmatch(lines[j])
		if endMatch != nil && endMatch[1] == name {
			return j
		}
		// Nested/Duplicate begin check
		if beginMatch := beginSectionRx.FindStringSubmatch(lines[j]); beginMatch != nil && beginMatch[1] == name {
			break
		}
	}
	return -1
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

// loadState reads the state from the provided reader.
// It expects the caller to handle file opening and locking.
func loadState(r io.Reader) (map[string]struct{}, error) {
	state := make(map[string]struct{})
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			state[line] = struct{}{}
		}
	}
	return state, scanner.Err()
}

// saveState writes the relative source paths to the locked state file.
// It truncates the file before writing and ensures content is synced.
func saveState(f *os.File, state map[string]struct{}) error {
	if err := f.Truncate(0); err != nil {
		return err
	}
	if _, err := f.Seek(0, 0); err != nil {
		return err
	}

	keys := make([]string, 0, len(state))
	for k := range state {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, srcPath := range keys {
		if _, err := fmt.Fprintf(f, "%s\n", srcPath); err != nil {
			return err
		}
	}
	// Flush writes to stable storage
	return f.Sync()
}
