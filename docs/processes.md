# Processes: fork, exec, exit, wait

A Unix process is created by `fork`, replaced by `exec`, ended by `exit`,
and collected by its parent with `wait`. This document shows how xv6-isor
implements that cycle, how it maps each call onto the host, and how the
same ideas appear in xv6, Linux and gVisor.

The theme: xv6-isor keeps xv6's **process table and semantics** in the
sentry, but lets the **host kernel own the real processes and address
spaces**. Each of the four calls is a different way of splitting that
work.

Contents:

1. [Two process tables](#two-process-tables)
2. [fork](#fork)
3. [exec](#exec)
4. [exit](#exit)
5. [wait, and reaping](#wait-and-reaping)
6. [Comparison with Linux and gVisor](#comparison-with-linux-and-gvisor)

---

## Two process tables

Every guest is a real host process that the sentry traces. So each one is
recorded twice:

- **The host kernel** has the real process: its address space, registers,
  scheduling, and a host pid.
- **The sentry** has a `Proc` (`sentry/kernel.go`), xv6's `struct proc`
  minus everything the host does for it. It holds the guest pid, parent,
  state, open-file table, current directory and program break. The sentry
  keeps two maps, `procs` (by guest pid) and `byHost` (by host pid).

Guest pids are **virtual**, starting at 1 for init and counting up
(`nextPid`, `sentry/kernel.go:85`). They have nothing to do with host
pids. This is the same idea as a Linux **pid namespace**, where a
container's init is pid 1 regardless of its host pid, and it's why
[pslist](../README.md#listing-processes-pslist) and `kill` speak in small
numbers.

All of this state is owned by the single ptrace thread, so there are no
locks on it (contrast xv6's `ptable.lock`).

---

## fork

xv6's `fork()` copies the whole address space and the file table. The
sentry can't copy a host address space from outside, so it lets the host
do it, and copies only its own bookkeeping.

**At the entry stop**, `sysFork` (`sentry/syscall.go:209`) rewrites the
call into

```
clone(CLONE_PARENT | SIGCHLD, 0, 0, 0, 0)
```

(`forkFlags`, `sentry/syscall.go:25`). Two flags matter:

- **`SIGCHLD`** makes the new task a child process (like `fork`), so the
  parent gets `SIGCHLD` and can `wait4` for it.
- **`CLONE_PARENT`** makes the *sentry*, not the guest parent, the host
  parent of the new process. Every guest is therefore a direct child of
  the sentry, so the sentry's single `wait4(-1, ...)` loop reaps all of
  them. (This is also why `pslist` shows a flat tree on the host: see
  [the pslist note](../README.md#listing-processes-pslist).)

The host kernel duplicates the address space with copy-on-write, exactly
as it does for a normal `fork`, so xv6-isor gets efficient COW fork for
free (see [memory.md](memory.md)).

**The new child is announced** by a `PTRACE_EVENT_FORK` stop on the
parent (`ptraceEvent`, `sentry/kernel.go:232`), which carries the child's
host pid. `forkChild` (`sentry/kernel.go:246`) then does the sentry half
of xv6's `fork()`:

- allocate a `Proc` and a fresh guest pid
- set its parent, name, cwd and program break from the parent
- **dup every open file** (`f.dup()`), so parent and child share file
  objects and offsets, exactly like xv6
- arrange for `fork` to return 0 in the child and the child's guest pid in
  the parent (the parent's exit hook translates the host pid, which the
  table maps back to a guest pid)

### The newborn race

A subtlety worth studying: a freshly cloned child can hit its own first
ptrace stop *before* the parent's `PTRACE_EVENT_FORK` is delivered, so the
sentry sees a stop from a host pid it doesn't know yet. `handleWait`
parks that pid in an `early` set (`sentry/kernel.go:192`); when
`forkChild` runs, it checks `early` and resumes the child if it was
waiting (`sentry/kernel.go:258`). This is the kind of ordering problem
that doesn't exist in a monolithic kernel like xv6, where `fork` builds
the child entirely before returning.

---

## exec

`exec(path, argv)` replaces the program in the current process. The host
kernel's `execve` does the heavy lifting (loading the ELF, laying out the
stack); the sentry's job is to translate and guard.

`sysExec` (`sentry/syscall.go:323`):

1. **Reads and checks `argv`** from guest memory: at most `MAXARG`
   readable strings, like xv6's own check.
2. **Resolves the guest path to a safe host path** with `execPath`
   (`sentry/fs.go:352`), which confines it to the root and requires a
   static x86-64 ELF (see [filesystem.md](filesystem.md) and
   [breakout.md](breakout.md) for why, and for the check-vs-use race this
   path has).
3. **Writes the host path into the guest's stack**, below the red zone
   (`sentry/syscall.go:353`), because `execve` needs the path in the
   guest's own memory.
4. **Rewrites the call** to `execve(hostpath, argv, NULL)` with an empty
   environment.
5. **An exit hook** renames the `Proc`, resets its program break (new
   address space) and makes `exec` return 0, or `-1` if `execve` failed.

Because the host loads an ordinary ELF, xv6-isor's guest programs are real
static x86-64 binaries with a custom entry point (`user/start.S`) instead
of xv6's in-kernel loader. The open-file table is **not** reset across
exec, matching Unix: descriptors survive `exec` unless marked
close-on-exec, which xv6 and xv6-isor don't implement for guests.

---

## exit

`exit()` ends a process. `sysExit` (`sentry/syscall.go:228`) simply
rewrites it to `exit_group(0)`; the host process really dies. The sentry
does the xv6 cleanup later, when the death is reported.

When `wait4` reports the exit (or a kill, or a crash), `procExited`
(`sentry/kernel.go:270`) runs the second half of xv6's `exit()`:

- mark the proc killed and **close all its open files** (which also wakes
  anyone blocked on a pipe it held)
- set its state to `zombie`
- **re-parent its children to init** (`sentry/kernel.go:288`), so orphans
  are adopted, exactly as xv6 does
- wake the parent, and init, in case either is in `wait`

Doing cleanup from `wait4` instead of from the dying process is an
improvement forced by the architecture: it covers processes that never
called `exit()` at all, those killed by a signal or a fault. In xv6 a
crash goes through `trap`, which calls `exit` internally; here the host
reports a `SIGSEGV`, the sentry kills the process (`sentry/kernel.go:216`,
see also [syscall-path.md](syscall-path.md)), and the same `procExited`
path collects it.

---

## wait, and reaping

A common question: **how does init find processes to reap?** It doesn't.
init just calls `wait()` in a loop (`user/init.c`), and the kernel does
the searching. No xv6 system call can list processes; `wait()` only ever
returns the pid of a child that has *already* exited, and destroys the
zombie in doing so. (That's the gap [pslist](../README.md#listing-processes-pslist)
fills with a new call.)

`sysWait` (`sentry/syscall.go:240`) is xv6's `wait()`:

- `tryWait` (`sentry/kernel.go:297`) scans for a child of the caller.
  - A `zombie` child: free its guest pid (`delete(k.procs, c.pid)`) and
    return it.
  - Live children but none dead yet: block.
  - No children (or the caller was killed): return -1.
- If it blocks, the proc is marked `waiting` and left stopped at its
  syscall entry, costing nothing. When a child later exits, `procExited`
  calls `wakeWaiter`, which re-runs `tryWait` and resumes the parent with
  the pid.

This is xv6's `sleep(curproc)` / `wakeup(initproc)` pair, rebuilt with the
sentry's block/complete mechanism (see
[syscall-path.md](syscall-path.md), "blocking calls"). Reaping works for
orphans because `procExited` already re-parented them to init, so by the
time init calls `wait()`, the dead orphan really is its child.

---

## Comparison with Linux and gVisor

| concept | xv6 | xv6-isor | Linux | gVisor |
|---|---|---|---|---|
| **create** | `fork` copies the address space in-kernel | `fork` → host `clone(CLONE_PARENT\|SIGCHLD)`; sentry copies its own state | `fork`/`clone`, COW address space | the Sentry creates a task; the host sees threads of the Sentry |
| **pids** | small integers in `ptable` | virtual, from `nextPid`; host pids hidden | global, or per **pid namespace** | virtual, in the Sentry |
| **who parents orphans** | init, via re-parenting in `exit` | init, via re-parenting in `procExited` | init, or a `PR_SET_CHILD_SUBREAPER` ancestor | the Sentry's own init |
| **replace** | in-kernel ELF loader | host `execve` of a confined, checked path | `execve`/`execveat` | the Sentry loads the ELF into a guest address space |
| **end** | `exit` runs in the dying process | `exit` → host `exit_group`; cleanup in `procExited` on the wait event | `exit_group`; `wait`-side cleanup | the Sentry frees the task |
| **collect** | `wait` scans `ptable` | `wait` scans `procs`; blocks via sentry wakeup | `wait4`/`waitid` | the Sentry implements `wait` |
| **list processes** | Ctrl-P `procdump` only | `getprocs`/`pslist` (added) | `/proc`, `ps` | `/proc`, emulated by the Sentry |

Three points worth drawing out:

- **`CLONE_PARENT` is xv6-isor's version of a subreaper.** By making the
  sentry the host parent of every guest, it gets a single `wait4` loop,
  the same way a Linux process that sets `PR_SET_CHILD_SUBREAPER` becomes
  the collector for a whole subtree. init in a pid namespace is the
  productized form.
- **Cleanup belongs on the wait side.** Both Linux and xv6-isor finish a
  process when its death is observed, not purely in the dying code path,
  so signals and crashes are handled uniformly. xv6 can be simpler
  because its `trap` funnels crashes back into `exit`.
- **gVisor keeps the whole process model in the Sentry**, like xv6-isor
  keeps it in its sentry, but gVisor's guests are *threads of the Sentry*
  rather than separate traced processes, so it never needs ptrace events
  or the newborn race.

---

## Try it yourself

- `./xv6-isor -strace fs`, then run `forktest`, and watch `fork`, `exec`,
  `exit` and `wait` interleave.
- Run `pslist` while a background job sleeps (`sleep 300 &`) to see
  states, parents and the zombie that appears after `zombie &`.
- Exercises:
  1. Add close-on-exec: a flag on a `File` and an `fcntl`-like call, so
     some descriptors don't survive `exec`.
  2. Make `wait` return the child's exit status (xv6's `wait` doesn't, and
     neither does this one). What has to change in `exit`, `sysExit` and
     `procExited`?
  3. Trace the newborn race: add a log line in `handleWait` when a pid
     lands in `early`, and run `forktest` until you see it.

---

## Sources

- The code: `sentry/syscall.go` (`sysFork`, `sysExec`, `sysExit`,
  `sysWait`, `forkFlags`), `sentry/kernel.go` (`forkChild`, `procExited`,
  `tryWait`, `wakeWaiter`, the `early` set), `user/init.c`.
- xv6: `proc.c` (`fork`, `exit`, `wait`), which xv6-isor mirrors.
- gVisor: `g3doc/architecture_guide/` (the Sentry as a process model).
- Manual pages: `clone(2)` (`CLONE_PARENT`), `execve(2)`, `execveat(2)`,
  `wait4(2)`, `prctl(2)` (`PR_SET_CHILD_SUBREAPER`), `pid_namespaces(7)`.
