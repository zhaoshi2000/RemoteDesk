//go:build windows

// Package winservice provides the SCM lifecycle only. It does not interact with Session 0's desktop.
package winservice

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"syscall"
	"unsafe"
)

type serviceStatus struct{ Type, State, Accepted, Win32Exit, SpecificExit, Checkpoint, WaitHint uint32 }
type entry struct {
	Name *uint16
	Proc uintptr
}

var api = syscall.NewLazyDLL("advapi32.dll")
var startDispatcher = api.NewProc("StartServiceCtrlDispatcherW")
var registerHandler = api.NewProc("RegisterServiceCtrlHandlerExW")
var setStatus = api.NewProc("SetServiceStatus")
var state struct {
	mu     sync.Mutex
	name   *uint16
	handle uintptr
	cancel context.CancelFunc
	run    func(context.Context) error
	err    error
}

func report(status serviceStatus) {
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.handle != 0 {
		setStatus.Call(state.handle, uintptr(unsafe.Pointer(&status)))
	}
}
func handler(control, event uint32, data, ctx uintptr) uintptr {
	switch control {
	case 1, 5:
		report(serviceStatus{Type: 16, State: 3, Checkpoint: 1, WaitHint: 15000})
		state.cancel()
	}
	return 0
}
func mainCallback(argc uint32, argv uintptr) uintptr {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	state.cancel = cancel
	h, _, e := registerHandler.Call(uintptr(unsafe.Pointer(state.name)), syscall.NewCallback(handler), 0)
	if h == 0 {
		state.err = fmt.Errorf("RegisterServiceCtrlHandlerEx: %w", e)
		return 0
	}
	state.mu.Lock()
	state.handle = h
	state.mu.Unlock()
	report(serviceStatus{Type: 16, State: 2, Checkpoint: 1, WaitHint: 15000})
	report(serviceStatus{Type: 16, State: 4, Accepted: 1 | 4})
	state.err = state.run(ctx)
	status := serviceStatus{Type: 16, State: 1}
	if state.err != nil {
		status.Win32Exit = 1066
		status.SpecificExit = 1
	}
	report(status)
	return 0
}
func Run(name string, run func(context.Context) error) error {
	n, e := syscall.UTF16PtrFromString(name)
	if e != nil {
		return e
	}
	state.name = n
	state.run = run
	table := [2]entry{{Name: n, Proc: syscall.NewCallback(mainCallback)}, {}}
	r, _, e := startDispatcher.Call(uintptr(unsafe.Pointer(&table[0])))
	runtime.KeepAlive(table)
	runtime.KeepAlive(n)
	if r == 0 {
		return fmt.Errorf("StartServiceCtrlDispatcher (start through SCM, or use run): %w", e)
	}
	return state.err
}
