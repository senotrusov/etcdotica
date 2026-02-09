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
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

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

			section := match[2]
			chg, err := removeSection(targetPath, section)

			switch {
			case err != nil:
				logger.Error("Failed to remove section", "section", section, "target", targetPath, "err", err)

			case chg:
				logger.Debug("Removed orphaned section", "section", section, "target", targetPath)
				s.changed = true

			default:
				// This handles the case where err is nil but chg is false
				logger.Debug("Orphaned section already gone; state matches desired", "section", section, "target", targetPath)
			}

			continue
		}

		// Regular file
		targetPath := filepath.Join(s.cfg.Dst, oldRelPath)

		err := os.Remove(targetPath)

		switch {
		case err == nil:
			logger.Debug("Removed orphaned file", "file", targetPath)
			s.changed = true

		case errors.Is(err, os.ErrNotExist):
			logger.Debug("Orphaned file already gone; state matches desired", "file", targetPath)
			s.changed = true

		default:
			logger.Error("Failed to remove orphaned file", "file", targetPath, "err", err)
		}

	}
}
