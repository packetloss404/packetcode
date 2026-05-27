//go:build windows

package procrun

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"unsafe"

	"golang.org/x/sys/windows"
)

func configurePlatform(cmd *exec.Cmd) {
	cmd.SysProcAttr = &windows.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP}
}

func KillTree(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	pid := uint32(cmd.Process.Pid)
	if err := killDescendants(pid); err != nil {
		_ = exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run()
	}
	if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	return nil
}

func killDescendants(root uint32) error {
	children, err := processChildren()
	if err != nil {
		return err
	}
	var errs []error
	for _, pid := range postorder(root, children) {
		if err := terminateProcess(pid); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func processChildren() (map[uint32][]uint32, error) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(snap)

	children := map[uint32][]uint32{}
	var pe windows.ProcessEntry32
	pe.Size = uint32(unsafe.Sizeof(pe))
	if err := windows.Process32First(snap, &pe); err != nil {
		return nil, err
	}
	for {
		children[pe.ParentProcessID] = append(children[pe.ParentProcessID], pe.ProcessID)
		if err := windows.Process32Next(snap, &pe); err != nil {
			if err == windows.ERROR_NO_MORE_FILES {
				break
			}
			return nil, err
		}
	}
	return children, nil
}

func postorder(root uint32, children map[uint32][]uint32) []uint32 {
	var out []uint32
	var walk func(uint32)
	walk = func(pid uint32) {
		for _, child := range children[pid] {
			walk(child)
		}
		if pid != root {
			out = append(out, pid)
		}
	}
	walk(root)
	return out
}

func terminateProcess(pid uint32) error {
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE|windows.SYNCHRONIZE, false, pid)
	if err != nil {
		if err == windows.ERROR_INVALID_PARAMETER {
			return nil
		}
		return fmt.Errorf("open process %d: %w", pid, err)
	}
	defer windows.CloseHandle(h)
	if err := windows.TerminateProcess(h, 1); err != nil {
		return fmt.Errorf("terminate process %d: %w", pid, err)
	}
	event, err := windows.WaitForSingleObject(h, 500)
	if err != nil {
		return fmt.Errorf("wait for process %d: %w", pid, err)
	}
	if event == uint32(windows.WAIT_TIMEOUT) {
		return fmt.Errorf("wait for process %d: timeout", pid)
	}
	return nil
}
