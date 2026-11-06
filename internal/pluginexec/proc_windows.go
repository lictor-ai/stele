package pluginexec

import (
	"os/exec"
	"syscall"
	"unsafe"
)

// group leaves the plugin as Go starts it: stopping it kills the plugin, and
// contain's job kills what it started.
func group(cmd *exec.Cmd) {}

var (
	kernel32                 = syscall.NewLazyDLL("kernel32.dll")
	createJobObject          = kernel32.NewProc("CreateJobObjectW")
	setInformationJobObject  = kernel32.NewProc("SetInformationJobObject")
	assignProcessToJobObject = kernel32.NewProc("AssignProcessToJobObject")
)

const (
	jobObjectExtendedLimitInformation = 9
	jobObjectLimitKillOnJobClose      = 0x2000
	processSetQuota                   = 0x0100
	processTerminate                  = 0x0001
)

// jobLimits is JOBOBJECT_EXTENDED_LIMIT_INFORMATION.
type jobLimits struct {
	PerProcessUserTimeLimit int64
	PerJobUserTimeLimit     int64
	LimitFlags              uint32
	MinimumWorkingSetSize   uintptr
	MaximumWorkingSetSize   uintptr
	ActiveProcessLimit      uint32
	Affinity                uintptr
	PriorityClass           uint32
	SchedulingClass         uint32
	IoCounters              [6]uint64
	ProcessMemoryLimit      uintptr
	JobMemoryLimit          uintptr
	PeakProcessMemoryUsed   uintptr
	PeakJobMemoryUsed       uintptr
}

// contain puts the plugin in a job that kills every process in it when the
// job is closed, and returns what closes it: once the plugin is done, what it
// started goes too. A process it started before it joined the job, in the
// moment after it starts, is not in it. Best effort: if the job cannot be
// made, the plugin runs without one.
func contain(cmd *exec.Cmd) func() {
	job, _, _ := createJobObject.Call(0, 0)
	if job == 0 {
		return func() {}
	}
	limits := jobLimits{LimitFlags: jobObjectLimitKillOnJobClose}
	ok, _, _ := setInformationJobObject.Call(job, jobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), unsafe.Sizeof(limits))
	if ok != 0 {
		if h, err := syscall.OpenProcess(processSetQuota|processTerminate, false, uint32(cmd.Process.Pid)); err == nil {
			assignProcessToJobObject.Call(job, uintptr(h))
			syscall.CloseHandle(h)
		}
	}
	return func() { syscall.CloseHandle(syscall.Handle(job)) }
}
