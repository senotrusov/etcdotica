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
)

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
