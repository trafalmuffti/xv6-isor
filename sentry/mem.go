package main

// Copying data between the sentry and a guest's address space.
//
// In xv6 the kernel shares the address space with the process (copyin /
// copyout walk the page table). The sentry lives in a different host
// process, so it uses process_vm_readv/process_vm_writev, which the host
// kernel allows because we are the tracer of the guest.

import (
	"bytes"
	"errors"
	"syscall"
	"unsafe"
)

const (
	pageSize           = 4096
	sysProcessVMReadv  = 310
	sysProcessVMWritev = 311
)

var errFault = errors.New("bad address")

type iovec struct {
	base uintptr
	len  uint64
}

func vmTransfer(nr uintptr, pid int, addr uint64, buf []byte) error {
	if len(buf) == 0 {
		return nil
	}
	local := iovec{uintptr(unsafe.Pointer(&buf[0])), uint64(len(buf))}
	remote := iovec{uintptr(addr), uint64(len(buf))}
	n, _, e := syscall.Syscall6(nr, uintptr(pid),
		uintptr(unsafe.Pointer(&local)), 1, uintptr(unsafe.Pointer(&remote)), 1, 0)
	if e != 0 || int(n) != len(buf) {
		return errFault
	}
	return nil
}

// copyIn reads n bytes at guest address addr.
func copyIn(p *Proc, addr uint64, n int) ([]byte, error) {
	buf := make([]byte, n)
	return buf, vmTransfer(sysProcessVMReadv, p.hpid, addr, buf)
}

// copyOut writes buf to guest address addr.
func copyOut(p *Proc, addr uint64, buf []byte) error {
	return vmTransfer(sysProcessVMWritev, p.hpid, addr, buf)
}

// copyInStr reads a NUL-terminated string of at most max bytes
// (like xv6's fetchstr). It never reads across into a page it does not
// need, so a string at the very end of a mapping still works.
func copyInStr(p *Proc, addr uint64, max int) (string, error) {
	var out []byte
	for len(out) < max {
		chunk := pageSize - int(addr%pageSize)
		if chunk > max-len(out) {
			chunk = max - len(out)
		}
		b, err := copyIn(p, addr, chunk)
		if err != nil {
			return "", err
		}
		if i := bytes.IndexByte(b, 0); i >= 0 {
			return string(append(out, b[:i]...)), nil
		}
		out = append(out, b...)
		addr += uint64(chunk)
	}
	return "", errFault // too long
}

func copyInU64(p *Proc, addr uint64) (uint64, error) {
	b, err := copyIn(p, addr, 8)
	if err != nil {
		return 0, err
	}
	return *(*uint64)(unsafe.Pointer(&b[0])), nil
}
