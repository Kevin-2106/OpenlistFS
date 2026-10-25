//go:build windows

// Package singleinstance holds a session-wide lock before Wails starts. A
// separate auto-reset event retains activation requests received during startup.
package singleinstance

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"golang.org/x/sys/windows"
)

type Instance struct {
	mutex     windows.Handle
	event     windows.Handle
	once      sync.Once
	lifecycle sync.Mutex
	closed    bool
	cancel    context.CancelFunc
	done      chan struct{}
}

func Acquire(id string) (*Instance, bool, error) {
	name, err := windows.UTF16PtrFromString(`Local\` + id)
	if err != nil {
		return nil, false, err
	}
	mutex, err := windows.CreateMutex(nil, false, name)
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		defer windows.CloseHandle(mutex)
		return nil, false, activate(id)
	}
	if err != nil {
		return nil, false, err
	}
	eventName, _ := windows.UTF16PtrFromString(`Local\` + id + ".activate")
	event, err := windows.CreateEvent(nil, 0, 0, eventName)
	if err != nil && !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		windows.CloseHandle(mutex)
		return nil, false, err
	}
	return &Instance{mutex: mutex, event: event}, true, nil
}

func activate(id string) error {
	name, _ := windows.UTF16PtrFromString(`Local\` + id + ".activate")
	deadline := time.Now().Add(5 * time.Second)
	for {
		event, err := windows.OpenEvent(windows.EVENT_MODIFY_STATE, false, name)
		if err == nil {
			defer windows.CloseHandle(event)
			// Pass the launching process's foreground permission to the running UI.
			windows.NewLazySystemDLL("user32.dll").NewProc("AllowSetForegroundWindow").Call(0xffffffff)
			return windows.SetEvent(event)
		}
		if !errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || time.Now().After(deadline) {
			return fmt.Errorf("cannot activate the running application: %w", err)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// Start waits until the DOM is ready; reloads cannot create extra listeners.
func (i *Instance) Start(ctx context.Context, show func()) {
	i.lifecycle.Lock()
	defer i.lifecycle.Unlock()
	if i.closed {
		return
	}
	i.once.Do(func() {
		ctx, i.cancel = context.WithCancel(ctx)
		i.done = make(chan struct{})
		go func() { defer close(i.done); i.listen(ctx, show) }()
	})
}

func (i *Instance) listen(ctx context.Context, show func()) {
	for ctx.Err() == nil {
		result, err := windows.WaitForSingleObject(i.event, 200)
		if err != nil {
			return
		}
		if result == windows.WAIT_OBJECT_0 && ctx.Err() == nil {
			show()
		}
	}
}

func (i *Instance) Close() {
	i.lifecycle.Lock()
	defer i.lifecycle.Unlock()
	if i.closed {
		return
	}
	i.closed = true
	if i.cancel != nil {
		i.cancel()
		<-i.done
	}
	windows.CloseHandle(i.event)
	windows.CloseHandle(i.mutex)
}
