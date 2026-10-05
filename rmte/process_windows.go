//go:build windows

package main

import (
	"syscall"
	"unsafe"
)

// interruptTabProcess stops running child processes of the tab process on Windows.
// When cmd.exe is run with pipes, child commands (such as python, ping, npm, etc.)
// do not receive SIGINT from writing to stdin. Finding and terminating child processes
// allows the tab to return cleanly to the shell prompt on Ctrl+C.
func interruptTabProcess(tab *TabSession) {
	if tab == nil || tab.Cmd == nil || tab.Cmd.Process == nil {
		return
	}
	parentPID := uint32(tab.Cmd.Process.Pid)
	if parentPID == 0 {
		return
	}

	hSnap, err := syscall.CreateToolhelp32Snapshot(syscall.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return
	}
	defer syscall.CloseHandle(hSnap)

	var pe syscall.ProcessEntry32
	pe.Size = uint32(unsafe.Sizeof(pe))

	if err := syscall.Process32First(hSnap, &pe); err != nil {
		return
	}

	var childPIDs []uint32
	for {
		if pe.ParentProcessID == parentPID && pe.ProcessID != parentPID {
			childPIDs = append(childPIDs, pe.ProcessID)
		}
		if err := syscall.Process32Next(hSnap, &pe); err != nil {
			break
		}
	}

	for _, pid := range childPIDs {
		killProcessTree(pid)
	}
}

func killProcessTree(pid uint32) {
	// Terminate descendants first
	hSnap, err := syscall.CreateToolhelp32Snapshot(syscall.TH32CS_SNAPPROCESS, 0)
	if err == nil {
		var pe syscall.ProcessEntry32
		pe.Size = uint32(unsafe.Sizeof(pe))
		if syscall.Process32First(hSnap, &pe) == nil {
			for {
				if pe.ParentProcessID == pid && pe.ProcessID != pid {
					killProcessTree(pe.ProcessID)
				}
				if syscall.Process32Next(hSnap, &pe) != nil {
					break
				}
			}
		}
		syscall.CloseHandle(hSnap)
	}

	// Terminate process
	hProc, err := syscall.OpenProcess(syscall.PROCESS_TERMINATE, false, pid)
	if err == nil {
		syscall.TerminateProcess(hProc, 1)
		syscall.CloseHandle(hProc)
	}
}
