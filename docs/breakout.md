# Breaking out of the sandbox

xv6-isor is a teaching tool, not a security boundary. This document maps
its security model honestly: which escapes the design stops, which it
doesn't, and which Linux feature closes each remaining gap. Every claim
points at the line of code responsible, so you can read the boundary for
yourself.

"Breakout" means guest code getting the host to do something the sentry
didn't intend: touching files outside the root, running host code, or
controlling the host kernel or the sentry.

> This is an analysis of xv6-isor's own code, for learning. It describes
> weaknesses at the level of "which mechanism is missing", not a recipe.
> xv6-isor runs with your full user rights (see
> [Threat model](#the-threat-model)); don't run untrusted programs under
> it.

Contents:

1. [The trust boundary](#the-trust-boundary)
2. [The threat model](#the-threat-model)
3. [What the design stops](#what-the-design-stops)
4. [What it doesn't stop](#what-it-doesnt-stop)
5. [The one-line summary table](#summary)
6. [How Linux and gVisor close each gap](#how-linux-and-gvisor-close-each-gap)

---

## The trust boundary

```
 guest code ──syscall──▶ [ptrace stop] ──▶ sentry decides ──▶ host kernel
     │                                                            ▲
     └──── page faults, ELF loading, CPU instructions ───────────┘
                         (the sentry never sees these)
```

The sentry mediates only the top path: system calls. Everything a guest
does *without* a system call, page faults on its own memory, the host
kernel loading its ELF file, raw CPU instructions, goes straight to the
real Linux kernel. That split is the key to reading the boundary: the
sentry can only defend what passes through a syscall stop.

See [syscall-path.md](syscall-path.md) for how that interception works.

---

## The threat model

Who the sentry runs as decides how much a breakout is worth:

- **xv6-isor runs with all the rights of whoever started it.** No
  seccomp filter, no namespaces, no dropped privileges, no separate user.
  `PTRACE_O_EXITKILL` (`sentry/ptrace.go:22`) kills guests if the sentry
  dies, but nothing confines the sentry itself.
- **gVisor assumes the opposite.** Its Sentry runs as `nobody` in a user
  namespace, under a seccomp filter, in an empty mount namespace (see
  [security-models.md](security-models.md)). gVisor treats "the Sentry is
  compromised" as an expected case and still contains it.

So in xv6-isor a breakout is a full compromise of your account. This is
fine for its purpose, running programs you wrote and compiled yourself,
and it's exactly the layer the project leaves as an exercise.

---

## What the design stops

These are the standard ptrace-sandbox escapes. The design closes each
one, and `user/isortest.c` tests several.

### Calling a host system call directly

The syscall table (`sentry/syscall.go`) is an allow-list keyed on the
full number in `orig_rax`. Anything not in it gets `-1` and
`unknown sys call N`, so the host runs nothing (see
[syscall-path.md](syscall-path.md), step 6). Linux's `getpid` is number
39; to the sentry that's unknown. `isortest` checks this
("Linux system calls are rejected").

### Using the 32-bit syscall table

`int $0x80` enters through the 32-bit table, where number 12 is `brk`,
not `sbrk`, and the sentry's rewrites would mean the wrong thing.
`PTRACE_GET_SYSCALL_INFO` reports the ABI, and a guest that uses the
32-bit entry is killed (`sentry/syscall.go:137`). `isortest` checks this
too.

### Escaping the tracer

The only `clone` the host runs is the one the sentry builds in `sysFork`,
with every argument register set by the sentry (see
[processes.md](processes.md)). A guest can't pass `CLONE_UNTRACED` or
`CLONE_VM`, because it never supplies the flags.

### Changing arguments after they're checked

The classic ptrace TOCTOU: a second thread rewrites a path after the
check. xv6 has no threads, and the sentry copies every argument once with
`copyIn` (`sentry/mem.go`) and works on its own copy. So for the *data*
arguments of emulated calls this doesn't apply. (Exec is different, see
below.)

### Climbing out with `..` or symlinks

Every path is resolved by the host kernel with
`openat2(RESOLVE_IN_ROOT | RESOLVE_NO_MAGICLINKS)`
(`sentry/fs.go:77`). `..` at the root stays at the root, and symlinks,
absolute or relative, can't point outside it. Guests also have no
`symlink` or `rename` call. `isortest` checks "symlinks can't leave the
root", and the test harness plants an escaping symlink.

### Killing the sentry to free the guests

`PTRACE_O_EXITKILL` (`sentry/ptrace.go:22`) makes the host kill every
guest if the sentry exits, including on a Go panic. It fails closed.

---

## What it doesn't stop

Most serious first. Each one names the Linux mechanism that would fix it.

### 1. Confused deputy: the sentry opens files as itself

Every `open` is done by the sentry, with the sentry's identity
(`sentry/fs.go:138`). `openat2` keeps paths inside the root, but *what
lives in the root* is opened with the sentry's full rights. Two
consequences, both only if the root contains a procfs/sysfs or you run
`./xv6-isor /`:

- **`/proc/self` leaks the sentry.** `RESOLVE_NO_MAGICLINKS` blocks
  `/proc/self/fd/*` style magic links, but `/proc/self` itself is an
  ordinary symlink to the sentry's own pid. So `cat /proc/self/status`
  describes the *sentry*. `isortest`'s "host files are invisible" passes
  only because `fs/` has no `/proc`.
- **`/proc/sys` and `/sys` are writable if the sentry runs as root.**
  `fs.open` rejects non-regular files (`sentry/fs.go:184`), which blocks
  real device nodes, but procfs/sysfs control files report as regular. If
  the sentry runs as root, writing one such kernel-settings file is a
  host compromise.
- **`/proc/self/mem` isn't reachable** only because xv6 has no `lseek`
  and offset 0 is unmapped.

The fix is to not open guest files with the sentry's authority. gVisor
uses a separate, confined **gofer** process and an empty mount namespace
for exactly this (see [filesystem.md](filesystem.md)).

### 2. Exec check-vs-use race (TOCTOU)

`sysExec` resolves the guest path, checks it's a static x86-64 ELF
(`execPath`/`checkELF`, `sentry/fs.go:352`), then rewrites the call to
`execve(hostpath, ...)` (`sentry/syscall.go:345`). The host kernel opens
the path **again** by name. Between the check and that second open,
another guest process, running concurrently, can `unlink` the checked
file and `link` a different file to the name, or overwrite it (regular
files reopen with `O_TRUNC`, `sentry/fs.go:196`). The host then loads
whatever is there: a `#!` script runs a host interpreter, a dynamic ELF
pulls in the host's `ld.so`.

How bad: the loaded host code is still traced, so its Linux syscalls are
read as xv6 numbers and still go through the sentry. It escapes the
*filesystem view* for code loading, not the syscall sandbox. A setuid
binary gains nothing, because it's traced.

The fix is check-by-fd: open once, verify the fd, and `execveat(fd, "",
AT_EMPTY_PATH)` that same fd, so name and bytes can't diverge.

### 3. The host kernel is still the attack surface

The big one in principle, shared by every ptrace sandbox, gVisor's old
ptrace platform included. Guest code reaches real kernel code through:

- **the ELF loader** (`binfmt_elf` parses everything past the header that
  `checkELF` looks at; a guest can overwrite any program in the root)
- **page faults and `brk`** on its own memory
- **syscall entry itself**, even for calls the sentry cancels
- **CPU instructions and side channels** (`rdtsc`, `cpuid`,
  Spectre/MDS-class leaks)

Any kernel bug on those paths is a full escape, invisible to the sentry.
gVisor narrows this by moving memory management into the Sentry
(systrap/KVM) and wrapping the Sentry in seccomp + namespaces, so even a
kernel-facing bug meets a second wall.

### 4. A sentry bug is a full compromise

The sentry has no seccomp filter and your full rights (see
[Threat model](#the-threat-model)). Go rules out memory corruption, but a
*logic* bug, a path that skips `abspath`/`openat2`, a host fd leaked to a
guest, is direct host access. gVisor's engineering rules (no cgo, all
`unsafe` isolated, seccomp on the Sentry) exist for this.

### 5. Information leaks

- **The host path of the root.** `sysExec` writes the canonical host path
  into the guest's stack below the red zone *before* `execve` runs
  (`sentry/syscall.go:353`), and `execPath` never checks the execute bit.
  So a guest can copy an ELF to a new file (created non-executable),
  `exec` it, get `EACCES`, and read the absolute host path, e.g.
  `/home/you/xv6-isor/fs`, from just below `%rsp`.
- **Host inode numbers**, in dirents and `fstat`.
- **Timing**, via `uptime`, `rdtsc` and the vDSO.

A fix for the path leak is to write the host path somewhere the guest
can't read after a failed exec, or to use `execveat` with a pre-opened fd
so no host path crosses into guest memory.

### 6. Terminal escape sequences

Console writes go straight to the sentry's stdout
(`k.console.write`). A guest can emit terminal escape sequences (OSC 52
clipboard writes, title changes, and worse on some emulators). That's an
escape into your terminal, not the host OS, but it's a real channel. The
fix is to sanitize or restrict console output.

### 7. Resource exhaustion

Not a breakout, but a host impact. `sbrk` has no ceiling beyond the
host's (`sysSbrk`), a single `read`/`write` is capped at 1 MB but totals
aren't (`maxIO`, `sentry/sysfile.go:11`), pipes and CPU are unbounded,
and `NPROC` (64) counts zombies. Linux's answer is **cgroups** and
`setrlimit`.

---

## Summary

| # | path | needs | result | missing mechanism |
|---|---|---|---|---|
| 1 | confused deputy via `/proc`,`/sys` | a root dir with them, or `/` | secrets leak; host root if the sentry is root | confined gofer + mount namespace |
| 2 | exec check-vs-use race | two processes + winning a race | host binaries run (still syscall-confined) | `execveat` by fd |
| 3 | host kernel bug | a kernel 0-day | full host | seccomp on the sentry; VM-based platform |
| 4 | sentry logic bug | a bug | full host as the sentry's user | seccomp + namespaces on the sentry |
| 5 | leaks: host path, inodes, time | nothing | fingerprinting | exec by fd; virtualized clocks/inodes |
| 6 | terminal escapes | nothing | your terminal | output sanitizing |
| 7 | resource exhaustion | nothing | host DoS | cgroups, rlimits |

The syscall boundary itself is solid: an allow-list, fixed `clone` flags,
an ABI check, and arguments copied once. The gaps are everything *around*
it, what the sentry does with its own authority (1, 4), what the host
kernel does on the guest's behalf (2, 3), and side channels (5-7). That
is precisely why gVisor is more than a syscall filter.

---

## How Linux and gVisor close each gap

Turning this list into fixes is the natural next project. Roughly from
smallest to largest:

| gap | Linux mechanism | sketch for xv6-isor |
|---|---|---|
| 2, 5 exec race & path leak | `execveat(2)` with `AT_EMPTY_PATH` | open the program once, verify the fd, exec *that fd*; never write a host path into guest memory |
| 6 terminal | output filtering | strip control bytes in `Console.write` |
| 7 resources | cgroups v2, `setrlimit(2)` | put each guest in a cgroup; cap total memory and process count |
| 1 confused deputy | separate helper + mount namespace | move file opens into a small, confined helper like gVisor's gofer |
| 3, 4 kernel & sentry bugs | `seccomp(2)`, user/pid/mount namespaces | install a seccomp allow-list on the sentry listing only the host calls it makes (`ptrace`, `wait4`, `process_vm_readv/writev`, `openat2`, `read`, `write`, `clone`, `execve`, `brk`, ...); run it in namespaces as an unprivileged user |

The last row is the most faithful imitation of gVisor and the most
instructive: a seccomp filter on the sentry would, by itself, downgrade
gaps 3 and 4 from "full host" to "contained", because a compromised
sentry could no longer make arbitrary host system calls. See
[security-models.md](security-models.md) for how gVisor layers all of
these.

---

## Sources

- The code: `sentry/fs.go` (`openat2`, `open`, `execPath`, `checkELF`),
  `sentry/syscall.go` (`sysExec`, `sysSbrk`, the 32-bit check),
  `sentry/ptrace.go` (ptrace options), `sentry/sysfile.go` (`maxIO`),
  `user/isortest.c` (the sandbox tests).
- gVisor: `g3doc/architecture_guide/security.md` (threat model,
  defense-in-depth, "Is this just a ptrace sandbox?").
- Manual pages: `openat2(2)` (`RESOLVE_IN_ROOT`, `RESOLVE_NO_MAGICLINKS`),
  `execveat(2)`, `ptrace(2)` (`PTRACE_O_EXITKILL`,
  `PTRACE_GET_SYSCALL_INFO`), `seccomp(2)`, `user_namespaces(7)`,
  `cgroups(7)`.
