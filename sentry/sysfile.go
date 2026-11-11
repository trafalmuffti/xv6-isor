package main

// File-system system calls (xv6's sysfile.c).

import (
	"encoding/binary"
	"io"
	"syscall"
)

const maxIO = 1 << 20 // cap on a single read/write

func sysDup(k *Kernel, p *Proc, r *syscall.PtraceRegs) result {
	_, f := argfd(p, r, 0)
	if f == nil {
		return fail()
	}
	fd := p.fdalloc(f)
	if fd < 0 {
		return fail()
	}
	f.dup()
	return ret(int64(fd))
}

func sysClose(k *Kernel, p *Proc, r *syscall.PtraceRegs) result {
	fd, f := argfd(p, r, 0)
	if f == nil {
		return fail()
	}
	p.ofile[fd] = nil
	f.close()
	return ret(0)
}

func sysRead(k *Kernel, p *Proc, r *syscall.PtraceRegs) result {
	_, f := argfd(p, r, 0)
	addr, n := arg(r, 1), argint(r, 2)
	if f == nil || !f.readable || n < 0 {
		return fail()
	}
	if n > maxIO {
		n = maxIO
	}
	// deliver copies what was read into the guest's buffer.
	deliver := func(b []byte, cnt int64) int64 {
		if cnt > 0 && copyOut(p, addr, b[:cnt]) != nil {
			return -1
		}
		return cnt
	}
	switch f.kind {
	case fdPipe:
		return k.async(p, func() func() int64 {
			b, cnt := f.pipe.read(&p.killed, n)
			return func() int64 { return deliver(b, cnt) }
		})
	case fdDevice:
		if f.major != consoleMajor {
			return fail()
		}
		return k.async(p, func() func() int64 {
			b, cnt := k.console.read(n)
			return func() int64 { return deliver(b, cnt) }
		})
	case fdInode:
		if f.isDir {
			b := f.dir[min(f.off, len(f.dir)):]
			cnt := min(len(b), n)
			f.off += cnt
			return ret(deliver(b, int64(cnt)))
		}
		b := make([]byte, n)
		cnt, err := f.host.Read(b)
		if err != nil && err != io.EOF {
			return fail()
		}
		return ret(deliver(b, int64(cnt)))
	}
	return fail()
}

func sysWrite(k *Kernel, p *Proc, r *syscall.PtraceRegs) result {
	_, f := argfd(p, r, 0)
	addr, n := arg(r, 1), argint(r, 2)
	if f == nil || !f.writable || n < 0 {
		return fail()
	}
	if n > maxIO {
		n = maxIO
	}
	b, err := copyIn(p, addr, n)
	if err != nil {
		return fail()
	}
	switch f.kind {
	case fdPipe:
		return k.async(p, func() func() int64 {
			cnt := f.pipe.write(&p.killed, b)
			return func() int64 { return cnt }
		})
	case fdDevice:
		if f.major != consoleMajor {
			return fail()
		}
		return ret(k.console.write(b))
	case fdInode:
		cnt, err := f.host.Write(b)
		if err != nil && cnt == 0 {
			return fail()
		}
		return ret(int64(cnt))
	}
	return fail()
}

// fstat fills in xv6's struct stat:
//
//	struct stat { short type; int dev; uint ino; short nlink; uint size; };
//
// which on x86-64 is laid out as type@0 dev@4 ino@8 nlink@12 size@16.
func sysFstat(k *Kernel, p *Proc, r *syscall.PtraceRegs) result {
	_, f := argfd(p, r, 0)
	if f == nil || f.kind == fdPipe {
		return fail()
	}
	var st syscall.Stat_t
	if err := syscall.Fstat(int(f.host.Fd()), &st); err != nil {
		return fail()
	}
	typ, size := tFILE, uint32(st.Size)
	switch {
	case f.kind == fdDevice:
		typ, size = tDEV, 0
	case f.isDir:
		typ, size = tDIR, uint32(len(f.dir))
	}
	var b [20]byte
	binary.LittleEndian.PutUint16(b[0:], uint16(typ))
	binary.LittleEndian.PutUint32(b[4:], 1) // ROOTDEV
	binary.LittleEndian.PutUint32(b[8:], uint32(st.Ino))
	binary.LittleEndian.PutUint16(b[12:], uint16(st.Nlink))
	binary.LittleEndian.PutUint32(b[16:], size)
	if copyOut(p, arg(r, 1), b[:]) != nil {
		return fail()
	}
	return ret(0)
}

func sysOpen(k *Kernel, p *Proc, r *syscall.PtraceRegs) result {
	path, ok := argstr(p, r, 0)
	if !ok {
		return fail()
	}
	f, err := k.fs.open(abspath(p.cwd, path), argint(r, 1))
	if err != nil {
		return fail()
	}
	f.ref = 1
	fd := p.fdalloc(f)
	if fd < 0 {
		f.close()
		return fail()
	}
	return ret(int64(fd))
}

func sysPipe(k *Kernel, p *Proc, r *syscall.PtraceRegs) result {
	rf, wf := newPipe()
	fd0 := p.fdalloc(rf)
	fd1 := -1
	if fd0 >= 0 {
		fd1 = p.fdalloc(wf)
	}
	var b [8]byte
	binary.LittleEndian.PutUint32(b[0:], uint32(fd0))
	binary.LittleEndian.PutUint32(b[4:], uint32(fd1))
	if fd0 < 0 || fd1 < 0 || copyOut(p, arg(r, 0), b[:]) != nil {
		if fd0 >= 0 {
			p.ofile[fd0] = nil
		}
		if fd1 >= 0 {
			p.ofile[fd1] = nil
		}
		rf.close()
		wf.close()
		return fail()
	}
	return ret(0)
}

// pathCall runs a one-path operation and converts the outcome to 0/-1.
func pathCall(p *Proc, r *syscall.PtraceRegs, op func(string) error) result {
	path, ok := argstr(p, r, 0)
	if !ok {
		return fail()
	}
	if op(abspath(p.cwd, path)) != nil {
		return fail()
	}
	return ret(0)
}

func sysMkdir(k *Kernel, p *Proc, r *syscall.PtraceRegs) result {
	return pathCall(p, r, k.fs.mkdir)
}

func sysUnlink(k *Kernel, p *Proc, r *syscall.PtraceRegs) result {
	return pathCall(p, r, k.fs.unlink)
}

func sysMknod(k *Kernel, p *Proc, r *syscall.PtraceRegs) result {
	major, minor := argint(r, 1), argint(r, 2)
	return pathCall(p, r, func(path string) error { return k.fs.mknod(path, major, minor) })
}

func sysLink(k *Kernel, p *Proc, r *syscall.PtraceRegs) result {
	newp, ok := argstr(p, r, 1)
	if !ok {
		return fail()
	}
	return pathCall(p, r, func(oldp string) error { return k.fs.link(oldp, abspath(p.cwd, newp)) })
}

func sysChdir(k *Kernel, p *Proc, r *syscall.PtraceRegs) result {
	return pathCall(p, r, func(path string) error {
		if !k.fs.isDir(path) {
			return errNotFound
		}
		p.cwd = path
		return nil
	})
}
