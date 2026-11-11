package main

// Open files, pipes and the console (xv6's file.c, pipe.c, console.c).

import (
	"io"
	"os"
	"sync"
)

// Limits and flags, from xv6's param.h, fcntl.h, stat.h and fs.h.
const (
	nproc   = 64  // maximum number of processes
	nofile  = 16  // open files per process
	maxArg  = 32  // max exec arguments
	maxPath = 128 // maximum file path name
	dirSiz  = 14  // directory entry name length

	oRDONLY = 0x000
	oWRONLY = 0x001
	oRDWR   = 0x002
	oCREATE = 0x200
	oTRUNC  = 0x400 // later xv6 versions; harmless to support

	tDIR  = 1
	tFILE = 2
	tDEV  = 3

	consoleMajor = 1
	pipeSize     = 512
)

type fileKind int

const (
	fdPipe fileKind = iota + 1
	fdInode
	fdDevice
)

// File is xv6's struct file. It is shared (ref-counted) between file
// descriptors created by dup() and fork(), so they also share the offset.
type File struct {
	kind     fileKind
	ref      int // only touched by the ptrace thread
	readable bool
	writable bool

	pipe *Pipe // fdPipe

	host  *os.File // fdInode / fdDevice: the backing host file
	isDir bool
	dir   []byte // directory contents in xv6 dirent format
	off   int    // read offset into dir
	major int    // fdDevice
}

func (f *File) dup() *File {
	f.ref++
	return f
}

func (f *File) close() {
	f.ref--
	if f.pipe != nil {
		// Wake anyone blocked on the pipe so they notice killed
		// processes, even if this was not the last reference.
		f.pipe.wakeup()
	}
	if f.ref > 0 {
		return
	}
	switch f.kind {
	case fdPipe:
		f.pipe.close(f.writable)
	case fdInode, fdDevice:
		f.host.Close()
	}
}

// Pipe is xv6's struct pipe: a 512-byte ring buffer. Readers and writers
// may block, so pipe operations run on their own goroutines (see
// Kernel.async) and synchronize with a condition variable, the moral
// equivalent of xv6's sleep()/wakeup() on &pi->nread / &pi->nwrite.
type Pipe struct {
	mu        sync.Mutex
	cond      *sync.Cond
	data      [pipeSize]byte
	nread     uint32
	nwrite    uint32
	readopen  bool
	writeopen bool
}

func newPipe() (rf, wf *File) {
	pi := &Pipe{readopen: true, writeopen: true}
	pi.cond = sync.NewCond(&pi.mu)
	rf = &File{kind: fdPipe, ref: 1, readable: true, pipe: pi}
	wf = &File{kind: fdPipe, ref: 1, writable: true, pipe: pi}
	return
}

func (pi *Pipe) wakeup() {
	pi.mu.Lock()
	pi.cond.Broadcast()
	pi.mu.Unlock()
}

func (pi *Pipe) close(writable bool) {
	pi.mu.Lock()
	if writable {
		pi.writeopen = false
	} else {
		pi.readopen = false
	}
	pi.cond.Broadcast()
	pi.mu.Unlock()
}

// write is pipewrite(). It returns the number of bytes written or -1.
func (pi *Pipe) write(killed *atomicBool, buf []byte) int64 {
	pi.mu.Lock()
	defer pi.mu.Unlock()
	for i := range buf {
		for pi.nwrite == pi.nread+pipeSize { // pipe is full
			if !pi.readopen || killed.Load() {
				return -1
			}
			pi.cond.Broadcast()
			pi.cond.Wait()
		}
		if !pi.readopen || killed.Load() {
			return -1
		}
		pi.data[pi.nwrite%pipeSize] = buf[i]
		pi.nwrite++
	}
	pi.cond.Broadcast()
	return int64(len(buf))
}

// read is piperead(). It returns the bytes read, or nil and -1.
func (pi *Pipe) read(killed *atomicBool, n int) ([]byte, int64) {
	pi.mu.Lock()
	defer pi.mu.Unlock()
	for pi.nread == pi.nwrite && pi.writeopen { // pipe is empty
		if killed.Load() {
			return nil, -1
		}
		pi.cond.Wait()
	}
	var out []byte
	for len(out) < n && pi.nread != pi.nwrite {
		out = append(out, pi.data[pi.nread%pipeSize])
		pi.nread++
	}
	pi.cond.Broadcast()
	return out, int64(len(out))
}

// Console is device major 1. It is backed by the sentry's stdin/stdout;
// the host terminal already provides line editing.
type Console struct {
	in, out *os.File
	tty     bool // stdin is a terminal

	mu   sync.Mutex
	eofs int
}

// read blocks, so it is always called on its own goroutine.
func (c *Console) read(n int) ([]byte, int64) {
	buf := make([]byte, n)
	m, err := c.in.Read(buf)
	if m > 0 {
		return buf[:m], int64(m)
	}
	if err == io.EOF || m == 0 {
		c.mu.Lock()
		c.eofs++
		c.mu.Unlock()
		return nil, 0
	}
	return nil, -1
}

// exhausted reports whether non-interactive input has been used up: the
// first end-of-file goes to the guest (sh exits), the second means init's
// fresh sh found nothing more to do either, so the sentry can shut down.
func (c *Console) exhausted() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.tty && c.eofs >= 2
}

func (c *Console) write(b []byte) int64 {
	n, _ := c.out.Write(b)
	return int64(n)
}
