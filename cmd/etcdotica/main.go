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
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
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
