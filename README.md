# xv6-isor

xv6, but in user mode: a small, readable user-mode kernel in Go, written to
teach the ideas behind [gVisor](https://gvisor.dev).

The guest programs are xv6's own user programs (`sh`, `ls`, `cat`, `grep`,
...) built as ordinary static x86-64 Linux executables. They make **xv6
system calls**, which the host kernel never runs. The sentry runs each
guest as a host process, stops it at every system call with `ptrace(2)`,
and carries the call out itself, using a host directory as the guest's
`/`.

```
$ make
$ ./xv6-isor fs
init: starting sh
$ echo hello | wc
1 1 6
$ ls
.              1 566514 320
..             1 566514 320
cat            2 566532 10560
...
console        3 566500 0
```

## Requirements

* Linux on x86-64, kernel 5.6 or newer (for `openat2` and
  `PTRACE_GET_SYSCALL_INFO`)
* Go 1.22 or newer, gcc and make
* ptrace must be allowed. A process can always trace its own children
  unless a seccomp profile or LSM blocks `ptrace` (in some containers you
  may need `--cap-add=SYS_PTRACE` or a relaxed seccomp profile).

## Usage

```
make                     # builds ./xv6-isor and the guest root ./fs
./xv6-isor fs            # boot /init (which starts /sh) with ./fs as "/"
./xv6-isor -strace fs    # same, logging every guest system call
make test                # end-to-end tests (test.sh + user/isortest.c)
```

```
xv6-isor [-strace] [-init /init] <root-directory>
```

The one argument is the host directory that the guest sees as its root.
Any directory works as long as it contains the guest programs (at least
`init` and `sh`; `make` fills `fs/` with all of them). As in xv6 there is
no `PATH`, so outside `/` you run programs as `/ls`, `/cat` and so on.

If standard input is a terminal, the session works like xv6 under QEMU:
Ctrl-D ends the shell and init starts a new one. Press Ctrl-C to stop the
sentry. If standard input is a file or pipe, the sentry exits once the
input runs out.

## How it works

```
 ┌───────────── guest (host processes, traced) ─────────────┐
 │  init        sh        ls ...     static x86-64 ELF,     │
 │                                   xv6 syscall numbers    │
 └──────┬──────────┬──────────┬─────────────────────────────┘
        │ syscall  │          │   every syscall stops the process (ptrace)
 ┌──────▼──────────▼──────────▼─────────────────────────────┐
 │  sentry (xv6-isor, Go)                                   │
 │   process table · fd tables · pipes · console · cwd      │
 │   syscall table (the 21 xv6 calls)                       │
 └──────┬───────────────────────────────────────────────────┘
        │ only system calls the sentry builds itself
 ┌──────▼───────────────────────────────────────────────────┐
 │  host Linux kernel     root dir via openat2(IN_ROOT)     │
 └──────────────────────────────────────────────────────────┘
```

gVisor intercepts an application's system calls (with ptrace, KVM, or
seccomp traps) and sends them to the **sentry**, a kernel written in Go
that runs as an ordinary user process. Only the sentry talks to the host.
xv6-isor keeps that design and shrinks the system call table to xv6's 21
calls. Those few calls are enough to run a real Unix shell with pipes,
redirection and background jobs.

### The ptrace loop (`sentry/kernel.go`)

`/init` is started with `PTRACE_TRACEME`. With `PTRACE_O_TRACEFORK` and
related options, every descendant is traced automatically. Each guest is
resumed with `PTRACE_SYSCALL`, so the host stops it at the entry and at the
exit of every system call. At the entry stop the sentry reads the
registers. `%rax` holds the **xv6** system call number (see
`user/usys.S`) and `%rdi`, `%rsi`, `%rdx` hold the arguments. The sentry
then handles the call in one of three ways:

| strategy | how | used for |
|---|---|---|
| **emulate** | set `orig_rax = -1` so the host skips the call, then write the result into `%rax` at the exit stop | `read` `write` `open` `close` `dup` `pipe` `fstat` `mkdir` `mknod` `link` `unlink` `chdir` `getpid` `uptime` `kill` |
| **block** | same as emulate, but leave the guest stopped until a goroutine finishes the work (pipe data, console input, a timer, a child exiting) | `read`/`write` on pipes and the console, `wait`, `sleep` |
| **rewrite** | replace the call with a host call that only affects the calling process, and fix the result at the exit stop | `fork` → `clone`, `exec` → `execve`, `sbrk` → `brk`, `exit` → `exit_group` |

All ptrace requests must come from the tracing thread. So a single
goroutine, locked to its OS thread, owns all kernel state. A helper
goroutine blocks in `wait4()` and forwards stop events, and blocking work
sends a closure back over a channel. This is the user-space version of
xv6's `sleep()`/`wakeup()`.

### Mapping xv6 to the host

| xv6 call | sentry implementation |
|---|---|
| `fork()` | Rewritten to `clone(CLONE_PARENT\|SIGCHLD)`, so the host copies the address space. `PTRACE_EVENT_FORK` gives the child's host pid, and the sentry copies the parent's fd table and cwd. `CLONE_PARENT` makes the sentry the host parent of every guest, so it can reap them all. Guest pids are virtual (1, 2, 3, ...), like gVisor's. |
| `exit()` | Rewritten to `exit_group`. When `wait4` reports the death, the sentry closes the files, gives the children to init and wakes a parent that is waiting. |
| `wait()` | Emulated. Blocks until a child becomes a zombie. |
| `exec(path, argv)` | The guest path is resolved inside the root and checked to be a static x86-64 ELF. The host path is written into the guest's stack (below the red zone) and the call is rewritten to `execve(hostpath, argv, NULL)`. |
| `sbrk(n)` | Rewritten to `brk(old+n)`. The first time, the sentry doesn't know `old` yet. It runs `brk(0)` and then **restarts** the guest's `sbrk` by moving `%rip` back over the 2-byte `syscall` instruction, which is how the kernel restarts interrupted calls. |
| `read`/`write` | Files: the sentry's own host fd. Pipes: a 512-byte ring buffer in the sentry (xv6's `pipe.c`). Console: the sentry's stdin/stdout. Data moves with `process_vm_readv`/`process_vm_writev`. |
| `open` | `openat2(rootfd, path, RESOLVE_IN_ROOT)`, so `..` and symlinks (even absolute ones) cannot leave the root. Directories are read in xv6's `struct dirent` format, so `ls` works unchanged. |
| `mknod` | Device nodes are small regular files holding a magic header (`xv6-isor-device 1 1`), because unprivileged users cannot create real ones. Major 1 is the console. |
| `fstat` | Fills in xv6's `struct stat` (type, dev, ino, nlink, size). |
| `mkdir` `unlink` `link` | `mkdirat` / `unlinkat` / `linkat` relative to a parent directory that was opened inside the root. |
| `chdir` | The cwd is a guest path stored in the sentry. |
| `kill(pid)` | `SIGKILL` to the host process, which also wakes it if it's blocked in a pipe. |
| `sleep` / `uptime` | Timers in the sentry. One tick is 10 ms. |
| `getpid` `dup` `close` `pipe` | Pure sentry bookkeeping. |

### Why the guest can't get out

* Every system call stops the guest. Any number outside xv6's table gets
  `-1` and the message `unknown sys call N`, exactly as in xv6. So the
  host never runs a call that the sentry did not write.
* `int $0x80` goes through the 32-bit syscall table, where the sentry's
  rewrites would mean something else. `PTRACE_GET_SYSCALL_INFO` reports
  the ABI used, and the process is killed.
* The guest holds no host file descriptors. Its fd table lives in the
  sentry.
* Paths are resolved by the host kernel with `RESOLVE_IN_ROOT`. The guest
  has no `symlink` or `rename` call, so it cannot plant a link between the
  sentry's check and `execve`.
* `exec` accepts only static x86-64 ELF files. A dynamic binary would load
  a host dynamic linker, and a `#!` script would run a host interpreter.
* Faults (`SIGSEGV` and friends) kill the faulting process with a message
  like xv6's `usertrap()`. Other host signals are dropped, because xv6
  has no signals.

This is a teaching tool, not a security boundary. Real ptrace sandboxes
also have to deal with multi-threaded tracees changing syscall arguments
after they are checked (xv6 has no threads), the vDSO, resource limits,
and much more. gVisor's ptrace platform also keeps the sentry's state out
of the application's address space entirely, using stub processes.

## Layout

```
sentry/            the user-mode kernel (Go)
  main.go          command line
  kernel.go        process table, ptrace event loop, fork/exit/wait
  syscall.go       syscall table + process syscalls (xv6 sysproc.c)
  sysfile.go       file syscalls (xv6 sysfile.c)
  file.go          files, pipes, console (xv6 file.c, pipe.c, console.c)
  fs.go            root directory confinement, dirents, exec checks
  mem.go           copyin/copyout via process_vm_readv/writev
  ptrace.go        ptrace helpers missing from package syscall
user/              guest programs, from MIT xv6 (x86 version)
  start.S usys.S   entry point and x86-64 syscall stubs (new)
  isortest.c       tests for every system call (new)
  sleep.c          tiny sleep(1) for trying kill (new)
  *.c *.h          xv6 user programs and ulib (changes marked "isor:")
test.sh            end-to-end tests
```

Changes to the xv6 sources are small and marked with `isor:` comments:

* `usys.S` uses `syscall` instead of `int $64`.
* `start.S` replaces the argument setup that xv6's `exec` did.
* `printf.c` uses `<stdarg.h>` instead of walking a 32-bit stack.
* `ulib.c` no longer needs `x86.h`.
* `grep.c` keeps a partial line across pipe reads. The original dropped
  it, which is rare on single-CPU xv6 but common when the reader and
  writer really run in parallel.

## License

MIT. The programs in `user/` come from
[xv6](https://github.com/mit-pdos/xv6-public), © 2006-2018 Frans
Kaashoek, Robert Morris, Russ Cox, MIT, under the MIT license (see
`user/LICENSE.xv6`).
