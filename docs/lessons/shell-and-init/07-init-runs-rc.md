# Lesson 7: init runs `/etc/rc`

The shell is finished. This lesson turns to `init`, the first process,
and gives it the job it had in Research UNIX and the BSDs: run a shell
script called `/etc/rc` to bring the system up, fall back to a repair
shell if that script fails, and shut the system down cleanly when asked.
The lesson is about who decides what during boot, and the answer it
teaches is that almost every decision belongs in a script.

---

## Objectives

After this lesson, a student should be able to:

1. Describe the duties that only process 1 can perform, and why.
2. Trace a boot from the first process to a shell prompt, naming the
   program and file that make each decision.
3. Explain single-user mode, what it is for, and how a system gets into
   and out of it.
4. Write `/etc/rc` and `/etc/profile` for xv6-isor.
5. Write `init` as a small state machine, following the design of 4.4BSD.
6. Explain how `init` can shut the system down without signals.

---

## Background

### What is special about process 1

`init` is the first program the kernel runs, and it has process ID 1. Two
things make it special.

- **It adopts orphans.** When a process exits, its children are given to
  `init`, which becomes their parent and must eventually call `wait` for
  them. If `init` stopped calling `wait`, dead orphans would stay in the
  process table as zombies forever. xv6-isor's sentry does this
  re-parenting in `procExited`, exactly as xv6's kernel does in `exit`
  (see [processes.md](../../processes.md)).
- **It must not exit.** Every other process has `init` as an ancestor. In
  xv6, if `init` exits the kernel panics. In xv6-isor, the sentry prints
  `init exiting` and stops (`procExited` in `sentry/kernel.go`), always
  with status 1 today.

Everything else that `init` does is policy. Which programs to start, in
what order, with what settings, and what to do when one dies, are all
choices, and this module's principle is to write choices in the shell.

### Research UNIX

In Seventh Edition Research UNIX, `init` was a short C program. At boot
it ran `/etc/rc` with the shell, then read a file listing the terminals,
started a login program on each one, and started it again whenever it
exited. If the machine was booted in single-user mode, `init` first ran a
shell on the console, and continued the normal boot only when that shell
exited. The decisions about mounting file systems, cleaning temporary
files and starting daemons were all in `/etc/rc`, which an administrator
could read and edit.

### 4.4BSD

The BSDs kept that structure and made it more robust. The 4.4BSD `init`
is written as a state machine, with one function per state, and each
function returns the next state to enter. The main states are:

| state | what happens |
|---|---|
| single user | a shell runs on the console for repairs |
| runcom | `/etc/rc` runs, with the argument `autoboot` on a normal boot |
| read ttys | the terminal list is read |
| multi user | login programs run on the terminals and are restarted when they exit |
| clean ttys | the terminal list is reread after a change |
| death | every process is told to stop, as part of shutdown |

If `/etc/rc` fails, for example because a file system check found
errors, `init` goes to single-user mode instead of multi-user mode, so an
administrator can repair the system. When the single-user shell exits,
`init` runs `/etc/rc` again. Later BSD systems added `/etc/rc.shutdown`,
a script that `init` runs before stopping the remaining processes.

The 4.4BSD design is the model for this lesson, simplified: xv6-isor has
one console and no terminal list, and it has no signals.

### No signals

The BSD `init` is controlled by signals. For example, `shutdown` sends it
a signal to halt, and another signal makes it reread the terminal list.
xv6 has no signals, so this lesson needs another way for `init` to learn
that it should shut down. The answer here uses two things xv6-isor
already has: `init` wakes up whenever one of its children exits, and the
`getprocs` call from `pslist` can find which process is `init`'s child.
Lesson 9 generalizes the idea.

---

## Design

### The boot sequence

After this lesson, booting goes like this:

1. The sentry starts `/init`, as now.
2. `init` makes sure the console is open as file descriptors 0, 1 and 2,
   as now.
3. If `/etc/rc` exists, `init` enters the **runcom** state: it runs
   `/sh /etc/rc autoboot` and waits for it, collecting any other children
   that exit meanwhile.
4. If `/etc/rc` exits with status 0, `init` enters **multi-user**. If it
   exits with any other status, `init` prints a message and enters
   **single-user**.
5. In single-user, `init` runs `/sh` on the console and waits. When that
   shell exits, `init` goes back to runcom and runs `/etc/rc` again, as in
   BSD.
6. In multi-user, `init` runs a login shell, `/sh -l`, and starts it again
   whenever it exits, as today.
7. If `/etc/rc` does not exist, `init` goes straight to multi-user and
   behaves exactly as it does today. This keeps every earlier test
   working.

### `init` as a state machine

The code follows 4.4BSD's structure closely, because that structure makes
the boot logic easy to read:

```c
typedef int (*state_func)(void);

int single_user(void);   // each returns the index of the next state
int runcom(void);
int multi_user(void);
int death(void);

state_func states[] = { single_user, runcom, multi_user, death };

int
main(void)
{
  int s = setup_console_and_pick_first_state();
  for(;;)
    s = states[s]();
}
```

Each state function starts the programs it needs and then waits. Every
state uses the same helper to wait, which matters because of the next
point.

### Waiting for a particular child

`wait` returns whichever child exits first. While `/etc/rc` runs, an
orphan that was re-parented to `init` might exit first. The helper loops
until the child it is interested in has exited, reaping any others along
the way:

```c
int
waitfor(int pid, int *status)
{
  int w, st;
  while((w = wait(&st)) >= 0){
    if(w == pid){
      *status = st;
      return 0;
    }
    // some other child, probably an adopted orphan: reaped, nothing more
  }
  return -1;   // no children left
}
```

This is the same problem Lesson 2's shell had with background jobs, and
it is the reason xv6's `init` prints `zombie!` today. Students should
connect the two.

### Login shells and `/etc/profile`

A login shell is the shell a user gets after logging in. By convention it
first runs a profile script that sets up the user's session. xv6 has no
users and no login, but the convention is still useful: `/sh -l` sources
`/etc/profile` with the `.` builtin from Lesson 4, and then reads
commands from the console. A typical `/etc/profile` for xv6-isor is:

```sh
# /etc/profile: run by login shells
PATH=/:/bin
PS1='xv6$ '
echo "Welcome to xv6-isor."
```

Because the profile is sourced, not run as a separate program, the
variables it sets remain in the login shell.

### `/etc/rc`

The boot script decides everything about the system's layout and what
runs at boot. A first version for this lesson:

```sh
#!/bin/sh
# /etc/rc: run by init at boot. $1 is "autoboot" on a normal boot.

echo "rc: starting ($1)"

# Device files. The kernel doesn't decide where these live; rc does.
test -d /dev || mkdir /dev
test -e /dev/console || mknod /dev/console 1 1
test -e /dev/null || mknod /dev/null 2 0

# Directories for state that should not survive a reboot.
test -d /var || mkdir /var
test -d /var/run || mkdir /var/run
for f in /var/run/*; do
    test -f "$f" && rm "$f"
done

hostname=xv6
echo "rc: hostname is $hostname"

# The administrator's own additions.
if test -f /etc/rc.local; then
    echo "rc: running /etc/rc.local"
    . /etc/rc.local
fi

exit 0
```

Students should read this script line by line and say which lesson made
each line possible: `test` from Lesson 5, `||` from Lesson 3, variables
from Lesson 4, `.` from Lesson 4, the null device and the exit status from
Lesson 1. That review is the best way to show that the shell lessons were
building toward this.

`mknod` is a user program that calls the `mknod` system call. xv6 has no
such program today, and writing it is part of this lesson's lab. `rm` and
`mkdir` already exist.

Note that `rc` creates `/dev/console`, but `init` still opens `console`
in the root directory, because it runs before `rc`. Moving `init` to
`/dev/console` is an exercise: it has a chicken-and-egg problem worth
discussing.

### Shutting down

Shutdown has two parts: asking `init` to stop, and the stop itself.

**Asking.** A new user program, `shutdown`, writes the request into the
file `/var/run/init.request`, as the word `halt`. Then it must wake
`init`, which is blocked in `wait`. `init` wakes up when one of its own
children exits, and the console shell is one of its children. So
`shutdown` finds that shell, by following parent pids upward with
`getprocs` from its own entry until it reaches a process whose parent is
pid 1, and kills it. `init`'s `wait` returns, and before starting a new
shell `init` checks for the request file.

This is indirect, and students should be told so: it is a workaround for
the lack of signals, built from parts the system already has. Lesson 9
replaces it with a more general mechanism, a timer child that wakes
`init` regularly.

**Stopping.** In the **death** state, `init`:

1. runs `/sh /etc/rc.shutdown` if it exists, and waits for it
2. uses `getprocs` to list the remaining processes, and kills each one
   except itself
3. collects them all with `wait` until it returns -1
4. prints `init: halted` and calls `exit(0)`

**The sentry must cooperate.** Today the sentry treats `init` exiting as
an error and exits with status 1. After this lesson, it should exit with
`init`'s own exit status, using the status Lesson 1 added. Then
`./xv6-isor fs` exits with 0 after a clean shutdown, and a test can check
that. This is a one-line change in `procExited`.

A first `/etc/rc.shutdown`:

```sh
#!/bin/sh
# /etc/rc.shutdown: run by init before it stops everything else.
echo "rc.shutdown: stopping"
# xv6's rm has no -f option, so test first.
test -f /var/run/init.request && rm /var/run/init.request
exit 0
```

Lesson 8 extends it to stop services in reverse order.

### What goes where

A summary table helps students keep the split clear:

| decision | made by |
|---|---|
| reap orphans | `init` (only it can) |
| restart the login shell | `init` |
| what happens at boot, in what order | `/etc/rc` |
| create `/dev` and its files | `/etc/rc` |
| session settings such as `PATH` | `/etc/profile` |
| what happens at shutdown | `/etc/rc.shutdown` |
| stopping the remaining processes | `init` |
| whether a failed boot gets a repair shell | `init`, using `/etc/rc`'s exit status |

---

## Walkthrough

### Step 1: write the tests

Boot tests need their own root directories, because each one uses a
different `/etc/rc`. `test.sh` gains a helper that copies `fs/` into a
temporary directory, adds or replaces files under `etc/`, boots it with
scripted console input, and checks the output. Tests include:

- With no `/etc/rc`, the console behaves exactly as today.
- With an `/etc/rc` that prints `hello from rc` and exits 0, that line
  appears before the first prompt.
- With an `/etc/rc` that exits 1, the output contains `single-user`, and
  a command typed next runs in the single-user shell.
- Typing `exit` in the single-user shell runs `/etc/rc` again.
- `/etc/profile` sets `PS1`, and the prompt changes.
- `shutdown` runs `/etc/rc.shutdown`, prints `init: halted`, and the
  sentry exits with status 0.
- An orphan created by `/etc/rc` (a background job that outlives the
  script) is reaped, which a test can check with `pslist`.

### Step 2: write `mknod` and `shutdown`

Both are short user programs. `shutdown` is the more interesting one,
because it uses `getprocs` to walk up the process tree.

### Step 3: restructure `init`

Move the existing loop into `multi_user`, add `waitfor`, and add the
other states. Keep the behavior with no `/etc/rc` identical.

### Step 4: change the sentry's handling of `init`'s exit

Make the sentry exit with `init`'s status.

### Step 5: write the scripts

Add `fs/etc/rc`, `fs/etc/profile` and `fs/etc/rc.shutdown`. The Makefile
must copy them into `fs/`, which it currently does only for programs and
the README.

### Step 6: update the README and the end-to-end test

The boot transcript in the top-level README changes, and so do the lines
`test.sh` expects. Update both, and say in the README how to get a
single-user shell (break `/etc/rc`, or create a file that `rc` checks, as
in exercise 3).

---

## Lab

**Assignment.** Implement the boot and shutdown sequence above.

**Done when:**

- All the boot tests pass, along with every earlier test.
- `./xv6-isor fs`, given the input `shutdown`, exits with status 0.
- A diagram, handed in, shows the states, the transitions between them,
  and the condition for each transition.

---

## Pitfalls

- **Forgetting to reap orphans in every state.** If a state waits with a
  plain `wait` and treats the first pid as its child, an orphan's death is
  mistaken for the shell's, and `init` starts a second shell.
- **Letting `init` exit by accident.** Any path where `init` returns from
  `main` or calls `exit` outside the death state ends the system. Test the
  failure paths, such as `fork` failing.
- **A boot loop.** If `/etc/rc` always fails, and the single-user shell's
  input is exhausted, `init` alternates between runcom and single-user
  forever. With scripted input, the sentry's end-of-input rule stops it,
  but students should still think about what a real system should do.
- **`/etc/rc` waiting forever.** If `rc` starts a program in the
  foreground that never ends, boot never finishes. Real systems have the
  same problem; Lesson 8's services start in the background for this
  reason.
- **Running `rc` with the console as standard input.** `rc` can read the
  console, which is useful for asking questions at boot and dangerous for
  programs that read input unexpectedly. Lesson 8 redirects services to
  `/dev/null`.
- **Not copying `fs/etc` in the Makefile.** The scripts exist in the
  repository but not in the guest's root, and the boot quietly takes the
  "no `/etc/rc`" path.

---

## Exercises

1. **Easy.** Make `init` print how long `/etc/rc` took, using `uptime()`.
2. **Easy.** Add a `reboot` request: `init` runs `rc.shutdown`, stops the
   remaining processes, and then goes back to runcom instead of exiting.
3. **Medium.** Make `/etc/rc` enter single-user mode on purpose when a file
   `/etc/singleuser` exists, by exiting with status 1. Is a file the best
   way to ask for single-user mode at boot? What did real systems use?
4. **Medium.** Move `init` to `/dev/console`. Explain the chicken-and-egg
   problem and how you solved it.
5. **Hard.** Give `/etc/rc` a time limit: if it has not finished within a
   number of ticks, `init` kills it and enters single-user mode. You will
   need the timer child from Lesson 9, or a design of your own.
6. **Hard.** The `shutdown` program finds the console shell by walking up
   the process tree. Find a situation in which it kills the wrong
   process, and fix it.

---

## Check your understanding

1. What two things make process 1 special?
2. What does `init` do when `/etc/rc` exits with status 1?
3. What happens when the single-user shell exits?
4. Why must `init` loop when waiting for a particular child?
5. Why does `/etc/profile` have to be sourced rather than run?
6. Which lessons made each line of the sample `/etc/rc` possible?
7. How does `shutdown` wake `init` without signals?
8. Why must the sentry exit with `init`'s status after this lesson?
9. Which decisions in the "what goes where" table belong to `init`, and
   why can they not be moved to a script?
