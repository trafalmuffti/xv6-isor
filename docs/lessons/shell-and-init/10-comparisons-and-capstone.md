# Lesson 10: Other init systems, the capstone, and assessment

The last session steps back from the code. Students have built a shell
and an `init` in the Research UNIX and BSD tradition, and now they can
compare it with the other designs that real systems use, from System V to
systemd to the tiny `init` programs inside containers. The session ends
with the capstone project and the assessment for the whole module.

---

## Objectives

After this lesson, a student should be able to:

1. Place the system they built among the major Unix init designs, and
   describe what each design does differently and why.
2. Name the duties every process 1 must perform, whatever its design.
3. Argue for and against declarative service configuration, compared
   with shell scripts.
4. Explain what goes wrong when an ordinary program runs as process 1 in
   a container, and how small init programs fix it.
5. Plan and carry out a capstone project that extends the system.

---

## The duties every process 1 shares

Every design below, however large or small, must do the same few things,
because the kernel gives them to process 1 and nobody else:

1. **Start something.** The kernel runs one program; everything else
   descends from it.
2. **Reap orphans.** Every orphaned process becomes a child of process 1,
   which must collect it, or zombies accumulate.
3. **Never exit.** On most kernels, process 1 exiting brings the system
   down.
4. **Handle shutdown.** Something has to stop every other process in an
   orderly way.

Everything else, ordering, configuration, supervision, logging and
parallel startup, is a design choice. Students should be able to sort the
features of any init system into "duty" and "choice", and the tables
below are a good place to practise.

---

## The designs, compared

| design | era | configuration | order decided by | supervises? | who runs it |
|---|---|---|---|---|---|
| Research UNIX `init` + `/etc/rc` | 1970s | one shell script | line order | the login programs only | early Unix, Seventh Edition |
| 4.4BSD `init` + `/etc/rc` | early 1990s | one shell script, plus a terminal list | line order | the login programs only | the BSDs of that era |
| NetBSD/FreeBSD `rc.d` | 2000 onward | `rc.conf` + one script per service | `rcorder`, from `PROVIDE`/`REQUIRE` | no, by default | NetBSD, FreeBSD, and others |
| System V `init` | 1980s onward | `/etc/inittab` + numbered scripts per run level | the numbers in script names | `respawn` entries in `inittab` | commercial Unix; most Linux distributions until the 2010s |
| OpenRC | 2007 onward | dependency-aware shell scripts | dependencies declared in scripts | optional | Gentoo, Alpine and others |
| Upstart | 2006 onward | job files reacting to events | events | yes | Ubuntu for several years |
| launchd | 2005 onward | property-list files | on demand and dependencies | yes | macOS |
| daemontools / runit / s6 | late 1990s onward | one directory per service with a `run` script | runit: three stages, then all services at once | yes, one supervisor per service | Void Linux (runit), many containers and embedded systems |
| systemd | 2010 onward | declarative unit files | dependencies and ordering declared in units | yes, with restart policies and start limits | most Linux distributions today |
| xv6-isor (this module) | — | `rc.conf` + `rc.subr` + `rc.d`, all in shell | `rcorder` | yes, `init` with `respawn` | this repository |

A few rows deserve more explanation.

### System V

System V `init` reads `/etc/inittab`, a table in which each line names a
program, the run levels it belongs to, and an action such as `once`,
`wait` or `respawn`. A run level is a number standing for a system state,
such as single-user or multi-user with networking. Changing run level
runs the scripts for the new level from directories such as `/etc/rc3.d`,
whose names begin with `S` or `K` and a number. Compared with this
module, System V adds run levels as a first-class idea and supervision
through `respawn`, and it orders services by numbers that people choose
instead of by computed dependencies.

### runit and the daemontools family

runit divides the life of a system into three stages, each a script:
stage 1 does one-time setup, stage 2 starts the supervision tree and runs
until shutdown, and stage 3 stops everything. In stage 2, a program called
`runsvdir` starts one `runsv` supervisor for every service directory, and
each `runsv` starts the service's `run` script and restarts it when it
exits. There is no ordering between services: they all start at once,
and a service that needs another simply exits until the other is ready,
relying on the supervisor to restart it. That idea, "fail and be
restarted" instead of "wait for your dependencies", is the most
interesting contrast with `rcorder`.

### systemd

systemd replaces shell scripts with declarative unit files that list a
service's command, dependencies, ordering, restart policy and resource
limits. It starts services in parallel wherever the dependency graph
allows, tracks every process of a service using Linux control groups so
that daemonizing cannot escape it, and can start services on demand when
something first connects to them. Its restart settings, such as
`Restart=on-failure` with a start limit, are the same ideas as Lesson 9's
policies and crash-loop limit, made configurable.

The trade-off students should discuss is the one this whole module has
been about. A shell script can do anything, so it is flexible and easy to
read one line at a time, but the system cannot inspect it: it cannot
know, without running the script, what the script depends on. A
declarative file can do less, but the system can read it, compute an
order, start things in parallel and show the dependencies to the
administrator. Neither is simply better.

### Process 1 in containers

A container often runs a single application as its process 1. That
application was not written to be `init`, so it usually does not reap
orphans, and zombies accumulate inside the container. On Linux it may
also ignore signals such as `SIGTERM` that it never installed a handler
for, because the kernel does not apply default signal actions to process
1 in the same way, so the container does not stop cleanly. Tiny init
programs such as `tini` and `dumb-init`, and Docker's `--init` option,
exist to fix exactly those two problems: they run as process 1, start the
application as their child, forward signals to it, and reap orphans.
Students who have built Lesson 7 will recognize every line of what those
programs do.

### Plan 9

Plan 9 from Bell Labs, the successor research system to Unix, also boots
by running shell scripts, but in its own shell, `rc`, which has a
different syntax from the Bourne shell built here. Its machines run
scripts such as `termrc` for terminals and `cpurc` for CPU servers. The
name `rc` in both systems means "run commands", which is why the Plan 9
shell and the Unix boot script share a name without being related.

---

## Discussion questions

These work well for a seminar-style final session, in small groups that
report back.

1. Which of the four duties of process 1 does each design above perform
   in process 1 itself, and which does it delegate to other processes?
2. `rcorder` computes an order before anything starts. runit starts
   everything at once and lets services fail until their dependencies are
   ready. What are the costs of each approach at boot, and when a service
   later crashes?
3. A shell-script init is easy to read and hard to analyze. A declarative
   init is easy to analyze and limited in what it can say. Where would you
   draw the line, and why?
4. This module's `init` learns about events through a timer child,
   because xv6 has no signals. Which parts of the design would become
   simpler with signals, and which would become harder to get right?
5. Why do almost all of these designs keep process 1 small, and put
   supervision or service logic in other processes?
6. Containers often skip a full init system. What do they give up, and
   when does that matter?

---

## The capstone project

Each student, or pair, chooses one project, writes a one-page proposal in
the first week, and presents the result in the final week. Every project
must include tests in the module's style and a short document in
`docs/`.

### Project options

1. **Parallel boot.** Change `/etc/rc` to start services in parallel
   wherever `rcorder`'s graph allows. `rcorder` must print levels instead
   of a single list, and `/etc/rc` starts each level's scripts in the
   background and waits for them before starting the next. Measure boot
   time before and after.
2. **Run levels.** Add System V style run levels on top of `rc.d`, with a
   `runlevel N` command. Compare the result with the BSD design in your
   document.
3. **A logging pipeline.** Give every supervised service a logger: `init`
   connects the service's standard output to a pipe whose other end is a
   second supervised process that writes timestamped lines to a log file,
   as runit's `svlogd` does. Both must be restarted independently.
4. **Environment variables, end to end.** Add an `envp` argument to the
   sentry's `exec`, `export` to the shell, and `envp` to `main` through
   `start.S`. Then use it in `rc.subr` so that services get configuration
   through their environment.
5. **Differential testing.** Write a harness that runs each shell test
   both in xv6-isor's shell and in a POSIX shell on the host, such as
   `dash`, and reports every difference. Classify each difference as a
   bug, an intended simplification, or an xv6 limitation.
6. **A runit-style stage design.** Replace the single `init` with three
   stage scripts and a separate supervision tree, as runit does, keeping
   `init` itself to the four duties only. Compare its size and behavior
   with the version from Lesson 9.
7. **A teacher's tool.** Extend `sh -T` into an interactive explorer: print
   the parse tree, then the expanded words, then each process that would
   be created, for any command line. Use it to write a worked tutorial on
   quoting.

### Milestones

| week | milestone |
|---|---|
| 1 | proposal: what, why, how it will be tested |
| 2 | tests written and failing for the right reasons |
| 3 | implementation passing the tests |
| 4 | document written; presentation given |

---

## Assessment

### Weighting

| component | weight |
|---|---|
| Labs 1 to 9 | 50% |
| Quizzes from the "Check your understanding" sections | 15% |
| Capstone | 35% |

### Rubric for labs

| criterion | excellent | adequate | weak |
|---|---|---|---|
| Behavior (60%) | all tests pass, including edge cases the student added | the given tests pass | some given tests fail |
| Design (25%) | small, clear, follows the lesson's structure; no duplicated logic | works but with avoidable complexity | hard to follow; logic in the wrong place, such as policy in C |
| Written answers (15%) | accurate, in the student's own words, with examples | accurate but brief | missing or incorrect |

### Rubric for the capstone

| criterion | weight | what earns full marks |
|---|---|---|
| Problem and proposal | 10% | a clear goal and a test plan before any code |
| Correctness | 30% | tests cover normal cases, failures and edge cases, and pass |
| Design | 25% | fits the module's principles: mechanism in C, policy in scripts, small and readable |
| Document | 20% | someone who did not write the code can understand it and its limits |
| Presentation | 15% | explains one design decision and one thing that went wrong |

### Final checklist for the whole module

A complete module, at the end of Lesson 9, passes all of these:

- `make test` passes, with the shell tests and boot tests.
- Boot prints the `rc` messages, starts the enabled services, and shows a
  prompt.
- Breaking `/etc/rc` gives a single-user shell; exiting it retries the
  boot.
- `service NAME start`, `stop`, `restart` and `status` work for both
  supervised and unsupervised services.
- A supervised service that is killed comes back with a new pid.
- A service that crashes repeatedly is given up on, with a message.
- `shutdown` stops services in reverse order, and the sentry exits with
  status 0.
- `sh.c` is near 1,500 lines and `init.c` near 400.

---

## Glossary

**Builtin.** A command the shell runs itself, without starting a program.
Commands that change the shell's own state, such as `cd`, must be
builtins.

**Command substitution.** Replacing `$(command)` with the command's
output.

**Crash-loop limit.** A rule that stops restarting a service that has
failed too many times in a short period.

**Daemonize.** For a program to fork, let the parent exit, and continue in
the background. Supervised services should not do this.

**Exit status.** The small integer a process leaves for its parent; 0
means success.

**Expansion.** The steps that turn the words of a command, as typed, into
the arguments it is run with.

**Field splitting.** Breaking the result of an unquoted expansion into
separate words.

**Login shell.** A shell that first reads a profile script, such as
`/etc/profile`.

**Orphan.** A process whose parent has exited. It is re-parented to
process 1.

**Pidfile.** A file holding the pid of a running service.

**Positional parameters.** `$1`, `$2` and so on: the arguments of a script
or function.

**Reap.** To collect a dead child's exit status with `wait`, freeing its
process table entry.

**Reserved word.** A word such as `if` or `done` that has special meaning
only where a command could start.

**Run level.** In System V, a number standing for a system state, with a
set of services for each.

**Runcom.** "Run commands": the origin of the name `rc`, and the name of
4.4BSD `init`'s state that runs `/etc/rc`.

**Single-user mode.** A state in which only a repair shell runs on the
console.

**Supervisor.** A process that starts a service as its child and restarts
it according to a policy when it exits.

**Topological sort.** An ordering of a graph's nodes in which every node
comes after the nodes it depends on; `rcorder`'s algorithm.

**Zombie.** A process that has exited but has not yet been reaped.

---

## Further reading

These are the primary sources behind the history in this module. They
are worth reading in full, and most are short.

- Stephen R. Bourne, *An Introduction to the UNIX Shell*, in the Seventh
  Edition UNIX Programmer's Manual. The shell's own author explaining
  variables, control flow and scripts.
- Brian W. Kernighan and Rob Pike, *The UNIX Programming Environment*
  (1984), especially the chapters on the shell and shell programming.
- Marshall Kirk McKusick and others, *The Design and Implementation of
  the 4.4BSD Operating System* (1996), for the 4.4BSD boot and `init`.
- Luke Mewburn, *The Design and Implementation of the NetBSD rc.d
  System*, USENIX Annual Technical Conference (2001), the paper behind
  Lesson 8.
- The POSIX *Shell Command Language* specification, for the precise rules
  on expansion, quoting and `set -e`.
- Daniel J. Bernstein's daemontools documentation, for the case for
  supervision and against daemonizing.
- Lennart Poettering, *Rethinking PID 1* (2010), the essay that set out
  systemd's design goals.
- The xv6 book, by Russ Cox, Frans Kaashoek and Robert Morris, for the
  original shell and `init` that this module starts from.

Within this repository:

- [docs/processes.md](../../processes.md) for `fork`, `exec`, `exit` and
  `wait`, and how orphans reach `init`.
- [docs/syscall-path.md](../../syscall-path.md) for what each process
  creation costs under the sentry.
- [docs/isolation-landscape.md](../../isolation-landscape.md) for where
  containers and their process 1 fit.
- [docs/security-models.md](../../security-models.md) for why there are no
  users or permissions in any of this.
