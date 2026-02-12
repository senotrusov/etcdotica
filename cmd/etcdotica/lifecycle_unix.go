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
