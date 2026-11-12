package main

// System call dispatch (xv6's syscall.c) and the process-related calls
// (xv6's sysproc.c).

import (
	"fmt"
	"os"
	"path"
	"strings"
	"syscall"
	"time"
)

// Host (Linux x86-64) system calls the sentry rewrites guest calls into.
const (
	hostBrk       = syscall.SYS_BRK
	hostClone     = syscall.SYS_CLONE
	hostExecve    = syscall.SYS_EXECVE
	hostExitGroup = syscall.SYS_EXIT_GROUP

	// fork() becomes clone(CLONE_PARENT|SIGCHLD): CLONE_PARENT makes the
	// sentry, not the guest parent, the host parent of every guest, so
	// the sentry can reap them all with wait4().
	forkFlags = syscall.CLONE_PARENT | uintptr(syscall.SIGCHLD)

	tick = 10 * time.Millisecond // xv6's timer interrupt rate (100 Hz)
)

// result tells syscallEnter what to do with a system call.
type result struct {
	kind resultKind
	val  int64
	hook func(*syscall.PtraceRegs) // resHost: runs at the exit stop
}

type resultKind int

const (
	resValue   resultKind = iota // emulated; val is the return value
	resBlocked                   // emulated; finished later by complete()
	resHost                      // rewritten; the host kernel runs it
)

func ret(v int64) result                      { return result{kind: resValue, val: v} }
func fail() result                            { return ret(-1) }
func blocked() result                         { return result{kind: resBlocked} }
func host(h func(*syscall.PtraceRegs)) result { return result{kind: resHost, hook: h} }

type sysFunc func(k *Kernel, p *Proc, r *syscall.PtraceRegs) result

// The xv6 system call table. Numbers are xv6's (user/syscall.h).
var syscalls = map[uint64]struct {
	name string
	args string // for -strace: i = int, p = pointer, s = string
	fn   sysFunc
}{
	1:  {"fork", "", sysFork},
	2:  {"exit", "", sysExit},
	3:  {"wait", "", sysWait},
	4:  {"pipe", "p", sysPipe},
	5:  {"read", "ipi", sysRead},
	6:  {"kill", "i", sysKill},
	7:  {"exec", "sp", sysExec},
	8:  {"fstat", "ip", sysFstat},
	9:  {"chdir", "s", sysChdir},
	10: {"dup", "i", sysDup},
	11: {"getpid", "", sysGetpid},
	12: {"sbrk", "i", sysSbrk},
	13: {"sleep", "i", sysSleep},
	14: {"uptime", "", sysUptime},
	15: {"open", "si", sysOpen},
	16: {"write", "ipi", sysWrite},
	17: {"mknod", "sii", sysMknod},
	18: {"unlink", "s", sysUnlink},
	19: {"link", "ss", sysLink},
	20: {"mkdir", "s", sysMkdir},
	21: {"close", "i", sysClose},

	// isor extension, not in xv6: lets the pslist program list processes.
	22: {"getprocs", "pi", sysGetprocs},
}

// Argument helpers: xv6's argint/argaddr. The x86-64 calling convention
// already put the arguments in %rdi, %rsi, %rdx.
func arg(r *syscall.PtraceRegs, n int) uint64 {
	switch n {
	case 0:
		return r.Rdi
	case 1:
		return r.Rsi
	default:
		return r.Rdx
	}
}

func argint(r *syscall.PtraceRegs, n int) int { return int(int32(arg(r, n))) }

func argstr(p *Proc, r *syscall.PtraceRegs, n int) (string, bool) {
	s, err := copyInStr(p, arg(r, n), maxPath)
	return s, err == nil
}

// argfd fetches a file-descriptor argument and its open file.
func argfd(p *Proc, r *syscall.PtraceRegs, n int) (int, *File) {
	fd := argint(r, n)
	if fd < 0 || fd >= nofile || p.ofile[fd] == nil {
		return -1, nil
	}
	return fd, p.ofile[fd]
}

// fdalloc gives f the lowest free descriptor (takes over the reference).
func (p *Proc) fdalloc(f *File) int {
	for fd := range p.ofile {
		if p.ofile[fd] == nil {
			p.ofile[fd] = f
			return fd
		}
	}
	return -1
}

// syscallStop handles a syscall-entry or syscall-exit stop.
func (k *Kernel) syscallStop(p *Proc) {
	op, arch, err := syscallInfo(p.hpid)
	if err != nil {
		k.resume(p, 0)
		return
	}
	var regs syscall.PtraceRegs
	if err := ptraceGetRegs(p.hpid, &regs); err != nil {
		return // process vanished
	}
	switch op {
	case syscallInfoEntry:
		if arch != auditArchX86_64 {
			// `int $0x80` uses the 32-bit syscall table, where our
			// rewrites would mean something else entirely. Refuse.
			fmt.Fprintf(os.Stderr, "pid %d %s: 32-bit system call -- kill proc\n", p.pid, p.name)
			regs.Orig_rax = ^uint64(0)
			ptraceSetRegs(p.hpid, &regs)
			k.killProc(p)
			return
		}
		k.syscallEnter(p, &regs)
	case syscallInfoExit:
		k.syscallExit(p, &regs)
	default:
		k.resume(p, 0)
	}
}

func (k *Kernel) syscallEnter(p *Proc, regs *syscall.PtraceRegs) {
	nr := regs.Orig_rax
	sc, ok := syscalls[nr]
	var res result
	if !ok {
		fmt.Fprintf(os.Stderr, "%d %s: unknown sys call %d\n", p.pid, p.name, int64(nr))
		res = fail()
	} else {
		if k.strace {
			p.tracemsg = fmt.Sprintf("[%d %s] %s", p.pid, p.name, traceCall(p, regs, sc.name, sc.args))
		}
		p.sysname = sc.name
		res = sc.fn(k, p, regs)
	}

	switch res.kind {
	case resValue, resBlocked:
		// Tell the host kernel to skip the call; the exit stop will
		// install p.retval in %rax.
		regs.Orig_rax = ^uint64(0)
		p.retval = res.val
		p.exitHook = nil
		if err := ptraceSetRegs(p.hpid, regs); err != nil {
			return
		}
		if res.kind == resBlocked {
			p.blocked = true // stay stopped until complete()
			return
		}
	case resHost:
		p.exitHook = res.hook
		if err := ptraceSetRegs(p.hpid, regs); err != nil {
			return
		}
	}
	k.resume(p, 0)
}

func (k *Kernel) syscallExit(p *Proc, regs *syscall.PtraceRegs) {
	if h := p.exitHook; h != nil {
		p.exitHook = nil
		h(regs)
	} else {
		regs.Rax = uint64(p.retval)
	}
	if k.strace && p.tracemsg != "" {
		fmt.Fprintf(os.Stderr, "%s = %d\n", p.tracemsg, int64(regs.Rax))
		p.tracemsg = ""
	}
	ptraceSetRegs(p.hpid, regs)
	k.resume(p, 0)
}

// ---- process system calls ----

func sysFork(k *Kernel, p *Proc, r *syscall.PtraceRegs) result {
	if len(k.procs) >= nproc {
		return fail()
	}
	// Let the host duplicate the address space. The PTRACE_EVENT_FORK
	// stop that follows (see forkChild) copies the sentry-side state.
	r.Orig_rax = hostClone
	r.Rdi = uint64(forkFlags)
	r.Rsi, r.Rdx, r.R10, r.R8 = 0, 0, 0, 0
	return host(func(r *syscall.PtraceRegs) {
		// In the parent, translate the host pid into a guest pid.
		if c := k.byHost[int(int64(r.Rax))]; int64(r.Rax) > 0 && c != nil {
			r.Rax = uint64(c.pid)
		} else {
			r.Rax = ^uint64(0)
		}
	})
}

func sysExit(k *Kernel, p *Proc, r *syscall.PtraceRegs) result {
	// The host process really exits; procExited() does the rest when
	// wait4() reports it.
	r.Orig_rax = hostExitGroup
	r.Rdi = 0
	if k.strace {
		fmt.Fprintf(os.Stderr, "[%d %s] exit()\n", p.pid, p.name)
		p.tracemsg = ""
	}
	return host(nil)
}

func sysWait(k *Kernel, p *Proc, r *syscall.PtraceRegs) result {
	if pid, done := k.tryWait(p); done {
		return ret(pid)
	}
	p.waiting = true // procExited() will call wakeWaiter(p)
	return blocked()
}

func sysKill(k *Kernel, p *Proc, r *syscall.PtraceRegs) result {
	t := k.procs[argint(r, 0)]
	if t == nil || t.state != running {
		return fail()
	}
	k.killProc(t)
	return ret(0)
}

func sysGetpid(k *Kernel, p *Proc, r *syscall.PtraceRegs) result {
	return ret(int64(p.pid))
}

func sysUptime(k *Kernel, p *Proc, r *syscall.PtraceRegs) result {
	return ret(int64(k.ticks()))
}

func sysSleep(k *Kernel, p *Proc, r *syscall.PtraceRegs) result {
	n := argint(r, 0)
	if n < 0 {
		n = 0
	}
	time.AfterFunc(time.Duration(n)*tick, func() {
		k.work <- func() {
			if p.killed.Load() {
				k.complete(p, -1)
			} else {
				k.complete(p, 0)
			}
		}
	})
	return blocked()
}

// sbrk(n) grows memory by n bytes and returns the old break. The host
// kernel manages the memory, so this becomes brk(old+n). The sentry
// needs the current break for that; the first time, it asks the host
// with brk(0) and then *restarts* the guest's sbrk by backing %rip up
// over the 2-byte `syscall` instruction -- the same trick the kernel uses
// to restart interrupted system calls.
func sysSbrk(k *Kernel, p *Proc, r *syscall.PtraceRegs) result {
	if p.brk == 0 {
		saved := *r
		r.Orig_rax = hostBrk
		r.Rdi = 0
		return host(func(r *syscall.PtraceRegs) {
			p.brk = r.Rax
			*r = saved
			r.Rax = saved.Orig_rax // re-execute the guest's sbrk
			r.Rip -= 2
			p.tracemsg = ""
		})
	}
	n := int64(argint(r, 0))
	old := p.brk
	if n == 0 {
		return ret(int64(old))
	}
	newBrk := uint64(int64(old) + n)
	r.Orig_rax = hostBrk
	r.Rdi = newBrk
	return host(func(r *syscall.PtraceRegs) {
		if r.Rax == newBrk {
			p.brk = newBrk
			r.Rax = old
		} else {
			r.Rax = ^uint64(0) // out of memory
		}
	})
}

// exec(path, argv). The host kernel loads the ELF file: the sentry
// translates the guest path into a host path, writes it into the
// guest's memory (below the stack red zone) and rewrites the call into
// execve(hostpath, argv, NULL).
func sysExec(k *Kernel, p *Proc, r *syscall.PtraceRegs) result {
	gpath, ok := argstr(p, r, 0)
	if !ok {
		return fail()
	}
	// Check argv like xv6 does: at most MAXARG readable strings.
	uargv := arg(r, 1)
	for i := 0; ; i++ {
		if i >= maxArg {
			return fail()
		}
		a, err := copyInU64(p, uargv+uint64(8*i))
		if err != nil {
			return fail()
		}
		if a == 0 {
			break
		}
		if _, err := copyInStr(p, a, pageSize); err != nil {
			return fail()
		}
	}
	hpath, err := k.fs.execPath(abspath(p.cwd, gpath))
	if err != nil {
		if k.strace {
			fmt.Fprintf(os.Stderr, "[%d %s] exec %q: %v\n", p.pid, p.name, gpath, err)
		}
		return fail()
	}
	buf := append([]byte(hpath), 0)
	sp := (r.Rsp - 128 - uint64(len(buf))) &^ 15
	if copyOut(p, sp, buf) != nil {
		return fail()
	}
	r.Orig_rax = hostExecve
	r.Rdi = sp
	r.Rdx = 0 // empty environment
	name := path.Base(gpath)
	if len(name) > 15 {
		name = name[:15]
	}
	return host(func(r *syscall.PtraceRegs) {
		if int64(r.Rax) < 0 {
			r.Rax = ^uint64(0)
			return
		}
		p.name = name
		p.brk = 0 // new address space
		r.Rax = 0
	})
}

// traceCall formats a system call for -strace, e.g. open("console", 2).
func traceCall(p *Proc, r *syscall.PtraceRegs, name, args string) string {
	var parts []string
	for i, kind := range args {
		switch kind {
		case 'i':
			parts = append(parts, fmt.Sprint(argint(r, i)))
		case 'p':
			parts = append(parts, fmt.Sprintf("%#x", arg(r, i)))
		case 's':
			if str, ok := argstr(p, r, i); ok {
				parts = append(parts, fmt.Sprintf("%q", str))
			} else {
				parts = append(parts, fmt.Sprintf("%#x", arg(r, i)))
			}
		}
	}
	return name + "(" + strings.Join(parts, ", ") + ")"
}
