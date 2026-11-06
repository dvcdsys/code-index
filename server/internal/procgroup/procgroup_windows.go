//go:build windows

package procgroup

import (
	"os/exec"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Every child is assigned to one process-wide Job Object created with
// JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE. The kernel closes the job's last handle
// when cix-server dies — cleanly or not — and that kills every process still in
// the job. This is the Windows replacement for "the child leads a process
// group": it is the only mechanism that also covers a hard kill of the parent
// (Task Manager, `schtasks /end`, a crash), which would otherwise orphan a
// llama-server holding the GPU.
//
// Nested jobs are supported since Windows 8, so this still works when
// cix-server itself runs inside a job (Task Scheduler puts its tasks in one).

var (
	jobOnce sync.Once
	job     windows.Handle
	jobErr  error
)

func ensureJob() (windows.Handle, error) {
	jobOnce.Do(func() {
		h, err := windows.CreateJobObject(nil, nil)
		if err != nil {
			jobErr = err
			return
		}
		info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
			BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
				LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
			},
		}
		if _, err := windows.SetInformationJobObject(h,
			windows.JobObjectExtendedLimitInformation,
			uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
			windows.CloseHandle(h)
			jobErr = err
			return
		}
		job = h // intentionally never closed: it closes when the process dies
	})
	return job, jobErr
}

// Prepare configures cmd before Start. The child gets no console window of
// its own; it must not inherit a console it could receive CTRL events from.
func Prepare(cmd *exec.Cmd) {
	cmd.SysProcAttr = hiddenProcAttr()
}

// Adopt places a started child in the kill-on-close job. There is a short
// window between Start and Adopt in which a child could spawn grandchildren
// that escape the job; none of the children we run do that at startup.
func Adopt(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	h, err := ensureJob()
	if err != nil {
		return err
	}
	ph, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE,
		false, uint32(cmd.Process.Pid))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(ph)
	return windows.AssignProcessToJobObject(h, ph)
}

// Terminate stops the child. Windows has no SIGTERM for a process we do not
// share a console with, and the children (an embedding sidecar, tunnel
// binaries) hold no state worth a graceful path, so this is TerminateProcess.
func Terminate(cmd *exec.Cmd) int { return Kill(cmd) }

// Kill force-terminates the child.
func Kill(cmd *exec.Cmd) int {
	if cmd == nil || cmd.Process == nil {
		return 0
	}
	_ = cmd.Process.Kill()
	return cmd.Process.Pid
}
