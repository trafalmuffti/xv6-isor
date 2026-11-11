package main

// The guest file system.
//
// xv6 has its own on-disk file system (fs.c). The sentry instead exposes a
// host directory as the guest's "/", much like gVisor's gofer. Every guest
// path is made absolute (using the guest's cwd) and then resolved by the
// host kernel with openat2(RESOLVE_IN_ROOT) relative to the root
// directory, so ".." and symbolic links can never escape it.

import (
	"bytes"
	"debug/elf"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path"
	"strings"
	"syscall"
	"unsafe"
)

const (
	sysOpenat2        = 437
	oPATH             = 0x200000 // O_PATH
	resolveNoMagic    = 0x02     // RESOLVE_NO_MAGICLINKS
	resolveInRoot     = 0x10     // RESOLVE_IN_ROOT
	atRemoveDir       = 0x200
	atSymlinkNoFollow = 0x100
	atEmptyPath       = 0x1000
)

var (
	errNotFound = errors.New("no such file")
	errExists   = errors.New("file exists")
	errBadType  = errors.New("wrong file type")
)

// Device nodes. A host user generally cannot create real device nodes,
// so mknod() creates a tiny regular file holding this magic header.
const devMagic = "xv6-isor-device "

type FS struct {
	rootFd   int    // O_PATH descriptor of the root directory
	rootPath string // canonical host path of the root
}

func newFS(dir string) (*FS, error) {
	fd, err := syscall.Open(dir, oPATH|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	real, err := os.Readlink(fmt.Sprintf("/proc/self/fd/%d", fd))
	if err != nil {
		return nil, err
	}
	fs := &FS{rootFd: fd, rootPath: real}
	// Make sure the kernel supports openat2 (Linux 5.6+).
	if fd2, err := fs.openat2("/", oPATH, 0); err != nil {
		return nil, fmt.Errorf("openat2: %v (need Linux >= 5.6)", err)
	} else {
		syscall.Close(fd2)
	}
	return fs, nil
}

// abspath turns a guest path into a clean absolute guest path.
func abspath(cwd, p string) string {
	if !strings.HasPrefix(p, "/") {
		p = cwd + "/" + p
	}
	return path.Clean(p)
}

// openat2 resolves an absolute guest path inside the root.
func (fs *FS) openat2(gpath string, flags int, mode uint32) (int, error) {
	how := struct{ flags, mode, resolve uint64 }{
		uint64(flags | syscall.O_CLOEXEC), uint64(mode), resolveInRoot | resolveNoMagic,
	}
	p, err := syscall.BytePtrFromString(gpath)
	if err != nil {
		return -1, err
	}
	for {
		fd, _, e := syscall.Syscall6(sysOpenat2, uintptr(fs.rootFd),
			uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(&how)), unsafe.Sizeof(how), 0, 0)
		if e == syscall.EAGAIN || e == syscall.EINTR {
			continue // RESOLVE_IN_ROOT asks us to retry on concurrent renames
		}
		if e != 0 {
			return -1, e
		}
		return int(fd), nil
	}
}

// parent opens the directory containing gpath and returns it together with
// the final path element (like xv6's nameiparent).
func (fs *FS) parent(gpath string) (int, string, error) {
	dir, name := path.Split(gpath)
	if name == "" || name == "." || name == ".." {
		return -1, "", errBadType
	}
	fd, err := fs.openat2(dir, oPATH|syscall.O_DIRECTORY, 0)
	return fd, name, err
}

// reopen turns an O_PATH descriptor into a real one with the given flags.
func reopen(fd int, flags int) (int, error) {
	return syscall.Open(fmt.Sprintf("/proc/self/fd/%d", fd), flags|syscall.O_CLOEXEC, 0)
}

// readDevice reports whether the regular file fd is a device node made by
// mknod, and if so its major number.
func readDevice(fd int, st *syscall.Stat_t) (int, bool) {
	if st.Size > 64 {
		return 0, false
	}
	rfd, err := reopen(fd, syscall.O_RDONLY)
	if err != nil {
		return 0, false
	}
	defer syscall.Close(rfd)
	buf := make([]byte, 64)
	n, _ := syscall.Read(rfd, buf)
	if n < 0 || !bytes.HasPrefix(buf[:n], []byte(devMagic)) {
		return 0, false
	}
	var major, minor int
	if _, err := fmt.Sscanf(string(buf[len(devMagic):n]), "%d %d", &major, &minor); err != nil {
		return 0, false
	}
	return major, true
}

// open implements the file-system half of sys_open.
func (fs *FS) open(gpath string, omode int) (*File, error) {
	readable := omode&oWRONLY == 0
	writable := omode&(oWRONLY|oRDWR) != 0
	acc := syscall.O_RDONLY
	if readable && writable {
		acc = syscall.O_RDWR
	} else if writable {
		acc = syscall.O_WRONLY
	}

	pfd, err := fs.openat2(gpath, oPATH, 0)
	if err == syscall.ENOENT && omode&oCREATE != 0 {
		// create(path, T_FILE): make a new, empty regular file.
		hfd, err := fs.openat2(gpath, acc|syscall.O_CREAT|syscall.O_EXCL, 0644)
		if err != nil {
			return nil, err
		}
		return &File{kind: fdInode, host: os.NewFile(uintptr(hfd), gpath),
			readable: readable, writable: writable}, nil
	}
	if err != nil {
		return nil, err
	}
	defer syscall.Close(pfd)

	var st syscall.Stat_t
	if err := syscall.Fstat(pfd, &st); err != nil {
		return nil, err
	}
	switch st.Mode & syscall.S_IFMT {
	case syscall.S_IFDIR:
		if omode&oCREATE != 0 || writable {
			return nil, errBadType // xv6: can't open a directory for writing
		}
		dfd, err := reopen(pfd, syscall.O_RDONLY|syscall.O_DIRECTORY)
		if err != nil {
			return nil, err
		}
		host := os.NewFile(uintptr(dfd), gpath)
		ents, err := fs.dirents(gpath, host)
		if err != nil {
			host.Close()
			return nil, err
		}
		return &File{kind: fdInode, host: host, isDir: true, dir: ents, readable: true}, nil

	case syscall.S_IFREG:
		if major, ok := readDevice(pfd, &st); ok {
			dfd, err := syscall.Dup(pfd) // keep an O_PATH fd for fstat
			if err != nil {
				return nil, err
			}
			syscall.CloseOnExec(dfd)
			return &File{kind: fdDevice, major: major, host: os.NewFile(uintptr(dfd), gpath),
				readable: readable, writable: writable}, nil
		}
		flags := acc
		if omode&oTRUNC != 0 {
			flags |= syscall.O_TRUNC
		}
		hfd, err := reopen(pfd, flags)
		if err != nil {
			return nil, err
		}
		return &File{kind: fdInode, host: os.NewFile(uintptr(hfd), gpath),
			readable: readable, writable: writable}, nil
	}
	// Host FIFOs, sockets and devices are not part of the guest's world.
	return nil, errBadType
}

// dirents builds the xv6 on-disk directory format (struct dirent: a
// 2-byte inode number and a 14-byte name) from a host directory, so that
// unmodified programs like ls can read() directories.
func (fs *FS) dirents(gpath string, dir *os.File) ([]byte, error) {
	var out bytes.Buffer
	add := func(ino uint64, name string) {
		var de [2 + dirSiz]byte
		inum := uint16(ino)
		if inum == 0 {
			inum = 1 // 0 marks a free slot in xv6
		}
		binary.LittleEndian.PutUint16(de[:2], inum)
		copy(de[2:], name) // truncated to DIRSIZ, like xv6
		out.Write(de[:])
	}
	var st syscall.Stat_t
	if err := syscall.Fstat(int(dir.Fd()), &st); err != nil {
		return nil, err
	}
	add(st.Ino, ".")
	if pfd, err := fs.openat2(path.Join(gpath, ".."), oPATH, 0); err == nil {
		var pst syscall.Stat_t
		syscall.Fstat(pfd, &pst)
		syscall.Close(pfd)
		add(pst.Ino, "..")
	}
	names, err := dir.Readdirnames(-1)
	if err != nil {
		return nil, err
	}
	for _, name := range names {
		var est syscall.Stat_t
		if fstatat(int(dir.Fd()), name, &est) == nil {
			add(est.Ino, name)
		}
	}
	return out.Bytes(), nil
}

func (fs *FS) mkdir(gpath string) error {
	dfd, name, err := fs.parent(gpath)
	if err != nil {
		return err
	}
	defer syscall.Close(dfd)
	return syscall.Mkdirat(dfd, name, 0755)
}

func (fs *FS) mknod(gpath string, major, minor int) error {
	fd, err := fs.openat2(gpath, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL, 0644)
	if err != nil {
		return err
	}
	defer syscall.Close(fd)
	_, err = syscall.Write(fd, []byte(fmt.Sprintf("%s%d %d\n", devMagic, major, minor)))
	return err
}

func fstatat(dfd int, name string, st *syscall.Stat_t) error {
	p, err := syscall.BytePtrFromString(name)
	if err != nil {
		return err
	}
	_, _, e := syscall.Syscall6(syscall.SYS_NEWFSTATAT, uintptr(dfd),
		uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(st)), atSymlinkNoFollow, 0, 0)
	if e != 0 {
		return e
	}
	return nil
}

func unlinkat(dfd int, name string, flags int) error {
	p, err := syscall.BytePtrFromString(name)
	if err != nil {
		return err
	}
	_, _, e := syscall.Syscall(syscall.SYS_UNLINKAT, uintptr(dfd), uintptr(unsafe.Pointer(p)), uintptr(flags))
	if e != 0 {
		return e
	}
	return nil
}

func (fs *FS) unlink(gpath string) error {
	dfd, name, err := fs.parent(gpath)
	if err != nil {
		return err
	}
	defer syscall.Close(dfd)
	var st syscall.Stat_t
	if err := fstatat(dfd, name, &st); err != nil {
		return err
	}
	flags := 0
	if st.Mode&syscall.S_IFMT == syscall.S_IFDIR {
		flags = atRemoveDir // fails unless empty, like xv6's isdirempty()
	}
	return unlinkat(dfd, name, flags)
}

func (fs *FS) link(oldp, newp string) error {
	odfd, oname, err := fs.parent(oldp)
	if err != nil {
		return err
	}
	defer syscall.Close(odfd)
	var st syscall.Stat_t
	if err := fstatat(odfd, oname, &st); err != nil {
		return err
	}
	if st.Mode&syscall.S_IFMT == syscall.S_IFDIR {
		return errBadType // xv6: no hard links to directories
	}
	ndfd, nname, err := fs.parent(newp)
	if err != nil {
		return err
	}
	defer syscall.Close(ndfd)
	op, _ := syscall.BytePtrFromString(oname)
	np, _ := syscall.BytePtrFromString(nname)
	_, _, e := syscall.Syscall6(syscall.SYS_LINKAT, uintptr(odfd), uintptr(unsafe.Pointer(op)),
		uintptr(ndfd), uintptr(unsafe.Pointer(np)), 0, 0)
	if e != 0 {
		return e
	}
	return nil
}

// isDir is used by chdir.
func (fs *FS) isDir(gpath string) bool {
	fd, err := fs.openat2(gpath, oPATH|syscall.O_DIRECTORY, 0)
	if err != nil {
		return false
	}
	syscall.Close(fd)
	return true
}

// execPath resolves a guest program path to a host path that is safe to
// hand to the host's execve(). The returned path is canonical (contains no
// symbolic links), lies inside the root, and names a static x86-64 ELF
// executable. Guests have no symlink() or rename() system call, so they
// cannot change what the path refers to before execve() runs.
func (fs *FS) execPath(gpath string) (string, error) {
	pfd, err := fs.openat2(gpath, oPATH, 0)
	if err != nil {
		return "", err
	}
	defer syscall.Close(pfd)
	var st syscall.Stat_t
	if err := syscall.Fstat(pfd, &st); err != nil {
		return "", err
	}
	if st.Mode&syscall.S_IFMT != syscall.S_IFREG {
		return "", errBadType
	}
	real, err := os.Readlink(fmt.Sprintf("/proc/self/fd/%d", pfd))
	if err != nil {
		return "", err
	}
	if real != fs.rootPath && !strings.HasPrefix(real, strings.TrimSuffix(fs.rootPath, "/")+"/") {
		return "", errBadType
	}
	rfd, err := reopen(pfd, syscall.O_RDONLY)
	if err != nil {
		return "", err
	}
	f := os.NewFile(uintptr(rfd), real)
	defer f.Close()
	if err := checkELF(f); err != nil {
		return "", err
	}
	return real, nil
}

// checkELF accepts only static x86-64 executables. A dynamically linked
// program would make the host kernel load a host dynamic linker, and a
// "#!" script would run a host interpreter: neither belongs to the guest.
func checkELF(f *os.File) error {
	ef, err := elf.NewFile(f)
	if err != nil {
		return fmt.Errorf("not an ELF executable")
	}
	if ef.Class != elf.ELFCLASS64 || ef.Machine != elf.EM_X86_64 {
		return fmt.Errorf("not an x86-64 executable")
	}
	for _, ph := range ef.Progs {
		if ph.Type == elf.PT_INTERP {
			return fmt.Errorf("dynamically linked executables are not supported")
		}
	}
	return nil
}
