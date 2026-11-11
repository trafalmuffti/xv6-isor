package main

// getprocs(struct procinfo *table, int max): an isor extension to the xv6
// system call list that copies a description of every process into the
// caller's buffer and returns how many entries it wrote. It is used by
// the pslist program. xv6 itself only offers a kernel-side procdump()
// on Ctrl-P.
//
// It reports everything the sentry knows about a guest process, in guest
// terms. Host-only details -- the host pid, host paths -- are deliberately
// left out, so a guest learns nothing about the machine it runs on.

import (
	"encoding/binary"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

// struct procinfo in user/procinfo.h. Byte offsets on x86-64:
//
//	int pid;            0     int ppid;          4
//	int state;          8     int killed;       12
//	char wchan[8];     16     uint64 sz;        24
//	uint64 brk;        32     uint cputicks;    40
//	uint start;        44     char name[16];    48
//	char cwd[128];     64     char ofile[16];  192
const procinfoSize = 208

// Values of procinfo.state.
const (
	piRunning  = 1 // executing user code (or runnable)
	piSleeping = 2 // blocked in a system call
	piZombie   = 3 // exited, waiting for its parent to wait()
)

func sysGetprocs(k *Kernel, p *Proc, r *syscall.PtraceRegs) result {
	addr, max := arg(r, 0), argint(r, 1)
	if max < 0 {
		return fail()
	}
	procs := make([]*Proc, 0, len(k.procs))
	for _, q := range k.procs {
		procs = append(procs, q)
	}
	sort.Slice(procs, func(i, j int) bool { return procs[i].pid < procs[j].pid })
	if len(procs) > max {
		procs = procs[:max]
	}
	buf := make([]byte, 0, len(procs)*procinfoSize)
	for _, q := range procs {
		buf = append(buf, k.procinfo(q)...)
	}
	if copyOut(p, addr, buf) != nil {
		return fail()
	}
	return ret(int64(len(procs)))
}

func (k *Kernel) procinfo(q *Proc) []byte {
	b := make([]byte, procinfoSize)
	le := binary.LittleEndian

	le.PutUint32(b[0:], uint32(q.pid))
	if q.parent != nil {
		le.PutUint32(b[4:], uint32(q.parent.pid))
	}
	state, wchan := piRunning, ""
	switch {
	case q.state == zombie:
		state = piZombie
	case q.blocked:
		state, wchan = piSleeping, q.sysname
	}
	le.PutUint32(b[8:], uint32(state))
	if q.killed.Load() && q.state != zombie {
		le.PutUint32(b[12:], 1)
	}
	copy(b[16:23], wchan)

	cpu := q.cpuTicks
	if q.state != zombie {
		sz, ticks := hostProcStats(q.hpid)
		le.PutUint64(b[24:], sz)
		cpu = ticks
	}
	le.PutUint64(b[32:], q.brk)
	le.PutUint32(b[40:], cpu)
	le.PutUint32(b[44:], q.startTick)
	copy(b[48:63], q.name)
	copy(b[64:191], q.cwd)

	for fd, f := range q.ofile {
		b[192+fd] = fileCode(f)
	}
	return b
}

// fileCode summarizes an open file in one character for procinfo.ofile.
func fileCode(f *File) byte {
	switch {
	case f == nil:
		return 0
	case f.kind == fdPipe && f.writable:
		return 'w' // pipe, write end
	case f.kind == fdPipe:
		return 'r' // pipe, read end
	case f.kind == fdDevice:
		return 'c' // console (device)
	case f.isDir:
		return 'd'
	default:
		return 'f'
	}
}

// hostProcStats reads a live guest's memory size (bytes) and CPU time
// (user+system, in clock ticks) from the host's /proc. The host counts
// CPU time in USER_HZ units, which is 100 on x86 -- the same 10 ms tick
// as the sentry.
func hostProcStats(hpid int) (sz uint64, cpu uint32) {
	if data, err := os.ReadFile(fmt.Sprintf("/proc/%d/statm", hpid)); err == nil {
		if f := strings.Fields(string(data)); len(f) > 0 {
			pages, _ := strconv.ParseUint(f[0], 10, 64)
			sz = pages * pageSize
		}
	}
	if data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", hpid)); err == nil {
		// The command name may contain spaces; fields resume after ')'.
		s := string(data)
		if i := strings.LastIndexByte(s, ')'); i >= 0 {
			f := strings.Fields(s[i+1:])
			if len(f) > 12 { // f[0] is field 3 (state); utime=14, stime=15
				ut, _ := strconv.ParseUint(f[11], 10, 32)
				st, _ := strconv.ParseUint(f[12], 10, 32)
				cpu = uint32(ut + st)
			}
		}
	}
	return
}

// rusageTicks converts wait4's rusage into 10 ms ticks.
func rusageTicks(ru *syscall.Rusage) uint32 {
	us := (ru.Utime.Sec+ru.Stime.Sec)*1000000 + int64(ru.Utime.Usec+ru.Stime.Usec)
	return uint32(us / int64(tick/1000))
}
