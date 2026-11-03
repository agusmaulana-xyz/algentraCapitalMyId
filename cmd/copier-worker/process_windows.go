//go:build windows

package main

import (
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// NewCallback allocates a process-lifetime Windows callback trampoline. Do not
// call it from the periodic chart monitor: the Windows callback table is
// finite, and MT5 chart enumeration runs every few seconds for each account.
var (
	windowEnumMu       sync.Mutex
	activeWindowEnum   atomic.Pointer[windowEnumHandler]
	windowEnumCallback = syscall.NewCallback(dispatchWindowEnum)
)

type windowEnumHandler struct {
	callback func(windows.HWND) uintptr
}

func dispatchWindowEnum(hwnd uintptr, _ uintptr) uintptr {
	handler := activeWindowEnum.Load()
	if handler == nil {
		return 1
	}
	return handler.callback(windows.HWND(hwnd))
}

func enumWindows(callback func(windows.HWND) uintptr) {
	windowEnumMu.Lock()
	defer windowEnumMu.Unlock()
	activeWindowEnum.Store(&windowEnumHandler{callback: callback})
	defer activeWindowEnum.Store(nil)
	_ = windows.EnumWindows(windowEnumCallback, nil)
}

func enumChildWindows(parent windows.HWND, callback func(windows.HWND) uintptr) {
	windowEnumMu.Lock()
	defer windowEnumMu.Unlock()
	activeWindowEnum.Store(&windowEnumHandler{callback: callback})
	defer activeWindowEnum.Store(nil)
	windows.EnumChildWindows(parent, windowEnumCallback, nil)
}

func manageMT5Charts(processID uint32, stopped <-chan struct{}, enforceSingle bool) {
	// MT5 creates its chart window asynchronously after the process starts.
	select {
	case <-stopped:
		return
	case <-time.After(4 * time.Second):
	}
	maximizeMT5Charts(processID)
	if !enforceSingle {
		return
	}
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-stopped:
			return
		case <-ticker.C:
			enforceSingleMT5Chart(processID)
		}
	}
}

func maximizeMT5Charts(processID uint32) {
	user32 := windows.NewLazySystemDLL("user32.dll")
	showWindow := user32.NewProc("ShowWindowAsync")
	getWindowRect := user32.NewProc("GetWindowRect")
	getWindowTextLength := user32.NewProc("GetWindowTextLengthW")
	getWindowText := user32.NewProc("GetWindowTextW")
	maximize := uintptr(3) // SW_MAXIMIZE
	var mainWindow windows.HWND
	var largestArea int64
	enumWindows(func(hwnd windows.HWND) uintptr {
		var owner uint32
		windows.GetWindowThreadProcessId(hwnd, &owner)
		if owner == processID && windows.IsWindowVisible(hwnd) {
			var bounds struct{ Left, Top, Right, Bottom int32 }
			if result, _, _ := getWindowRect.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&bounds))); result != 0 {
				area := int64(bounds.Right-bounds.Left) * int64(bounds.Bottom-bounds.Top)
				if area > largestArea {
					mainWindow = hwnd
					largestArea = area
				}
			}
		}
		return 1
	})
	if mainWindow == 0 {
		return
	}
	showWindow.Call(uintptr(mainWindow), maximize)
	enumChildWindows(mainWindow, func(window windows.HWND) uintptr {
		if !windows.IsWindowVisible(window) {
			return 1
		}
		length, _, _ := getWindowTextLength.Call(uintptr(window))
		if length == 0 || length > 256 {
			return 1
		}
		buffer := make([]uint16, length+1)
		getWindowText.Call(uintptr(window), uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)))
		if isMT5ChartWindowTitle(windows.UTF16ToString(buffer)) {
			showWindow.Call(uintptr(window), maximize)
		}
		return 1
	})
}

// requestMT5Close asks the terminal's main window to close normally (WM_CLOSE)
// so MT5 saves its state, including the Market Watch symbol list.
func requestMT5Close(processID uint32) {
	user32 := windows.NewLazySystemDLL("user32.dll")
	getWindowRect := user32.NewProc("GetWindowRect")
	postMessage := user32.NewProc("PostMessageW")
	var mainWindow windows.HWND
	var largestArea int64
	enumWindows(func(hwnd windows.HWND) uintptr {
		var owner uint32
		windows.GetWindowThreadProcessId(hwnd, &owner)
		if owner == processID && windows.IsWindowVisible(hwnd) {
			var bounds struct{ Left, Top, Right, Bottom int32 }
			if result, _, _ := getWindowRect.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&bounds))); result != 0 {
				area := int64(bounds.Right-bounds.Left) * int64(bounds.Bottom-bounds.Top)
				if area > largestArea {
					mainWindow, largestArea = hwnd, area
				}
			}
		}
		return 1
	})
	if mainWindow != 0 {
		postMessage.Call(uintptr(mainWindow), 0x0010, 0, 0) // WM_CLOSE
	}
}

func enforceSingleMT5Chart(processID uint32) {
	user32 := windows.NewLazySystemDLL("user32.dll")
	getWindowRect := user32.NewProc("GetWindowRect")
	postMessage := user32.NewProc("PostMessageW")
	var mainWindow windows.HWND
	var largestArea int64
	enumWindows(func(hwnd windows.HWND) uintptr {
		var owner uint32
		windows.GetWindowThreadProcessId(hwnd, &owner)
		if owner == processID && windows.IsWindowVisible(hwnd) {
			var bounds struct{ Left, Top, Right, Bottom int32 }
			if result, _, _ := getWindowRect.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&bounds))); result != 0 {
				area := int64(bounds.Right-bounds.Left) * int64(bounds.Bottom-bounds.Top)
				if area > largestArea {
					mainWindow = hwnd
					largestArea = area
				}
			}
		}
		return 1
	})
	if mainWindow == 0 {
		return
	}
	kept := false
	enumChildWindows(mainWindow, func(hwnd windows.HWND) uintptr {
		if !windows.IsWindowVisible(hwnd) || !isMT5ChartWindowTitle(windowText(uintptr(hwnd), user32)) {
			return 1
		}
		if !kept {
			kept = true
			return 1
		}
		postMessage.Call(uintptr(hwnd), 0x0010, 0, 0) // WM_CLOSE
		return 1
	})
}

func windowText(hwnd uintptr, user32 *windows.LazyDLL) string {
	length, _, _ := user32.NewProc("GetWindowTextLengthW").Call(hwnd)
	if length == 0 || length > 256 {
		return ""
	}
	buffer := make([]uint16, length+1)
	user32.NewProc("GetWindowTextW").Call(hwnd, uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)))
	return windows.UTF16ToString(buffer)
}

func isMT5ChartWindowTitle(title string) bool {
	title = strings.ToUpper(strings.TrimSpace(title))
	for _, period := range []string{",M1", ",M2", ",M3", ",M4", ",M5", ",M6", ",M10", ",M12", ",M15", ",M20", ",M30", ",H1", ",H2", ",H3", ",H4", ",H6", ",H8", ",H12", ",D1", ",W1", ",MN1"} {
		if strings.HasSuffix(title, period) {
			return true
		}
	}
	return false
}

func prepareChild(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x00000200} // CREATE_NEW_PROCESS_GROUP
}

func stopChildProcess(cmd *exec.Cmd, done <-chan error, timeout time.Duration) error {
	select {
	case err := <-done:
		return err
	default:
	}
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	// CTRL_BREAK_EVENT gives the terminal a graceful stop before the supervisor
	// falls back to TerminateProcess.
	if err := windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, uint32(cmd.Process.Pid)); err != nil {
		_ = cmd.Process.Kill()
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err := <-done:
		return err
	case <-timer.C:
		_ = cmd.Process.Kill()
		select {
		case err := <-done:
			return err
		case <-time.After(5 * time.Second):
			return os.ErrDeadlineExceeded
		}
	}
}
