package main

// Thin wrappers around the ptrace(2) requests that Go's syscall package
// does not provide.

import (
	"sync/atomic"
	"syscall"
	"unsafe"
)

const (
	// PTRACE_GET_SYSCALL_INFO (Linux 5.3+) tells us whether a syscall stop
	// is an entry or an exit stop, and which syscall ABI (64-bit `syscall`
	// vs. 32-bit `int $0x80`) the tracee used.
	ptraceGetSyscallInfo = 0x420e

	syscallInfoEntry = 1
	syscallInfoExit  = 2

	auditArchX86_64 = 0xc000003e

	ptraceOExitKill = 0x100000 // kill tracees if the sentry dies

	// Options set on the first process; children inherit them.
	ptraceOptions = syscall.PTRACE_O_TRACESYSGOOD | // syscall stops report SIGTRAP|0x80
		syscall.PTRACE_O_TRACEFORK | // auto-attach to children...
		syscall.PTRACE_O_TRACEVFORK |
		syscall.PTRACE_O_TRACECLONE |
		syscall.PTRACE_O_TRACEEXEC | // ...and report execve
		ptraceOExitKill

	// Stop signal of a syscall stop when PTRACE_O_TRACESYSGOOD is set.
	sigSyscallStop = syscall.SIGTRAP | 0x80
)

// syscallInfo returns the op (entry/exit) and audit arch of the current
// syscall stop of pid.
func syscallInfo(pid int) (op uint8, arch uint32, err error) {
	// struct ptrace_syscall_info: u8 op; u8 pad[3]; u32 arch; ...
	var buf [88]byte
	_, _, e := syscall.Syscall6(syscall.SYS_PTRACE, ptraceGetSyscallInfo,
		uintptr(pid), uintptr(len(buf)), uintptr(unsafe.Pointer(&buf[0])), 0, 0)
	if e != 0 {
		return 0, 0, e
	}
	return buf[0], *(*uint32)(unsafe.Pointer(&buf[4])), nil
}

// The ptrace requests below go straight to the raw system call rather
// than through package syscall: gccgo's syscall package lacks some of
// them (PtraceSyscall) and its libc-based wrappers misreport errors, so
// this keeps the sentry identical under both Go compilers.
func ptrace(req int, pid int, addr, data uintptr) error {
	_, _, e := syscall.Syscall6(syscall.SYS_PTRACE, uintptr(req), uintptr(pid), addr, data, 0, 0)
	if e != 0 {
		return e
	}
	return nil
}

// ptraceSyscall resumes pid until its next syscall stop, delivering sig.
func ptraceSyscall(pid int, sig syscall.Signal) error {
	return ptrace(syscall.PTRACE_SYSCALL, pid, 0, uintptr(sig))
}

func ptraceSetOptions(pid int, options int) error {
	return ptrace(syscall.PTRACE_SETOPTIONS, pid, 0, uintptr(options))
}

func ptraceGetEventMsg(pid int) (uint64, error) {
	var msg uint64
	err := ptrace(syscall.PTRACE_GETEVENTMSG, pid, 0, uintptr(unsafe.Pointer(&msg)))
	return msg, err
}

func ptraceGetRegs(pid int, regs *syscall.PtraceRegs) error {
	return ptrace(syscall.PTRACE_GETREGS, pid, 0, uintptr(unsafe.Pointer(regs)))
}

func ptraceSetRegs(pid int, regs *syscall.PtraceRegs) error {
	return ptrace(syscall.PTRACE_SETREGS, pid, 0, uintptr(unsafe.Pointer(regs)))
}

// atomicBool is a minimal atomic.Bool (which needs Go 1.19; gccgo 13
// provides the Go 1.18 standard library).
type atomicBool struct{ v int32 }

func (b *atomicBool) Load() bool { return atomic.LoadInt32(&b.v) != 0 }

func (b *atomicBool) Store(val bool) {
	var i int32
	if val {
		i = 1
	}
	atomic.StoreInt32(&b.v, i)
}
