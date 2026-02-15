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
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"runtime"
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
	Force        bool
	Collect      bool
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
	// Version is set during the build process via -ldflags.
	// It defaults to "development" for local builds.
	Version = "development"

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

	// Create a context to handle graceful shutdown.
	// This context is cancelled when a termination signal is received.
	ctx, cancel := context.WithCancel(context.Background())

	// Start the platform-specific lifecycle handler in a separate goroutine.
	// This listens for OS signals (or Windows Service events) and calls cancel().
	go HandleLifecycle(cancel)

	// Initial validation: Source must exist and be a directory on startup.
	// We only strictly require existence at start. Transient failures later
	// (in watch mode) are handled in the loop.
	if err := validateSource(cfg.Src); err != nil {
		logger.Error("Error validating source", "err", err)
		os.Exit(1)
	}

	stateFilePath := filepath.Join(cfg.Src, ".etcdotica")

	runLoop(ctx, cfg, stateFilePath)
}

// parseFlags handles command line argument parsing and configuration setup.
func parseFlags() Config {
	defaultLogLevel := "info"
	if env := os.Getenv("EDTC_LOG_LEVEL"); env != "" {
		defaultLogLevel = env
	}

	var binDirs stringArray
	flag.Var(&binDirs, "bindir", "Directory relative to the source directory in which all files will\nbe ensured to have the executable bit set (can be repeated).")

	collectFlag := flag.Bool("collect", false, "Collect mode: copy newer files from destination back to source.\nIgnored if '-force' is enabled.")
	dstFlag := flag.String("dst", "", "Destination directory (default: user home directory, or / if root).")
	everyoneFlag := flag.Bool("everyone", false, "Set group and other permissions to the same permission bits as\nthe owner, then apply the umask to the resulting mode.")
	forceFlag := flag.Bool("force", false, "Force overwrite even if destination is newer. Overrides '-collect'.")
	logFormat := flag.String("log-format", "human", "Log format: human, text or json")
	logLevel := flag.String("log-level", defaultLogLevel, "Log level: debug, info, warn, error")
	srcFlag := flag.String("src", "", "Source directory (required).")
	umaskFlag := flag.String("umask", "", "Set process umask (octal, e.g. 077).")
	versionFlag := flag.Bool("version", false, "Print version information and exit.")
	watchFlag := flag.Bool("watch", false, "Watch mode: scan continuously for changes.")

	flag.Parse()

	if *versionFlag {
		fmt.Printf("etcdotica %s (%s)\n", Version, runtime.Version())
		os.Exit(0)
	}

	setupLogger(*logFormat, *logLevel)

	// Validation: src is required
	if *srcFlag == "" {
		flag.Usage()
		logger.Error("Error: -src argument is required")
		os.Exit(1)
	}

	umask := setupUmask(*umaskFlag)
	absSrc, absDst := resolvePaths(*srcFlag, *dstFlag)

	// Consolidate flags with Environment Variables.
	// Force mode takes precedence over Collect mode. If Force is enabled, Collect
	// is explicitly disabled to prevent the tool from attempting to pull and
	// push the same file in a single cycle.
	force := *forceFlag || parseBoolEnv("EDTC_FORCE")
	collect := (*collectFlag || parseBoolEnv("EDTC_COLLECT")) && !force

	if force && (*collectFlag || parseBoolEnv("EDTC_COLLECT")) {
		logger.Warn("Both force and collect modes were enabled; force takes precedence and collect has been disabled.")
	}

	return Config{
		Watch:        *watchFlag,
		Force:        force,
		Collect:      collect,
		Src:          absSrc,
		Dst:          absDst,
		BinDirs:      binDirs,
		Everyone:     *everyoneFlag,
		ProcessUmask: umask,
	}
}

// parseBoolEnv checks an environment variable for "1" or "true".
func parseBoolEnv(key string) bool {
	val := strings.ToLower(os.Getenv(key))
	return val == "1" || val == "true"
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
func runLoop(ctx context.Context, cfg Config, stateFilePath string) {
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
		// hasPartialErrors: Non-fatal errors occurred on specific files/sections (sync continued).
		hasPartialErrors := syncIteration(cfg, stateFilePath, &cachedState, &cachedStateMeta, metaCache)

		if !cfg.Watch {
			if hasPartialErrors {
				// Partial failure: Loop ran, but some files failed to sync.
				logger.Error("Synchronization finished with partial errors")
				os.Exit(2)
			}
			// Success
			os.Exit(0)
		}

		if hasPartialErrors {
			// In watch mode, we log errors as transient and retry.
			logger.Error("Transient error in watch mode; retrying")
		}

		// Wait logic: Sleep for the interval OR wake up immediately on shutdown signal.
		select {
		case <-ctx.Done():
			logger.Info("Shutdown requested during wait. Exiting...")
			return
		case <-time.After(watchRetryInterval):
			// Continue to next iteration
		}

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
// Returns:
//   - partialErrors: True if individual file/section errors occurred during the pass.
func syncIteration(cfg Config, stateFilePath string, cachedState *map[string]struct{}, cachedStateMeta *fileMeta, metaCache map[string]fileMeta) bool {
	logger.Debug("Starting sync iteration")

	// Open the state file with read/write permissions.
	// We hold the file handle and lock throughout the entire sync process to prevent race conditions.
	// If the source directory is transiently unavailable (e.g. network mount), this will fail.
	stateFile, err := openAndLockState(stateFilePath)
	if err != nil {
		logger.Error("Error accessing state file", "err", err)
		return true
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
	hasSyncErrors := s.run()

	// Save State only if changes occurred.
	// We do NOT update the cache here. If we wrote to the file, its mtime/size on disk has changed.
	// On the next iteration, the check at the top of the loop will fail (mismatch), causing a fresh read.
	if s.changed {
		if err := saveState(stateFile, s.newState); err != nil {
			logger.Error("Error saving state", "err", err)
			hasSyncErrors = true // Saving state is a critical part of the sync process
		}
	}

	return hasSyncErrors
}
