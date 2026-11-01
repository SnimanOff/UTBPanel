//go:build windows

package main

import (
	"path/filepath"
	"runtime"
	"sync"

	webview2 "github.com/jchv/go-webview2"
	"golang.org/x/sys/windows"

	"github.com/SnimanOff/UTBPanel/backend/internal/gate"
)

var setForeground = windows.NewLazySystemDLL("user32.dll").NewProc("SetForegroundWindow")

// mini is the small window (/mini) opened by a click on the tray icon: one
// at a time, with its own WebView2 profile next to the gate state.
type mini struct {
	mu sync.Mutex
	w  webview2.WebView
}

func (m *mini) open(g *gate.Gate) {
	m.mu.Lock()
	if m.w != nil {
		w := m.w
		m.mu.Unlock()
		w.Dispatch(func() { setForeground.Call(uintptr(w.Window())) })
		return
	}
	m.mu.Unlock()
	go func() {
		// WebView2 needs its own thread with a message loop.
		runtime.LockOSThread()
		w := webview2.NewWithOptions(webview2.WebViewOptions{
			DataPath:      filepath.Join(g.Dir(), "webview"),
			AutoFocus:     true,
			WindowOptions: webview2.WindowOptions{Title: "TG Panel", Width: 480, Height: 640, Center: true},
		})
		if w == nil {
			// No WebView2 runtime: the same page in the browser.
			g.OpenPage("/mini")
			return
		}
		m.mu.Lock()
		m.w = w
		m.mu.Unlock()
		w.Navigate(g.LoginURL() + "&next=/mini")
		w.Run()
		m.mu.Lock()
		m.w = nil
		m.mu.Unlock()
		w.Destroy()
	}()
}

func (m *mini) close() {
	m.mu.Lock()
	w := m.w
	m.mu.Unlock()
	if w != nil {
		w.Terminate()
	}
}
