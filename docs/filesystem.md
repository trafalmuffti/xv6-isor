# The filesystem

xv6 has its own on-disk filesystem: inodes, blocks, a log, a block cache
(`fs.c` and friends). xv6-isor throws all of that away and exposes **a
host directory as the guest's root (`/`)**, much like gVisor's gofer. This
document explains how paths are resolved and confined, how directories and
device files are faked so unmodified programs work, and how this compares
to `chroot`, `pivot_root`, mount namespaces and gVisor.

The theme: the sentry is a **reference monitor for the filesystem**. Every
guest path flows through a few functions in `sentry/fs.go`, which is what
makes the confinement (and its weaknesses) easy to read.

Contents:

1. [One directory becomes a filesystem](#one-directory-becomes-a-filesystem)
2. [Resolving a path, safely](#resolving-a-path-safely)
3. [Directories: faking xv6's on-disk format](#directories-faking-xv6s-on-disk-format)
4. [Device files without privilege](#device-files-without-privilege)
5. [exec: the path that must not change](#exec-the-path-that-must-not-change)
6. [Comparison: chroot, pivot_root, namespaces, gVisor](#comparison-chroot-pivot_root-namespaces-gvisor)

---

## One directory becomes a filesystem

You run `./xv6-isor <dir>`, and the guest sees `<dir>` as `/`. At startup
`newFS` (`sentry/fs.go`) opens that directory with `O_PATH` and keeps the
descriptor (`rootFd`) and its canonical path (`rootPath`). Every later
lookup is relative to `rootFd`.

There are no inodes, logs or block caches in the sentry. A guest file *is*
a host file; a guest directory *is* a host directory. The sentry's job is
to make host files look enough like xv6 files that xv6's programs run
unchanged, and to make sure the guest can never name a file outside the
root.

The guest's current directory is just a string in the sentry
(`Proc.cwd`), an absolute guest path. `chdir` (`sentry/sysfile.go:225`)
checks the target is a directory and stores it; `fork` copies it to the
child (see [processes.md](processes.md)).

---

## Resolving a path, safely

This is the heart of the filesystem and of the sandbox. A guest path is
turned into a host file in two steps.

**1. Make it absolute.** `abspath(cwd, p)` (`sentry/fs.go:69`) joins the
path with the guest's cwd if it's relative, then `path.Clean`s it. So the
guest always hands the next step an absolute, tidy guest path.

**2. Resolve it inside the root.** `openat2` (`sentry/fs.go:77`) calls the
Linux `openat2(2)` system call relative to `rootFd`, with:

```
RESOLVE_IN_ROOT | RESOLVE_NO_MAGICLINKS
```

- **`RESOLVE_IN_ROOT`** makes the kernel treat `rootFd` as `/` for this
  resolution: `..` at the root stays at the root, and absolute symlinks
  are interpreted relative to the root. A path simply cannot escape.
- **`RESOLVE_NO_MAGICLINKS`** blocks the "magic" symlinks in `/proc`
  (like `/proc/self/fd/3`), which would otherwise jump to an arbitrary
  open file.

This one kernel feature replaces all of xv6's `namei` path-walking logic,
and does it more safely than a hand-written check could, because the
kernel enforces it atomically against concurrent renames. (`openat2`
even returns `EAGAIN` when a concurrent rename could have let a resolution
escape; the sentry retries.)

`parent` (`sentry/fs.go:100`) is the same idea for operations that work on
a name within a directory (`mkdir`, `unlink`, `link`, creating a file):
it opens the parent directory inside the root and returns it plus the
final component, like xv6's `nameiparent`. The actual operation then uses
`mkdirat`, `unlinkat`, `linkat` relative to that directory fd, so the
check and the operation refer to the same directory.

Deleting works like xv6: `unlink` (`sentry/fs.go:292`) passes
`AT_REMOVEDIR` for directories, which fails unless the directory is empty,
matching xv6's `isdirempty`.

See [breakout.md](breakout.md) for what this confinement does and doesn't
guarantee, in particular that files *inside* the root are still opened
with the sentry's own identity.

---

## Directories: faking xv6's on-disk format

xv6 programs read directories by `read`ing raw `struct dirent` records off
the directory file:

```c
struct dirent { ushort inum; char name[DIRSIZ]; };  // DIRSIZ = 14
```

`ls` (`user/ls.c`) does exactly this. A host directory isn't a stream of
these records, so when a guest opens a directory, `dirents`
(`sentry/fs.go:212`) builds that format on the fly:

- it emits `.` and `..` with real inode numbers (from `fstatat` on the
  directory and its parent)
- for each host entry it writes a 2-byte inode number (truncated to 16
  bits) and the name padded to 14 bytes, exactly xv6's layout

The result is cached on the open `File`, and `read` serves slices of it
(see [the File section in syscall-path](syscall-path.md)). So unmodified
`ls` sees what it expects. The Linux equivalent a native program would use
is `getdents64(2)`; xv6-isor instead presents xv6's much simpler on-disk
record, because that's what the guest code parses.

Inode numbers are real host inode numbers (truncated), which is a minor
information leak noted in [breakout.md](breakout.md).

---

## Device files without privilege

xv6 has device files; the console is major 1, created by `init` with
`mknod("console", 1, 1)`. On the host, creating a real device node needs
privilege a normal user doesn't have. So xv6-isor fakes them:

- `mknod` (`sentry/fs.go:257`) creates an ordinary regular file whose
  contents are a magic header plus the numbers, e.g.
  `xv6-isor-device 1 1\n` (`devMagic`, `sentry/fs.go:42`).
- When any file is opened, `readDevice` (`sentry/fs.go:116`) peeks at the
  first bytes; if it sees the magic header, the file is treated as a
  device with that major number instead of a regular file.
- Major 1 is wired to the console (the sentry's stdin/stdout); other
  majors are accepted but have no backing.

This is a teaching compromise, not how Linux works. On Linux a device node
is an inode with a type (`S_IFCHR`/`S_IFBLK`) and a `dev_t` major/minor
that indexes a driver table; `mknod(2)` needs `CAP_MKNOD`. The magic-file
trick lets an unprivileged sentry support `mknod("console", ...)` without
any of that. `fs.open` otherwise rejects real host device nodes, FIFOs
and sockets (`sentry/fs.go`), so the host's own `/dev` can't leak in as a
guest device.

---

## exec: the path that must not change

`exec` is where filesystem confinement and process creation meet.
`execPath` (`sentry/fs.go:352`):

1. resolves the guest path inside the root (`openat2` as above)
2. requires a regular file, and reads back its canonical host path with
   `readlink` on `/proc/self/fd/N`, double-checking it's within
   `rootPath`
3. requires a **static x86-64 ELF** (`checkELF`): right class and machine,
   and no `PT_INTERP`, so no dynamic linker and no `#!` interpreter from
   the host can be pulled in

The host `execve` then loads it (see [processes.md](processes.md)). Why
all the checks: a dynamic binary would make the host load its *dynamic
linker*, and a `#!` script would run a host interpreter, both outside the
guest's world. The check-then-use gap here is the one real filesystem
TOCTOU in xv6-isor, analyzed in [breakout.md](breakout.md).

---

## Comparison: chroot, pivot_root, namespaces, gVisor

Confining a process to a subtree is an old problem with several Linux
answers:

| mechanism | what it does | escapes / limits |
|---|---|---|
| **`chroot(2)`** | changes a process's idea of `/` to a subtree | a process with `CAP_SYS_CHROOT` can often escape (chroot again, `..` from an fd opened before); doesn't affect mounts or other namespaces |
| **`openat2(RESOLVE_IN_ROOT)`** (xv6-isor) | confines *one resolution* to a root fd | per-call, so it can't be forgotten globally; but only guards paths, not fds the sentry already holds |
| **`pivot_root(2)`** | swaps the root mount for a new one, inside a mount namespace | the proper container primitive; needs a mount namespace |
| **mount namespaces** | give a process its own set of mounts | the basis of real containers; what gVisor's sandbox uses (an *empty* mount namespace) |
| **gVisor gofer** | a separate process owns the container's filesystem and hands fds to the Sentry | the Sentry can't open host files at all (without directfs); the gofer is itself confined |

Where xv6-isor sits:

- It uses **`openat2(RESOLVE_IN_ROOT)`**, a per-resolution guarantee
  enforced by the kernel. This is newer and, for path resolution,
  stronger than `chroot`: it can't be escaped with `..` or symlinks, and
  there's no "escape a chroot via an old fd" trick because each lookup is
  re-confined.
- But it is **only a path guard.** It confines *names*, not the sentry's
  authority. Files inside the root are opened by the sentry as itself, and
  the sentry isn't in a mount namespace, so a procfs under the root is
  reachable (see [breakout.md](breakout.md), gap 1).
- **gVisor goes further** precisely here: an empty mount namespace plus a
  separate gofer process means the Sentry has *no* path to host files
  except the fds the gofer chooses to give it. That's the layer xv6-isor
  leaves out. See [security-models.md](security-models.md).

A faithfulness note: xv6-isor's filesystem is closer to gVisor's *gofer
model* (a host directory presented to the guest) than to xv6's own on-disk
filesystem. If you want to study xv6's actual filesystem (inodes, the
log, crash recovery), read the base xv6 `fs.c`; xv6-isor deliberately
doesn't reimplement it, since the host already provides a filesystem.

---

## Try it yourself

- `./xv6-isor -strace fs`, then `ls`, and watch `open(".")` followed by
  `read`s that return the synthesized dirents.
- `echo hi > f; cat f; mkdir d; cd d; /ls; cd ..; rm d/f d` to exercise
  creation, directories and deletion.
- Try to escape: `cat ../../../etc/passwd`, or make a symlink on the host
  pointing out of `fs/` and read it from the guest. Both are blocked by
  `RESOLVE_IN_ROOT`; `isortest` and `test.sh` check this.
- Exercises:
  1. Fix the exec TOCTOU: open the program once in `execPath` and
     `execveat` that fd, so the name can't be swapped (see
     [breakout.md](breakout.md)).
  2. Add a real `lseek`, then make directory reads restartable instead of
     caching the whole listing.
  3. Run the sentry in its own mount namespace with the root `pivot_root`ed,
     and see which breakout paths from [breakout.md](breakout.md) close.

---

## Sources

- The code: `sentry/fs.go` (`abspath`, `openat2`, `parent`, `dirents`,
  `mknod`/`readDevice`, `execPath`/`checkELF`, `unlink`), `sentry/sysfile.go`
  (`sysOpen`, `sysChdir`, directory `read`), `user/ls.c`, `user/init.c`.
- xv6: `fs.c` (the on-disk filesystem xv6-isor replaces), `sysfile.c`.
- gVisor: `g3doc/architecture_guide/security.md` and
  `g3doc/user_guide/filesystem.md` (the gofer and directfs).
- Manual pages: `openat2(2)` (`RESOLVE_IN_ROOT`, `RESOLVE_NO_MAGICLINKS`),
  `chroot(2)`, `pivot_root(2)`, `mount_namespaces(7)`, `getdents64(2)`,
  `mknod(2)`, `execveat(2)`.
