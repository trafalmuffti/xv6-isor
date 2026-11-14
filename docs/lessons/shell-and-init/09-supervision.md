# Lesson 9: Keeping services running after boot

After Lesson 8, `/etc/rc` starts services at boot, but nothing watches
them afterwards. If `ticker` crashes, it stays dead until someone notices
and runs `service ticker start`. This lesson makes `init` a supervisor:
it starts selected services itself, notices when one exits, and restarts
it according to a policy, but gives up on a service that keeps crashing.
This is the job Research UNIX `init` already did for the login programs
on each terminal, extended to any service. There is still no cron and no
time-based scheduling: the only reason to start a program after boot in
this design is that a supervised program has stopped.

---

## Objectives

After this lesson, a student should be able to:

1. Explain why a supervisor must be the parent of the processes it
   supervises.
2. Design a restart policy with a crash-loop limit, and explain why the
   limit is necessary.
3. Explain how `init` can wake up regularly without signals or a timeout
   on `wait`, and what that costs.
4. Explain why services should not "daemonize" when they are supervised.
5. Connect the design to System V's `respawn`, daemontools, runit, s6 and
   systemd.

---

## Background

### Supervision needs parenthood

On Unix, the only reliable way to learn that a process has exited is to
be its parent and call `wait`. Polling a pid with `isalive`, as Lesson 8
does, is unreliable, because a pid can be reused by another program, and
because there is always a delay between the death and the next poll. A
parent, by contrast, learns about the death immediately, gets the exit
status, and cannot be confused by pid reuse, because the pid is not freed
until the parent collects it.

That is why the supervisor must be the parent. In Lesson 8, services are
started in the background by a shell running `rc.subr`. That shell exits
straight away, so the service is orphaned and re-parented to `init`.
`init` then does collect it when it dies, but `init` has no idea which
service the pid belonged to, so it cannot restart it. This lesson makes
`init` start supervised services itself, and remember which pid is which
service.

### Respawning in history

Supervision is old. Research UNIX `init` started a login program on each
terminal and started it again whenever it exited, which is supervision
of exactly one kind of program. System V generalized this in
`/etc/inittab`, where any entry marked `respawn` is restarted when it
exits. System V `init` also introduced a crash-loop limit: an entry that
respawns too quickly is disabled for a few minutes with a message on the
console.

In the late 1990s, Daniel J. Bernstein's daemontools made supervision a
separate tool: one small `supervise` process per service, which starts
the service, waits for it, and starts it again. runit and s6 follow the
same design, and systemd builds restart policies into its service units,
with settings such as `Restart=on-failure` and a start limit that plays
the same role as System V's crash-loop limit. This lesson's design sits
closest to System V `inittab` and runit: `init` itself supervises, and
the policy for each service is chosen in scripts.

### The problem of waking up

A supervisor spends most of its time blocked in `wait`, which is exactly
right: it wakes when a child dies. But it also has to wake for other
reasons: a request to start supervising a new service, a request to stop,
a request to shut down, or a restart that has been deliberately delayed.
Real systems use signals for this, or a version of `wait` with a timeout.
xv6 has neither. Lesson 7 used a trick for shutdown, killing a child so
that `wait` returns. This lesson replaces the trick with a general
mechanism.

---

## Design

### The timer child

`init` keeps one extra child, the **timer**, whose only job is to exit
after a fixed delay:

```c
int
start_timer(void)
{
  int pid = fork();
  if(pid == 0){
    sleep(100);   // 100 ticks: one second
    exit(0);
  }
  return pid;
}
```

When the timer exits, `init`'s `wait` returns its pid. `init` recognizes
the pid, does its periodic work, and starts a new timer. So `init` wakes
at least once a second without any new kernel feature, and it still wakes
immediately when a service dies.

The cost is one extra process creation per second, which is real on
xv6-isor, where `fork` is a rewritten host system call with several
ptrace stops (see [syscall-path.md](../../syscall-path.md)). The benefit
is that the whole design needs no change to the sentry. Teachers should
present the alternatives too:

| way to wake | needs | trade-off |
|---|---|---|
| timer child (this lesson) | nothing new | a process per tick; up to one tick of delay |
| `wait` with a timeout | a new system call | cleaner; changes the kernel interface |
| a non-blocking `wait` plus `sleep` | a new flag on `wait` | busy-ish loop; simple |
| signals (`SIGALRM`, `SIGHUP`) | a signal system | what real Unix does; large to add |
| a control device that blocks until a request arrives | a new device in the sentry | precise; more sentry code |

Exercise 6 asks students to implement one alternative and measure it.

The timer is not a scheduler. It fires at a fixed interval only so that
`init` can check its control directory and any delayed restarts. Nothing
in this design runs a program because of the time of day.

### The control directory

Requests to `init` are files in `/var/run/init/`. Files are the one form
of communication every xv6 program already has, and they survive the
death of the program that wrote them.

| path | meaning | written by |
|---|---|---|
| `/var/run/init/want/NAME` | supervise service `NAME`; the file holds the restart policy | `respawn` |
| `/var/run/init/pid/NAME` | `NAME` is running with this pid | `init` |
| `/var/run/init/failed/NAME` | `NAME` crashed too often and is no longer restarted | `init` |
| `/var/run/init/request` | `halt` or `reboot` | `shutdown` |

Two small user programs write the requests:

- `respawn NAME [always|on-failure]` writes the want file.
- `unrespawn NAME` removes the want file.

Lesson 7's `shutdown` becomes simpler: it writes the request file and
exits. `init` notices within one tick, so the process-tree walk from
Lesson 7 is no longer needed.

### What `init` does on each wake

Whenever `wait` returns, `init` handles the pid it got, and then runs one
pass of its supervision loop:

1. **If the pid was the timer**, start a new timer.
2. **If the pid was a supervised service**, record its exit status and
   time, remove its pid file, and decide whether to restart it (below).
3. **Otherwise**, the pid was an adopted orphan; nothing more to do.
4. **Scan the want directory.** For every want file without a running
   service and without a failed mark, start the service. For every running
   service whose want file has disappeared, kill it.
5. **Check the request file**, and enter the death state from Lesson 7 if
   it says `halt`.

Starting a service means forking a child that runs `/sh /etc/rc.d/NAME
run`. The `run` action from Lesson 8's `rc.subr` ends with `exec`, so the
shell replaces itself with the service, and the pid `init` got from
`fork` is the service's pid. That is why Lesson 6 introduced `exec` and
why Lesson 8's library has a `run` action. `init` writes the pid into
`/var/run/init/pid/NAME`.

### The restart policy

Each want file holds one word:

| policy | restart when the service exits with status 0? | with a non-zero status? |
|---|---|---|
| `always` | yes | yes |
| `on-failure` | no | yes |

A service that exits under a policy that says not to restart it has its
want file removed by `init`, so that a later scan does not start it
again.

### The crash-loop limit

A service that fails immediately every time it starts would otherwise be
restarted forever, as fast as `init` can fork, filling the console with
messages and slowing the whole system. So `init` keeps, for each service,
the times of its recent restarts. If a service has been restarted 5 times
within 1,000 ticks (10 seconds), `init` stops restarting it, writes a
failed mark, and prints:

```
init: crashy restarted 5 times in 10s; giving up
```

The numbers are constants in `init.c`. Making them configurable per
service is an exercise. Removing the failed mark, with `service NAME
start` or by hand, lets `init` try again.

A gentler refinement is **backoff**: wait one tick before the first
restart, two before the second, four before the third, and so on. The
timer child makes this easy, because `init` already wakes every tick to
check whether a delayed restart is due.

### Changes to `rc.subr` and `rc.conf`

Whether a service is supervised is policy, so it is set in `rc.conf`:

```sh
ticker_enable="YES"
ticker_supervise="YES"
ticker_restart="always"

crashy_enable="YES"
crashy_supervise="YES"
crashy_restart="on-failure"
```

`run_rc_command` gains a branch for supervised services:

| action | unsupervised (Lesson 8) | supervised (this lesson) |
|---|---|---|
| `start` | run in background, write pidfile | clear any failed mark; `respawn NAME policy` |
| `stop` | kill the pid in the pidfile | `unrespawn NAME`; `init` kills it |
| `status` | `isalive` on the pidfile | read `/var/run/init/pid/NAME` or the failed mark |
| `run` | — | `exec` the command (used only by `init`) |

So the user's interface does not change: `service ticker status` works
either way. Only the mechanism underneath differs, which is the point of
keeping mechanism in a library.

### Daemons should not daemonize

Many traditional Unix daemons "daemonize" when they start: they fork,
the parent exits, and the child carries on in the background. Under a
supervisor this is exactly wrong. The process `init` started exits
immediately with status 0, so `init` thinks the service has stopped, and
the real service is now an orphan that nobody supervises. The daemontools
documentation made this point forcefully, and modern practice agrees:
under a supervisor, a service should stay in the foreground. Services in
this module do, and students should be able to explain why.

### Which programs `init` now runs

After this lesson, `init`'s children in multi-user mode are:

- the login shell on the console, restarted when it exits
- the timer, restarted every tick
- one process per supervised service
- any adopted orphans, until they die

`pslist` shows them all, with `init` as their parent, and it is a good
tool for checking the implementation.

---

## Walkthrough

### Step 1: write the tests

Supervision tests need care, because they involve time. They should wait
for observable events, such as a line in `/var/log/ticker` or a file in
`/var/run/init/`, rather than for a fixed time.

- A supervised `ticker` is running after boot, with its pid in
  `/var/run/init/pid/ticker`, and `pslist` shows `init` as its parent.
- After `kill` on that pid, a new `ticker` with a different pid appears.
- `service ticker stop` stops it, and it is not restarted.
- `crashy` with `on-failure` is restarted after each crash, then marked
  failed, and the console shows the "giving up" message.
- A service that exits with 0 under `on-failure` is not restarted.
- `shutdown` halts within about a second, without the Lesson 7
  process-tree walk.
- `pslist` shows exactly one timer child at any time.

### Step 2: add the timer and the wake loop

Restructure `multi_user` so that every return from `wait` leads to the
same handling function. Add the timer and test that `init` wakes every
second, for example by having it write `uptime()` to a debug file.

### Step 3: add the control directory and supervised starts

Write `respawn` and `unrespawn`. Add the scan of the want directory and
starting services through `rc.d/NAME run`.

### Step 4: add restart policies and the crash-loop limit

Keep a small table in `init` of supervised services: name, pid, policy,
and the times of recent restarts. A fixed-size array is enough.

### Step 5: update `rc.subr`, `rc.conf` and `shutdown`

Then boot and run all the tests, including Lesson 8's, which must still
pass for unsupervised services.

---

## Lab

**Assignment.** Make `init` supervise services as described.

**Done when:**

- All the tests above pass, with every earlier test.
- `init.c` is still under about 400 lines.
- A half-page design note explains how `init` learns about each kind of
  event (a service death, a request, a delayed restart, a shutdown) and
  how long each can take to be noticed.

---

## Pitfalls

- **Mistaking the timer for a service.** The timer's pid must be checked
  first. If it is treated as a dead service, the service table fills with
  nonsense.
- **Two timers.** If `init` starts a new timer for a reason other than the
  old one exiting, timers multiply, and `init` wakes more and more often.
- **Starting a service twice.** If the scan of the want directory runs
  before `init` has recorded a newly started service's pid, it starts a
  second copy. Record the pid immediately after `fork`.
- **Forgetting `exec` in the `run` action.** Then `init`'s child is a shell
  that waits for the service, killing the shell orphans the service, and
  `stop` appears to work while the service keeps running.
- **Restart storms.** Without the crash-loop limit, a broken service makes
  the system unusable. Test the limit before anything else.
- **Blocking `init`.** `init` must never block on anything except `wait`.
  Reading a file is fine, but waiting for user input, or for a program to
  finish in the foreground, freezes supervision.
- **Leaving want files after a planned stop.** If `stop` kills the service
  but leaves the want file, `init` restarts it at the next tick.

---

## Exercises

1. **Easy.** Make `init` log every start, exit and restart, with the exit
   status, to `/var/log/init`.
2. **Easy.** Add a `service NAME failed` action that clears the failed
   mark without starting the service.
3. **Medium.** Make the crash-loop numbers configurable per service by
   putting them in the want file, such as `on-failure 5 1000`.
4. **Medium.** Implement exponential backoff between restarts, with a
   maximum delay. Explain what problem backoff solves that the crash-loop
   limit alone does not.
5. **Medium.** Write a service that daemonizes (forks and exits), run it
   under supervision, and describe exactly what `init` and `pslist` show.
6. **Hard.** Replace the timer child with a new system call,
   `waittimeout(int *status, int ticks)`, in the sentry. Compare the code
   in `init` before and after, and measure the cost of each with
   `-strace`.
7. **Hard.** Move supervision out of `init` into a separate `supervise`
   program, one per service, as daemontools does, with `init` supervising
   only the supervisors. Argue which design is easier to reason about when
   things go wrong.

---

## Check your understanding

1. Why must a supervisor be the parent of the process it supervises?
2. Why can `isalive` give a wrong answer where `wait` cannot?
3. How does `init` wake up once a second without signals?
4. What does the timer child cost, and what does it buy?
5. Why does the `run` action end with `exec`?
6. What is the difference between the `always` and `on-failure`
   policies?
7. Why is a crash-loop limit necessary?
8. Why should a supervised service not daemonize?
9. Why is the timer child not a scheduler, and why does this design have
   no cron?
