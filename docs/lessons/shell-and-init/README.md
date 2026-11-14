# Lesson plan: the shell and init, the Research UNIX and BSD way

This directory is a lesson plan for teaching two things at once with
xv6-isor: the shell as a programming language, and how a Unix system
boots and keeps programs running after boot. The vehicle is a set of
planned changes to two small programs that already exist in the
repository, `user/sh.c` and `user/init.c`. Students make those changes
themselves, one lesson at a time, and each lesson leaves them with a
system that works and can be tested.

The model for the finished system is the classic one from Research UNIX
and the BSDs. In that model, `init` is tiny, and almost every decision
about booting is written as a shell script called `/etc/rc`. Later BSD
systems split that script into a configuration file, a small library and
one script per service, and this plan follows the same path. There is no
cron and no time-based scheduling in this plan, by design: the subject is
what happens at boot and what keeps running after it, not what runs at a
given hour.

Nothing in this directory changes the code. It describes changes for
students, teaching assistants or a future contributor to make. Where it
names a file, function or line in xv6-isor, that reference describes the
repository as it stands today.

---

## Who this is for

The plan assumes a student who has written C, has used a Unix shell
interactively, and has read at least the first chapter of the xv6 book.
It does not assume any prior knowledge of how shells parse their input,
of how `init` works, or of the BSD rc system. A student who has finished
an introductory operating systems course will move through the early
lessons quickly and can spend more time on the exercises. A student who
has never looked inside a shell should expect the early lessons to take
longest, because they introduce ideas that every later lesson depends on.

Teaching assistants should read the whole plan before the first session.
Several design choices in early lessons only make sense once you have
seen how later lessons use them. For example, the exit status added in
Lesson 1 is what lets `init` decide, in Lesson 7, whether the system
booted cleanly.

---

## Learning goals

By the end of the module, a student should be able to do each of the
following.

1. Explain what an exit status is, where it is stored, and why the kernel
   has to carry it from a dead child to its parent.
2. Describe the stages a shell goes through for each command: reading,
   splitting into tokens, parsing into a tree, expanding words, and
   executing.
3. Write a recursive-descent parser for a small language and explain how
   its grammar gives operators their precedence.
4. Explain why `cd`, `exit` and variable assignment must be built into
   the shell instead of being separate programs.
5. Predict the result of quoting and expansion for any command line in
   the subset of the shell language covered here.
6. Write shell scripts with variables, conditionals, loops, `case`
   statements and functions, and debug them with `set -x`.
7. Trace a Unix boot from the first process to a login prompt, naming
   which program makes each decision.
8. Explain the difference between the Research UNIX `/etc/rc`, the BSD
   `rc.d` system, System V run levels and a modern service manager.
9. Write an rc.d service script and control it with a `service` command.
10. Design a supervisor that restarts crashed programs without restarting
    them forever, and explain how it learns that a program has died.

---

## Prerequisites in the repository

Students should build xv6-isor and run its tests before the first lesson.
The commands are `make` and `make test`, described in the top-level
README. They should also skim these existing documents, because the
lessons refer to them:

- [docs/syscall-path.md](../../syscall-path.md), for how a guest system
  call reaches the sentry.
- [docs/processes.md](../../processes.md), for `fork`, `exec`, `exit` and
  `wait` in xv6-isor.
- [docs/filesystem.md](../../filesystem.md), for device files and paths.

---

## The lessons

| lesson | file | subject | main change |
|---|---|---|---|
| 1 | [01-exit-status.md](01-exit-status.md) | exit status and `/dev/null` | the sentry and every user program |
| 2 | [02-evaluator.md](02-evaluator.md) | a shell that returns values and reads scripts | `sh.c` restructured |
| 3 | [03-and-or.md](03-and-or.md) | `&&`, `\|\|` and `!` | a new grammar level |
| 4 | [04-variables-and-quoting.md](04-variables-and-quoting.md) | variables, quoting, parameters | expansion |
| 5 | [05-control-flow.md](05-control-flow.md) | `if`, `while`, `for`, `case`, `test` | compound commands |
| 6 | [06-functions-and-tools.md](06-functions-and-tools.md) | functions, scripts as programs, teaching aids | `sh.c` finished |
| 7 | [07-init-runs-rc.md](07-init-runs-rc.md) | `init` runs `/etc/rc`; single-user mode; shutdown | `init.c` |
| 8 | [08-rc-d.md](08-rc-d.md) | `rc.conf`, `rc.subr`, `rc.d`, `service` | scripts in `fs/etc` |
| 9 | [09-supervision.md](09-supervision.md) | respawning services after boot | `init.c` and helpers |
| 10 | [10-comparisons-and-capstone.md](10-comparisons-and-capstone.md) | other init systems; capstone; assessment | none |

Each lesson file has the same sections, so students and teaching
assistants always know where to look:

- **Objectives** say what the student should be able to do afterwards.
- **Background** gives the history and concepts needed for the lesson.
- **Design** explains the change and the reasons for each choice.
- **Walkthrough** describes the implementation in the order a student
  would write it.
- **Lab** is the hands-on assignment, with the tests that show it is done.
- **Pitfalls** lists the mistakes students make most often.
- **Exercises** are extra problems, from easy to hard.
- **Check your understanding** is a short set of questions for review or
  for a quiz.

---

## Suggested schedule

The module fits into ten weekly sessions of about two hours each, with a
lab assignment due before the next session. Lessons 1 and 2 are the
heaviest in code, and a class that falls behind should give them extra
time rather than rush them. Lessons 3 and 6 are the lightest, and a fast
class can combine Lesson 3 with Lesson 4 and Lesson 6 with Lesson 7.

| week | session | lab due the following week |
|---|---|---|
| 1 | Lesson 1: exit status | `exit(int)` and `wait(int*)` everywhere, tests passing |
| 2 | Lesson 2: the evaluator | scripts run from files; builtins work |
| 3 | Lesson 3: `&&` and `\|\|` | short-circuit operators with tests |
| 4 | Lesson 4: variables | expansion and quoting with tests |
| 5 | Lesson 5: control flow | `if`, `while`, `for`, `case` |
| 6 | Lesson 6: functions | functions and `sh -n` |
| 7 | Lesson 7: init and `/etc/rc` | boot through `/etc/rc`; single-user mode |
| 8 | Lesson 8: rc.d | services started from `rc.d` |
| 9 | Lesson 9: supervision | a crashing service is restarted, then given up on |
| 10 | Lesson 10: comparisons and capstone | capstone project |

---

## Design principles that run through every lesson

Five principles shape the whole plan, and students should hear them more
than once.

**Mechanism in the kernel and in init, policy in scripts.** The kernel
provides exit statuses, processes and files. `init` provides the few
things only process 1 can do: reaping orphans, restarting programs and
shutting the system down. Every decision about *what* to start, *in what
order* and *with which settings* is written in the shell language. This
is the lesson Research UNIX taught, and it is still the design of the BSD
systems, runit and s6.

**Every step leaves a working system.** No lesson ends with a half-built
shell. If a student stops after Lesson 3, they have a shell that is
smaller than the final one but correct in everything it accepts.

**Tests come first.** Each lab begins with the tests that will show it is
finished. Students who write the tests before the code learn the language
they are implementing more quickly, because each test forces a decision
about what a line of shell should do.

**Small enough to read.** The finished `sh.c` should stay near 1,500
lines and `init.c` near 400. If a feature would push past that, it
belongs in an exercise instead.

**Backward compatibility while learning.** Until Lesson 7, `init` behaves
exactly as it does today. A student who breaks `/etc/rc` in Lesson 7
still gets a shell, because single-user mode is part of the design.

---

## What the finished system looks like

At the end of the module, booting xv6-isor looks like this on the
console:

```
init: running /etc/rc
rc: hostname is xv6
rc: starting logd
rc: starting web
rc: running /etc/rc.local
init: multi-user
$ service web status
web is running as pid 9
$ kill 9
$ service web status
web is running as pid 12
$ shutdown
rc.shutdown: stopping web
rc.shutdown: stopping logd
init: halted
```

Behind that transcript are a few new programs and files. `init.c` grows
from 37 lines to a few hundred. `sh.c` gains a real evaluator, variables,
quoting, control flow and functions. The `fs/etc` directory gains `rc`,
`rc.conf`, `rc.subr`, `rc.d/`, `rc.local`, `rc.shutdown` and `profile`.
The sentry gains exit statuses and a null device, and nothing else.

---

## Decisions this plan has already made

The plan settles several questions so that every class builds the same
system. Teachers may change any of them, but each change has knock-on
effects in later lessons.

- **The shell is a Bourne-style shell**, close to a subset of POSIX `sh`.
  It is not the Plan 9 `rc` shell, which has a different syntax. The name
  `/etc/rc` comes from "run commands", and it is a Bourne shell script.
- **`exit` and `wait` change signature**, to `exit(int)` and
  `wait(int *)`, as xv6-riscv did. The old forms disappear.
- **Variables stay inside the shell.** xv6's `exec(path, argv)` has no
  environment argument, and the sentry passes an empty environment.
  Adding one is an exercise in Lesson 4.
- **There are no signals and no `trap` builtin**, because xv6 has no
  signals. `kill` still works, and the shell reports a killed child with
  status 137.
- **`init` learns about events by waiting for children**, including a
  short-lived timer child, instead of by signals or a new system call.
- **There is no cron.** Nothing in this plan runs a program at a given
  time of day.

---

## How to assess the module

Assessment combines the labs, short quizzes and a capstone project.
Lesson 10 contains a full rubric. A reasonable weighting is 50 percent
for the nine labs, 15 percent for the quizzes made from the "Check your
understanding" sections, and 35 percent for the capstone.

Labs are graded mostly by their tests, and partly by reading the code.
The tests check behavior. Reading the code checks that the student kept
the design small and clear, which matters because the whole point of the
module is that someone else can read the result.

---

## A note on history

The shell and `init` are two of the oldest programs in Unix, and their
history is a large part of why they look the way they do. Each lesson's
background section tells the part of that history that explains its
design. The short version is this.

The first Unix shell was written by Ken Thompson, and it could run
commands, redirect input and output, and build pipelines, but it was not
a real programming language. Stephen Bourne's shell, released with
Seventh Edition Research UNIX in 1979, added variables, control flow and
scripts, and almost every Unix shell since descends from it. Kenneth
Almquist's `ash` reimplemented it for the BSDs, and `dash`, the default
`/bin/sh` on Debian, descends from `ash`. POSIX later standardized the
language.

Early Research UNIX `init` started a shell to run `/etc/rc` at boot and
then started a login process on each terminal listed in a file, starting
it again whenever it exited. The BSDs kept that design and gave `init` an
explicit single-user mode for repairs. In 2000, NetBSD replaced the one
large `/etc/rc` with `rc.d`, a directory of small scripts ordered by a
program called `rcorder`, and FreeBSD adopted the same system a few years
later. System V took a different path with run levels and
`/etc/inittab`, and Linux distributions mostly followed System V until
service managers such as Upstart and systemd replaced it.

This plan follows the Research UNIX and BSD line because it is the
simplest design that still shows every idea, and because it fits the
"policy in scripts" principle that runs through the whole module.
