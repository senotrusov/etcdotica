// Copyright 2025-2026 Stanislav Senotrusov
//
// This work is dual-licensed under the Apache License, Version 2.0 and the MIT License.
// See LICENSE-APACHE and LICENSE-MIT in the top-level directory for details.
//
// SPDX-License-Identifier: Apache-2.0 OR MIT

//go:build !windows

package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

// HandleLifecycle listens for platform-specific termination signals and
// cancels the context to trigger a graceful shutdown.
func HandleLifecycle(cancel context.CancelFunc) {
	sigChan := make(chan os.Signal, 1)

	// SIGINT: Ctrl+C
	// SIGTERM: Standard termination signal (e.g. kill command)
	// SIGHUP: Terminal closed
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)

	// Block until the first signal is received.
	sig := <-sigChan
	logger.Info("Received termination signal", "signal", sig)

	// Signal the main loop to exit gracefully.
	cancel()

	// NOTE: We intentionally do NOT call signal.Stop(sigChan) here.
	// We want to keep the channel registered so that if the user mashes Ctrl+C
	// rapidly, subsequent signals are trapped by this channel (and dropped if full)
	// rather than triggering the default OS behavior (Hard Kill).
	// This ensures the application *only* exits when the current sync step finishes.
}
