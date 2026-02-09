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
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

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
