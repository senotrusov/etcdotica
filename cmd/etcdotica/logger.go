// Copyright 2025-2026 Stanislav Senotrusov
//
// This work is dual-licensed under the Apache License, Version 2.0 and the MIT License.
// See LICENSE-APACHE and LICENSE-MIT in the top-level directory for details.
//
// SPDX-License-Identifier: Apache-2.0 OR MIT

package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// setupLogger configures the global structured logger. It supports JSON and
// explicit text (key=value) handlers, and defaults to a "human" format with
// configurable log levels.
func setupLogger(format, levelStr string) {
	level := getSlogLevel(levelStr)
	var handler slog.Handler

	switch strings.ToLower(format) {
	case "json":
		handler = slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level})
	case "text":
		handler = slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})
	default:
		handler = &humanHandler{level: level}
	}

	logger = slog.New(handler)
	slog.SetDefault(logger)
}

// If you need to change log levels at runtime, use Go's built-in slog.LevelVar:
//
// type humanHandler struct {
//     level slog.Leveler // Use Leveler interface instead of Level
// }
//
// func (h *humanHandler) Enabled(_ context.Context, l slog.Level) bool {
//     return l >= h.level.Level() // .Level() is thread-safe on a LevelVar
// }

// humanHandler implements slog.Handler to provide the standard Go log format
// without keys for basic fields, supporting custom log levels.
type humanHandler struct {
	level slog.Level
}

func (h *humanHandler) Enabled(_ context.Context, l slog.Level) bool {
	return l >= h.level
}

// Add this global variable to manage buffer reuse
var loggerPool = sync.Pool{
	New: func() any {
		// Pre-allocate 1KB to minimize resizing for average logs
		b := make([]byte, 0, 1024)
		return &b
	},
}

func (h *humanHandler) Handle(_ context.Context, r slog.Record) error {
	// Get a buffer from the pool
	bp := loggerPool.Get().(*[]byte)
	defer loggerPool.Put(bp)

	// Reset length to 0, keep capacity
	b := (*bp)[:0]

	// Append Level
	b = append(b, r.Level.String()...)
	b = append(b, ' ')

	// Append Message
	b = append(b, r.Message...)

	// Iterate attributes
	r.Attrs(func(a slog.Attr) bool {
		b = append(b, ' ')
		b = append(b, a.Key...)
		b = append(b, '=')

		// Optimization: Handle types directly without reflection/boxing
		b = appendValue(b, a.Value)
		return true
	})

	b = append(b, '\n')

	// Write to stderr in one syscall (atomic-ish for typical line lengths)
	_, err := os.Stderr.Write(b)

	// Update the pointer in the pool if the slice grew
	*bp = b
	return err
}

// Helper to append values efficiently based on their Kind
func appendValue(b []byte, v slog.Value) []byte {
	v = v.Resolve()
	switch v.Kind() {
	case slog.KindString:
		return append(b, v.String()...)
	case slog.KindInt64:
		return strconv.AppendInt(b, v.Int64(), 10)
	case slog.KindUint64:
		return strconv.AppendUint(b, v.Uint64(), 10)
	case slog.KindFloat64:
		return strconv.AppendFloat(b, v.Float64(), 'f', -1, 64)
	case slog.KindBool:
		return strconv.AppendBool(b, v.Bool())
	case slog.KindDuration:
		return append(b, v.Duration().String()...)
	case slog.KindTime:
		return v.Time().AppendFormat(b, time.RFC3339)
	case slog.KindGroup:
		return append(b, "[Group]"...) // or handle nested groups if needed
	case slog.KindAny, slog.KindLogValuer:
		// Fallback to fmt for complex types (errors, structs)
		return fmt.Append(b, v.Any())
	default:
		return append(b, "UNKNOWN"...)
	}
}

// func (h *humanHandler) Handle(_ context.Context, r slog.Record) error {
// 	var sb strings.Builder
// 	sb.WriteString(r.Level.String())
// 	sb.WriteByte(' ')
// 	sb.WriteString(r.Message)

// 	r.Attrs(func(a slog.Attr) bool {
// 		sb.WriteByte(' ')
// 		sb.WriteString(a.Key)
// 		sb.WriteByte('=')
// 		fmt.Fprint(&sb, a.Value.Any())
// 		return true
// 	})

// 	fmt.Fprintln(os.Stderr, sb.String())
// 	return nil
// }

func (h *humanHandler) WithAttrs(attrs []slog.Attr) slog.Handler { return h }
func (h *humanHandler) WithGroup(name string) slog.Handler       { return h }

func getSlogLevel(level string) slog.Level {
	switch strings.ToLower(level) {
	case "debug":
		return slog.LevelDebug
	case "info":
		return slog.LevelInfo
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
