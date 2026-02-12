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

//go:build windows

package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/sys/windows/svc"
)

// HandleLifecycle detects if the application is running as a Windows Service
// or an interactive console application and handles shutdown signals accordingly.
func HandleLifecycle(cancel context.CancelFunc) {
	isService, err := svc.IsWindowsService()
	if err != nil {
		logger.Warn("Failed to detect Windows Service status; assuming interactive mode", "err", err)
		isService = false
	}

	if isService {
		runService(cancel)
	} else {
		runInteractive(cancel)
	}
}

// runInteractive handles standard console signals.
func runInteractive(cancel context.CancelFunc) {
	sigChan := make(chan os.Signal, 1)

	// os.Interrupt catches Ctrl+C (SIGINT) and Ctrl+Break on Windows.
	// syscall.SIGTERM catches termination requests (including Console Close event).
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	sig := <-sigChan
	logger.Info("Received termination signal", "signal", sig)
	cancel()

	// Channel is kept open to suppress default hard-kill behavior on repeated signals.
}

// runService executes the Windows Service handler in a background goroutine.
func runService(cancel context.CancelFunc) {
	go func() {
		// svc.Run blocks until the service is stopped by the Service Control Manager.
		err := svc.Run("etcdotica", &serviceHandler{cancel: cancel})
		if err != nil {
			logger.Error("Windows Service run failed", "err", err)
			// If the service framework fails, ensure the app shuts down.
			cancel()
		}
	}()
}

// serviceHandler implements svc.Handler
type serviceHandler struct {
	cancel context.CancelFunc
}

// Execute is called by the Service Control Manager.
func (m *serviceHandler) Execute(args []string, r <-chan svc.ChangeRequest, s chan<- svc.Status) (bool, uint32) {
	const cmdsAccepted = svc.AcceptStop | svc.AcceptShutdown

	s <- svc.Status{State: svc.StartPending}
	s <- svc.Status{State: svc.Running, Accepts: cmdsAccepted}

	for change := range r {
		switch change.Cmd {
		case svc.Interrogate:
			s <- change.CurrentStatus
		case svc.Stop, svc.Shutdown:
			logger.Info("Service stop/shutdown requested")

			// Notify SCM we are stopping.
			s <- svc.Status{State: svc.StopPending}

			// Signal the main loop to stop.
			m.cancel()

			// Returning from Execute causes svc.Run to return, effectively stopping the service logic.
			// The main process will exit once runLoop detects the context cancellation.
			return false, 0
		}
	}
	return false, 0
}
