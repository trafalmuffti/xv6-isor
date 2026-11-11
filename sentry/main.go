// Command xv6-isor is a tiny educational user-mode kernel in the spirit of
// gVisor. It runs programs written for the xv6 system call interface as
// ordinary Linux processes, intercepts every system call they make with
// ptrace(2), and implements those calls itself on top of a host directory
// that becomes the guest's root ("/").
//
// Usage:
//
//	xv6-isor [-strace] [-init /init] <root-directory>
package main

import (
	"flag"
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

func main() {
	strace := flag.Bool("strace", false, "log every guest system call to stderr")
	initPath := flag.String("init", "/init", "guest path of the first program to run")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: %s [flags] <root-directory>\n\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "Runs xv6 programs from <root-directory>, which the guest sees as \"/\".\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(2)
	}

	fs, err := newFS(flag.Arg(0))
	if err != nil {
		fatalf("root directory %s: %v", flag.Arg(0), err)
	}
	console := &Console{in: os.Stdin, out: os.Stdout, tty: isatty(0)}
	k := newKernel(fs, console, *strace)
	k.Run(*initPath)
}

func isatty(fd int) bool {
	var t syscall.Termios
	_, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), syscall.TCGETS, uintptr(unsafe.Pointer(&t)))
	return e == 0
}
