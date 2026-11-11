package main

// The sentry's core: process table and the ptrace event loop.
//
// Every guest process is an ordinary host process that the sentry traces
// with PTRACE_SYSCALL, so the host kernel stops it at the entry and at the
// exit of every system call it makes. At the entry stop the sentry decides
// what the call means (according to the xv6 ABI, not Linux's) and then
// either:
//
//   - emulates it entirely: it sets orig_rax to -1 so the host kernel skips
//     the call, and later writes the result into %rax (read, open, pipe, ...);
//   - blocks: the process simply stays stopped while a goroutine waits for
//     the event (pipe data, console input, a child exiting, a timer);
//   - rewrites it into a host system call that only touches the process
//     itself (fork -> clone, exec -> execve, sbrk -> brk, exit -> exit_group).
//
// The host kernel therefore never executes a system call that the sentry
// did not construct. This is the same split as gVisor's "sentry": the
// application talks to a user-mode kernel, and only that kernel talks to
// the host.
//
// All ptrace requests must come from the thread that is the tracer, so one
// goroutine, locked to its OS thread, owns all of this state. Anything that
// might block runs on other goroutines and sends a closure back on k.work.

import (
	"fmt"
	"os"
	"runtime"
	"sync/atomic"
	"syscall"
	"time"
)

type procState int

const (
	running procState = iota
	zombie
)

// Proc is xv6's struct proc, minus everything the host kernel does for us
// (page table, kernel stack, trap frame, scheduling).
type Proc struct {
	pid    int // guest pid
	hpid   int // host pid
	name   string
	parent *Proc
	state  procState
	killed atomic.Bool // read by pipe goroutines
	cwd    string      // absolute guest path
	ofile  [nofile]*File
	brk    uint64 // current program break; 0 = not yet known

	// Syscall bookkeeping.
	blocked  bool                      // stopped at entry, waiting
	waiting  bool                      // blocked in wait()
	retval   int64                     // result to put in %rax at exit
	exitHook func(*syscall.PtraceRegs) // for rewritten host syscalls
	newborn  bool                      // expect the initial SIGSTOP
	tracemsg string                    // -strace: the call being made
}

type waitEvent struct {
	pid    int
	status syscall.WaitStatus
}

type Kernel struct {
	fs      *FS
	console *Console
	strace  bool
	start   time.Time
	tracing bool // ptrace options have been set on init

	procs   map[int]*Proc // by guest pid
	byHost  map[int]*Proc // by host pid
	nextPid int
	initp   *Proc
	early   map[int]bool // host pids that stopped before we heard of them

	events chan waitEvent
	work   chan func()
}

func newKernel(fs *FS, console *Console, strace bool) *Kernel {
	return &Kernel{
		fs:      fs,
		console: console,
		strace:  strace,
		start:   time.Now(),
		procs:   map[int]*Proc{},
		byHost:  map[int]*Proc{},
		nextPid: 1,
		early:   map[int]bool{},
		events:  make(chan waitEvent, 64),
		work:    make(chan func(), 64),
	}
}

func (k *Kernel) allocProc(hpid int) *Proc {
	p := &Proc{pid: k.nextPid, hpid: hpid, cwd: "/"}
	k.nextPid++
	k.procs[p.pid] = p
	k.byHost[hpid] = p
	return p
}

// Run starts /init and runs the event loop. It does not return.
func (k *Kernel) Run(initPath string) {
	runtime.LockOSThread() // this thread is the tracer of every guest

	hostInit, err := k.fs.execPath(initPath)
	if err != nil {
		fatalf("cannot run %s: %v", initPath, err)
	}
	hpid, err := syscall.ForkExec(hostInit, []string{"init"}, &syscall.ProcAttr{
		Dir: k.fs.rootPath,
		Env: []string{},
		// No Files: the guest gets no host file descriptors at all.
		Sys: &syscall.SysProcAttr{Ptrace: true}, // PTRACE_TRACEME before execve
	})
	if err != nil {
		fatalf("starting init: %v", err)
	}
	k.initp = k.allocProc(hpid)
	k.initp.name = "init"

	// wait4() may be called from any thread of the tracer's thread group,
	// so a helper goroutine can block in it and forward the results.
	go func() {
		for {
			var ws syscall.WaitStatus
			pid, err := syscall.Wait4(-1, &ws, syscall.WALL, nil)
			if err == syscall.EINTR {
				continue
			}
			if err != nil {
				return
			}
			k.events <- waitEvent{pid, ws}
		}
	}()

	for {
		select {
		case ev := <-k.events:
			k.handleWait(ev.pid, ev.status)
		case fn := <-k.work:
			fn()
		}
	}
}

// resume continues a stopped guest until its next syscall stop,
// delivering sig (0 for none).
func (k *Kernel) resume(p *Proc, sig syscall.Signal) {
	syscall.PtraceSyscall(p.hpid, int(sig)) // ESRCH if it was killed: fine
}

func (k *Kernel) handleWait(hpid int, ws syscall.WaitStatus) {
	p := k.byHost[hpid]
	if ws.Exited() || ws.Signaled() {
		if p != nil {
			k.procExited(p)
		}
		delete(k.early, hpid)
		return
	}
	if !ws.Stopped() {
		return
	}
	if p == nil {
		// A freshly forked child can report its first stop before its
		// parent's PTRACE_EVENT_FORK. Leave it stopped until then.
		k.early[hpid] = true
		return
	}
	sig := ws.StopSignal()
	switch {
	case sig == sigSyscallStop:
		k.syscallStop(p)

	case sig == syscall.SIGTRAP && ws.TrapCause() > 0:
		k.ptraceEvent(p, ws.TrapCause())

	case sig == syscall.SIGTRAP && !k.tracing:
		// init's first stop, right after the host execve: configure
		// tracing. Children inherit these options.
		if err := syscall.PtraceSetOptions(hpid, ptraceOptions); err != nil {
			fatalf("PTRACE_SETOPTIONS: %v", err)
		}
		k.tracing = true
		k.resume(p, 0)

	case sig == syscall.SIGSTOP && p.newborn:
		p.newborn = false
		k.resume(p, 0)

	case sig == syscall.SIGSEGV || sig == syscall.SIGBUS || sig == syscall.SIGILL ||
		sig == syscall.SIGFPE || sig == syscall.SIGTRAP || sig == syscall.SIGSYS:
		// A hardware trap in user code. Like xv6's usertrap(), report it
		// and kill the process (delivering the signal does that, since
		// the guest has no way to install signal handlers).
		fmt.Fprintf(os.Stderr, "pid %d %s: trap %v -- kill proc\n", p.pid, p.name, sig)
		p.killed.Store(true)
		k.resume(p, sig)

	default:
		// xv6 has no signals; swallow anything else (e.g. SIGINT from
		// the terminal, job-control stops).
		k.resume(p, 0)
	}
}

func (k *Kernel) ptraceEvent(p *Proc, event int) {
	switch event {
	case syscall.PTRACE_EVENT_FORK, syscall.PTRACE_EVENT_VFORK, syscall.PTRACE_EVENT_CLONE:
		msg, err := syscall.PtraceGetEventMsg(p.hpid)
		if err == nil {
			k.forkChild(p, int(msg))
		}
	}
	k.resume(p, 0)
}

// forkChild records the new host process created by a rewritten fork().
// The host kernel already copied the address space; we copy the
// kernel-side state that lives in the sentry (xv6's fork() in proc.c).
func (k *Kernel) forkChild(parent *Proc, hpid int) {
	np := k.allocProc(hpid)
	np.parent = parent
	np.name = parent.name
	np.cwd = parent.cwd
	np.brk = parent.brk
	for i, f := range parent.ofile {
		if f != nil {
			np.ofile[i] = f.dup()
		}
	}
	np.retval = 0 // fork returns 0 in the child
	if k.early[hpid] {
		delete(k.early, hpid)
		k.resume(np, 0)
	} else {
		np.newborn = true
	}
}

// procExited runs when the host reports that a guest process is gone,
// whether it called exit(), was killed, or crashed. It is the second
// half of xv6's exit(): close files, hand children to init, become a
// zombie and wake the parent.
func (k *Kernel) procExited(p *Proc) {
	p.killed.Store(true)
	for i, f := range p.ofile {
		if f != nil {
			f.close()
			p.ofile[i] = nil
		}
	}
	p.state = zombie
	p.blocked = false
	delete(k.byHost, p.hpid)

	if p == k.initp {
		fmt.Fprintln(os.Stderr, "init exiting")
		os.Exit(1)
	}
	for _, c := range k.procs {
		if c.parent == p {
			c.parent = k.initp
		}
	}
	k.wakeWaiter(p.parent)
	k.wakeWaiter(k.initp)
}

// tryWait looks for a zombie child of p (xv6's wait() loop body). done is
// false if p has live children but none have exited yet.
func (k *Kernel) tryWait(p *Proc) (pid int64, done bool) {
	havekids := false
	for _, c := range k.procs {
		if c.parent != p {
			continue
		}
		havekids = true
		if c.state == zombie {
			delete(k.procs, c.pid) // free the pid
			return int64(c.pid), true
		}
	}
	if !havekids || p.killed.Load() {
		return -1, true
	}
	return 0, false
}

func (k *Kernel) wakeWaiter(p *Proc) {
	if p == nil || !p.waiting {
		return
	}
	if pid, done := k.tryWait(p); done {
		p.waiting = false
		k.complete(p, pid)
	}
}

// complete finishes a blocked system call with result v.
func (k *Kernel) complete(p *Proc, v int64) {
	if p.state != running || !p.blocked {
		return // killed while blocked
	}
	p.blocked = false
	p.retval = v
	k.resume(p, 0)
	k.maybeShutdown()
}

// async runs a blocking operation on its own goroutine. work runs off the
// ptrace thread and must not touch kernel state; the function it returns
// runs back on the ptrace thread to produce the system call's result
// (typically copying data into the guest).
func (k *Kernel) async(p *Proc, work func() func() int64) result {
	go func() {
		finish := work()
		k.work <- func() {
			if p.state == running && p.blocked {
				k.complete(p, finish())
			}
		}
	}()
	return blocked()
}

func (k *Kernel) killProc(p *Proc) {
	p.killed.Store(true)
	syscall.Kill(p.hpid, syscall.SIGKILL)
	for _, f := range p.ofile {
		if f != nil && f.pipe != nil {
			f.pipe.wakeup()
		}
	}
}

// maybeShutdown stops the sentry once non-interactive console input has
// been used up (otherwise init would restart sh forever).
func (k *Kernel) maybeShutdown() {
	if k.console.exhausted() {
		os.Exit(0)
	}
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "xv6-isor: "+format+"\n", args...)
	os.Exit(1)
}
