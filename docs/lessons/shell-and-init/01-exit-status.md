# Lesson 1: Exit status, and a place to throw output away

Every later lesson depends on one small number: the exit status a program
leaves behind when it finishes. The shell's `&&`, `||`, `if` and `while`
all test it. `init` uses it to decide whether the system booted cleanly
and whether a crashed service should be restarted. xv6, and therefore
xv6-isor, does not have it yet. This lesson adds it, and adds a null
device so that later lessons can run programs without letting them write
on the console.

---

## Objectives

After this lesson, a student should be able to:

1. Explain what an exit status is and the conventions for its values.
2. Trace an exit status from a call to `exit` in one process to a call to
   `wait` in its parent, naming every piece of code that carries it.
3. Explain why a process killed by a signal has an exit status even
   though it never called `exit`.
4. Change a system call's signature consistently across a kernel, a C
   library and every program that uses it.
5. Add a simple device to xv6-isor and explain what a null device is for.

---

## Background

### What an exit status is

When a Unix program finishes, it hands one small integer back to the
system. By convention, 0 means success and anything else means some kind
of failure. The kernel keeps that integer in the dead process's entry in
the process table, and the process stays there as a zombie until its
parent collects the number with `wait`. Collecting it is called reaping.

The number is not for the program itself, which is gone by the time
anyone reads it. It is for whoever started the program. In practice that
is almost always a shell, and the shell uses the number to decide what to
do next.

### Conventions for the values

Only the lowest 8 bits of the value survive on Unix systems, so exit
statuses run from 0 to 255. The meaning of each value is a convention,
not a rule enforced by the kernel. The conventions this module uses are
these:

| status | meaning | who uses it |
|---|---|---|
| 0 | success | every program |
| 1 | a general failure | most programs |
| 2 | wrong usage, such as a bad option | many programs, and shells for syntax errors |
| 126 | the file was found but could not be run | shells |
| 127 | the command was not found | shells |
| 128 + n | the process was killed by signal n | shells, when reporting a child |

So a program killed by `SIGKILL` (signal 9) is reported as 137, and one
killed by a segmentation fault (`SIGSEGV`, signal 11) as 139. BSD systems
also define the values 64 to 78 in a header called `sysexits.h`, with
names such as `EX_USAGE` and `EX_NOINPUT`, but few programs use them.

The convention that 0 means success surprises students who are used to C,
where 0 is false. The reason is that there is only one way to succeed and
many ways to fail, so success gets the single value and failures get the
rest. The shell turns this around for its own logic: in Lesson 3, a
status of 0 counts as true.

### Where xv6 stands

The x86 version of xv6 that xv6-isor uses has `exit(void)` and
`wait(void)`. A program cannot report success or failure, and a parent
learns only which child died, not how. Later versions of xv6, written for
RISC-V, changed these to `exit(int status)` and `wait(int *status)`, and
this lesson makes the same change.

In xv6-isor today, the sentry rewrites the guest's `exit` into a host
`exit_group(0)` (`sysExit` in `sentry/syscall.go`), so the status is
always 0. When `wait4` reports that a guest has died, `procExited` in
`sentry/kernel.go` turns the guest into a zombie, and `tryWait` hands its
pid to a waiting parent. No status travels along that path.

### Why a null device

Later lessons start background services. A service should not write on
the console, where it would fight the shell, and it should not read from
the console, where it would steal the user's typing. Unix solves this
with a device that discards everything written to it and returns end of
file to every read. On Unix systems it is called `/dev/null`. xv6-isor
already has one device, the console, implemented as major number 1. A
null device is the second, as major number 2.

---

## Design

### The new system call signatures

The two calls become:

```c
int exit(int status) __attribute__((noreturn));
int wait(int *status);   // status may be 0 if the caller doesn't care
```

`wait` keeps its return value, the pid of the child that died, or -1 if
there are no children. When `status` is not a null pointer, `wait` also
stores the child's exit status there.

### Where the status lives

The sentry already keeps a `Proc` structure for every guest. It gains one
field, the exit status, set when the process dies:

- If the guest called `exit(n)`, the status is `n & 0xff`. The mask
  matches every Unix system, where only 8 bits survive. Students should
  test that `exit(256)` reports 0, because that surprise is a good way to
  remember the rule.
- If the guest was killed by a signal, the status is 128 plus the signal
  number. In xv6-isor that happens when `kill` sends `SIGKILL` and when a
  fault such as a bad memory access kills the process.

The sentry records the status from `exit` itself when it sees the call,
in `sysExit`. It could instead read it back from the host's wait status
when `wait4` reports the death, because the host keeps the low 8 bits of
the value passed to `exit_group`. Recording it in `sysExit` is simpler to
explain, and it is the only option for the signal case, which needs the
host wait status anyway. The recommended design does both: `sysExit`
remembers the value, and `procExited` checks the host wait status to tell
an exit from a signal.

### How the status reaches the parent

There are two paths, and both need the status.

1. **The child is already a zombie when the parent calls `wait`.**
   `sysWait` calls `tryWait`, which finds the zombie. The sentry copies
   the status into the parent's memory at the address it passed, and
   returns the pid.
2. **The parent calls `wait` first and blocks.** The sentry leaves the
   parent stopped at its system call entry, as it does today. When a child
   dies later, `procExited` calls `wakeWaiter`, which calls `tryWait` and
   then `complete`. That path also has to copy the status out, so the
   sentry must remember the address the parent passed when it first called
   `wait`. A new `Proc` field, `waitAddr`, holds it.

If copying the status into the parent fails because the address is bad,
`wait` returns -1, as in xv6-riscv. The child has still been reaped,
which is a subtle point worth discussing in class: the zombie is gone
even though the parent never learned the status.

### The program entry point

Every guest program starts in `user/start.S`, which calls `main` and then
`exit`. Today it ignores `main`'s return value. After this lesson, the
return value of `main` should become the exit status, as it does in C on
every Unix system. That is one extra instruction, moving `%eax` into
`%edi` before the call to `exit`.

### The null device

Device files in xv6-isor are regular files with a magic header, created
by `mknod` (see [filesystem.md](../../filesystem.md)). The sentry's
`sysRead` and `sysWrite` check the major number and handle the console.
Major 2 becomes the null device:

- a read returns 0 bytes, which means end of file
- a write returns the number of bytes it was given, and discards them

A program creates the device file with `mknod("/dev/null", 2, 0)`. In
Lesson 7, `/etc/rc` will create `/dev` and the device files at boot,
because that is a decision about the system's layout, not something the
kernel should decide.

---

## Walkthrough

### Step 1: write the tests

Before changing any code, students add tests to `user/isortest.c` for the
behavior they are about to build. Writing them first forces decisions
about edge cases, such as what `wait(0)` should do.

- A child that calls `exit(3)` is reported with status 3.
- A child that calls `exit(256)` is reported with status 0.
- A child whose `main` returns 7 is reported with status 7.
- A child killed with `kill` is reported with status 137.
- A child that dereferences a null pointer is reported with status 139.
- `wait(0)` still works and still returns the pid.
- A process with no children gets -1 from `wait`.
- Writing to the null device succeeds, and reading from it returns 0.

### Step 2: change the user-side declarations

In `user/user.h`, change the declarations of `exit` and `wait`. The
system call stubs in `user/usys.S` don't change, because the arguments
are already in the right registers. Then fix `user/start.S` so that the
value returned by `main` becomes the argument to `exit`.

### Step 3: update every program

Every program in `user/` that calls `exit()` must now call `exit(0)` or
`exit(1)`, and every call to `wait()` must become `wait(0)`. This is
mechanical, but it is also a chance to make the programs better: a
program that prints a usage message should exit with 1 or 2, not 0. The
compiler will find every call that still has the old form, because the
declarations changed.

Teaching assistants should point out that this is the same work a real
operating system team faces when it changes a system call. Every caller
in the system has to change at once, which is why stable system call
interfaces matter so much on real systems.

### Step 4: change the sentry

In `sentry/kernel.go`, add `xstatus` and `waitAddr` fields to `Proc`. In
`sentry/syscall.go`, change `sysExit` to read its argument, store it
masked to 8 bits, and pass it on to the host's `exit_group`. Change
`sysWait` to remember its argument in `waitAddr`.

In `procExited`, decide the final status. The `waitEvent` that reaches it
already carries the host's wait status, so `procExited` needs that status
as a parameter. If the host reports a signal, the status is 128 plus the
signal number. If it reports a normal exit, the status is the value
`sysExit` recorded.

In `tryWait`, when a zombie child is found, copy its status into the
parent if `waitAddr` is not zero. That one place covers both paths,
because `wakeWaiter` also goes through `tryWait`. Copying uses `copyOut`
from `sentry/mem.go`.

### Step 5: add the null device

In `sentry/file.go`, add a constant for major 2. In `sysRead` and
`sysWrite`, handle it next to the console. This is about ten lines of Go.

### Step 6: update `-strace`

The `-strace` table lists argument kinds for each call. `exit` now has
one integer argument and `wait` one pointer. This change is small, but it
makes the trace show the status, which is useful in every later lesson.

---

## Lab

**Assignment.** Implement `exit(int)`, `wait(int *)` and the null device
as described above, and update every program.

**Done when:**

- `make test` passes, including the new `isortest` checks.
- `./xv6-isor -strace fs` shows `exit(3)` and the matching `wait` call.
- `grep -n "exit()" user/*.c` finds no calls with the old form.
- A short written answer explains why the zombie is reaped even when
  copying the status fails.

**Hand in** the diff and the written answer.

---

## Pitfalls

- **Forgetting the blocking path.** Students often copy the status in
  `sysWait` only. The test that has the parent wait before the child exits
  then fails, because the status arrives through `wakeWaiter`.
- **Copying into a dead parent.** If the parent has been killed while it
  waits, `complete` already refuses to resume it, but the copy must also
  be skipped. Check the parent's state before copying.
- **Masking in the wrong place.** If the mask is applied in the parent,
  a child that exits with 256 appears as 256 in `pslist`, which is wrong.
  Mask once, when the status is recorded.
- **Confusing the host pid with the guest pid.** `procExited` receives the
  host wait status, but the parent must see the guest pid. The existing
  code already returns the guest pid; keep it that way.
- **Leaving `start.S` alone.** Without the change to `start.S`, a program
  whose `main` returns 1 still reports 0, which breaks tests in later
  lessons in confusing ways.

---

## Exercises

1. **Easy.** Add an `EXITSTATUS` column to `pslist` that shows a zombie's
   exit status. What should it show for a running process?
2. **Easy.** Write `true` and `false` as user programs. They will be
   needed in Lesson 3. How short can each one be?
3. **Medium.** Make `kill` print a message when it fails, and exit with
   status 1. Then write a script, after Lesson 2, that uses that status.
4. **Medium.** xv6-riscv's `wait` returns -1 when the copy fails, but has
   already freed the child. Change the sentry so that the zombie survives
   if the copy fails. Argue for or against your change in two paragraphs.
5. **Hard.** Real Unix systems encode the exit status and the signal in
   one integer, read with macros such as `WIFEXITED` and `WTERMSIG`.
   Redesign `wait` to report both, and update `isortest`. Explain what the
   shell gains from the extra information.
6. **Hard.** Add a `/dev/zero` device, major 3, that returns zero bytes on
   read. Which xv6 program could use it?

---

## Check your understanding

1. Why does the kernel keep a dead process around as a zombie?
2. What exit status does a shell report for a program killed by signal 9,
   and why that number?
3. What does `exit(256)` report, and why?
4. Name the two paths by which an exit status reaches a parent in the
   sentry.
5. Why must every program in `user/` change when `exit` changes
   signature?
6. What does a read from the null device return, and what does a program
   reading it conclude?
7. Why does `/etc/rc`, and not the kernel, create `/dev/null`?
8. Where in `user/start.S` does the return value of `main` get lost
   today?
