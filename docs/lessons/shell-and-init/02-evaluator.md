# Lesson 2: A shell that returns values and reads scripts

xv6's shell is a beautiful piece of teaching code, and it is also a dead
end for this module. Its central function, `runcmd`, never returns: it
always ends by calling `exec` or `exit`. That design makes the shell
short, but it means the shell can never know whether a command succeeded,
can never run two commands in the same process, and can never read a
script from a file. This lesson restructures the shell around an
evaluator that returns an exit status. Every later shell lesson builds on
that one change.

---

## Objectives

After this lesson, a student should be able to:

1. Name the five stages of running a command line: reading, tokenizing,
   parsing, expanding and executing.
2. Explain why xv6's `runcmd` must fork before it runs anything, and what
   that costs.
3. Write an evaluator that walks a parse tree and returns an exit status.
4. Explain why `cd` and `exit` must be builtins, and why redirections on a
   builtin need special care.
5. Make the shell read commands from a file, from a string, or from the
   console, and explain how it decides whether to print a prompt.
6. Implement a command search path and the exit statuses 126 and 127.

---

## Background

### How xv6's shell works today

The shell in `user/sh.c` has three parts. `main` reads a line with
`getcmd`, checks by string comparison whether the line starts with
`cd `, and otherwise forks. The child calls `parsecmd` to build a tree of
`struct cmd` nodes, and then `runcmd` to execute it. The parent calls
`wait` and loops.

The parse tree has five node types, defined at the top of the file:
`EXEC` for a simple command, `REDIR` for a redirection, `PIPE` for a
pipeline, `LIST` for commands separated by `;`, and `BACK` for a command
ending in `&`. `runcmd` handles each node and never returns. For a `LIST`,
it forks to run the left side, waits, and then runs the right side by
calling itself, which eventually execs or exits. For a `PIPE`, it forks
twice, connects the two children with a pipe, waits for both and exits.

This design is elegant because the forked child can do anything to its
own file descriptors, and the parent shell is never affected. It is also
why `cd` must be special-cased in `main`: a `chdir` in a forked child
changes only the child's directory, which disappears when the child
exits.

### What it cannot do

Because `runcmd` never returns, the shell cannot:

- report or test an exit status, because nothing returns one
- run a builtin anywhere except at the very start of a line, so `cd a; ls`
  runs `cd a` in a child and has no effect
- set a variable in one command and use it in the next, which Lesson 4
  needs
- read a script from a file, because `main` only reads from the console

The fix is to separate *deciding what to do* from *doing it in a new
process*. The evaluator walks the tree in the shell's own process and
forks only when it has to start an external program.

### A little history

Ken Thompson's first Unix shell, used through Sixth Edition Research
UNIX, ran commands and built pipelines, but most of what we now think of
as the shell language was missing. Remarkably, `if` and `goto` were not
part of the shell at all: they were separate programs. The `goto` program
worked by seeking the shell's input file to the line with the matching
label, which only made sense because the shell read scripts from its
standard input. Filename wildcards were expanded by another separate
program, `/etc/glob`.

Stephen Bourne's shell, in Seventh Edition in 1979, moved control flow,
variables and expansion into the shell, and made scripts first-class.
The evaluator in this lesson is the first step from Thompson's design to
Bourne's.

---

## Design

### The five stages

Every shell processes a command line in the same five stages, and this
lesson makes the stages explicit in the code.

1. **Read.** Get the next line of input, from a file, a string or the
   console.
2. **Tokenize.** Split the line into words and operators such as `|`,
   `;` and `>`. The existing `gettoken` does this.
3. **Parse.** Build a tree from the tokens, following the grammar. The
   existing `parseline`, `parsepipe` and `parseexec` do this.
4. **Expand.** Replace variables and remove quotes in each word. Today
   there is nothing to expand; Lesson 4 adds this stage.
5. **Execute.** Walk the tree and run it. This is the new `eval`.

Students should be able to point to the function that implements each
stage. Keeping them separate is what makes the later lessons possible:
Lesson 3 changes only the parser, Lesson 4 only the expansion stage.

### The evaluator

The new central function is:

```c
int eval(struct cmd *cmd);   // returns the exit status
```

It handles each node type like this:

| node | what `eval` does | forks? |
|---|---|---|
| `EXEC`, builtin | runs the builtin in the shell's own process | no |
| `EXEC`, external | forks; the child execs; the parent waits and returns the status | yes |
| `REDIR` | applies the redirection, evaluates the inner command, undoes the redirection | only if the inner command does |
| `LIST` (`;`) | evaluates the left side, then the right side; returns the right side's status | no |
| `PIPE` | forks one child per side, connects them, waits for both; returns the right side's status | yes |
| `BACK` (`&`) | forks; the child evaluates the command and exits; the parent records the pid and returns 0 | yes |

`runcmd` survives as a helper for the forked child: it calls `eval` and
then exits with the status `eval` returned. The children of a pipeline
use it, because each side of a pipe has to run in its own process.

### Builtins

A builtin is a command the shell runs itself instead of executing a
program. This lesson adds three.

- `cd dir` changes the shell's directory. With no argument it goes to `/`,
  because xv6 has no home directories.
- `exit [n]` ends the shell with status `n`, or with the status of the
  last command if `n` is missing.
- `:` does nothing and returns 0. It looks like a toy, but it is useful
  in loops in Lesson 5 and is traditional.

The rule for when something must be a builtin is worth stating clearly:
**a command must be a builtin if it has to change the shell's own state.**
The directory, the variables and whether the shell keeps running are all
the shell's own state. A program run by `exec` gets a copy of that state
and cannot change the original.

A builtin is found by looking up the first word in a small table of names
and functions. The table is checked before searching for a program, so a
builtin always wins. Students can test this by creating a program named
`cd` in the root directory: the shell should still run its builtin.

### Redirections on builtins

An external command's redirections are easy, because they happen in the
forked child. A builtin runs in the shell itself, so `cd /x > out`
would change the shell's own standard output and leave it changed. The
shell must save the file descriptor before redirecting and put it back
afterwards. The pattern is:

```c
saved = dup(fd);        // remember where fd pointed
close(fd);
open(file, mode);       // gets the lowest free descriptor, which is fd
status = eval(inner);
close(fd);
dup(saved);             // put the original back
close(saved);
```

This works because `open` and `dup` always return the lowest free file
descriptor, a rule xv6 shares with every Unix system. It is a good moment
to point out how much of Unix depends on that one rule.

### Reading input

The shell should be able to read commands from three places:

- **The console**, interactively, as today, when started as `sh`.
- **A file**, when started as `sh script` or `sh script arg1 arg2`.
- **A string**, when started as `sh -c 'commands'`.

A small structure describes the input source: a file descriptor, or a
pointer into a string, plus a buffer. The reading function returns one
line at a time from whichever source is active. In Lesson 5, the parser
will sometimes need more lines to finish a command, and this structure is
where it gets them.

The fixed 100-byte line buffer in today's `main` becomes a buffer that
grows with `malloc`, because scripts have long lines. `MAXARGS`, 10 today,
grows to 32, which matches the sentry's own limit on `exec` arguments.

### Prompting

A shell should print a prompt only when a person is typing. When it reads
a script, a prompt would be mixed into the script's output. Real shells
decide by checking whether standard input is a terminal. In xv6-isor, a
program can call `fstat` on file descriptor 0 and check whether its type
is `T_DEV`, which is true for the console. The shell prints the prompt
only if it is reading from file descriptor 0, that descriptor is a
device, and no script or `-c` string was given.

### Comments

A `#` at the start of a word begins a comment that runs to the end of the
line. The words "at the start of a word" matter: `echo a#b` prints `a#b`.
The tokenizer handles comments, and it is the only change to the
tokenizer in this lesson.

### Finding programs: a search path

Today, `ls` works only in `/`, because `exec("ls")` looks in the current
directory. After `cd d`, a user must type `/ls`. Real shells search a
list of directories called the path.

In this lesson, the path is a fixed list containing only `/`. The rule is
the traditional one: if the command name contains a `/`, the shell uses it
as given; otherwise it tries each directory in the path in order. In
Lesson 4, the path becomes the value of a `PATH` variable that scripts can
change.

The search happens in the child, after `fork`, because the child is about
to call `exec` anyway. If every attempt fails, the child prints an error
and exits with status 127 if no file was found, or 126 if a file was found
but could not be executed. The sentry's `exec` returns -1 in both cases,
so the shell tells them apart by trying to `open` the file.

### Special parameters, early

Two values are so useful that this lesson adds them before Lesson 4 adds
real variables. `$?` is the exit status of the last command, and `$!` is
the pid of the last background command. The tokenizer replaces exactly
these two forms. Lesson 4 replaces this special case with the general
expansion stage.

### The status of the shell itself

When the shell reaches the end of its input, it exits with the status of
the last command it ran. That makes `sh script` behave like any other
program: its exit status tells the caller whether the script's last
command succeeded. `init` will rely on this in Lesson 7.

---

## Walkthrough

### Step 1: write the tests

The module keeps shell tests in `tests/sh/`, one script per test with a
file of expected output beside it. `test.sh` runs each script with the
new shell and compares its output. Tests for this lesson include:

- `cd d; /ls` lists the contents of `d`, proving `cd` works mid-line.
- `false; echo $?` prints 1, and `true; echo $?` prints 0.
- `nosuchcommand; echo $?` prints 127.
- `echo a # comment` prints `a`.
- `echo a#b` prints `a#b`.
- `sh -c 'exit 3'; echo $?` prints 3.
- A script that runs `exit 5` makes `sh script` exit with 5.
- `cd /d > out` leaves the shell's standard output unchanged.

`true` and `false` come from Lesson 1's exercises; if a class skipped
them, they are three lines each.

### Step 2: add the input source

Write the structure for input sources and a function that returns the
next line. Keep the old behavior for the console. Test it by making
`sh -c 'echo hi'` work before changing anything else.

### Step 3: write `eval`

Write `eval` for `EXEC` first, with no builtins: fork, exec, wait, return
the status. Then add `LIST`, `REDIR`, `PIPE` and `BACK`, one at a time,
running the tests after each. Then turn the old `runcmd` into the small
helper that calls `eval` and exits.

### Step 4: add builtins

Add the table and the three builtins. Remove the string comparison for
`cd` from `main`. Add the save-and-restore pattern for redirections on
builtins.

### Step 5: add the path search, comments and `$?`

These are independent and small, and students can add them in any order.

### Step 6: update `init` and the tests

`init` still runs `sh` with no arguments, so it doesn't need to change.
`test.sh` already drives the shell through its standard input, so the
existing end-to-end tests should still pass. If they do not, the
evaluator has changed behavior that users depend on, and that is a bug.

---

## Lab

**Assignment.** Restructure `sh.c` around `eval` as described, add the
builtins, script and `-c` input, prompting rules, comments, the path
search and `$?`/`$!`.

**Done when:**

- All tests in `tests/sh/` for this lesson pass.
- `make test` still passes.
- `sh.c` contains no string comparison for `cd` in `main`.
- A one-page written explanation names the function for each of the five
  stages and says which node types fork.

---

## Pitfalls

- **Waiting for the wrong child.** `wait` returns *any* child. If a
  background job finishes while the shell waits for a foreground
  command, the shell may collect the wrong one. The fix is to loop until
  `wait` returns the pid the shell is waiting for, remembering the
  statuses of others. This is the same problem `init` has, and it comes
  back in Lesson 9.
- **Leaking file descriptors into children.** After setting up a pipe, the
  parent must close both ends. Otherwise the reader never sees end of
  file, because a write end is still open somewhere. The old `runcmd`
  gets this right; the new code must too.
- **Running builtins in a child by accident.** If the builtin check is
  placed after `fork`, `cd` silently does nothing. Check the table before
  forking.
- **Printing the prompt into a script's output.** Test with
  `sh script > out` and check that `out` has no `$ ` in it.
- **Forgetting that `exit` is a builtin.** Without it, `exit` in a script
  tries to run a program called `exit`, which does not exist, so the
  script keeps going.

---

## Exercises

1. **Easy.** Add a `pwd` builtin. xv6 has no system call to ask for the
   current directory, so the shell must remember it. Where is the right
   place to update it, and what happens with `cd ..`?
2. **Easy.** Make `exit` with a non-number argument print an error and
   exit with status 2.
3. **Medium.** Add `jobs`, which lists background jobs the shell started
   and has not yet collected. Why can't it show jobs started by other
   shells?
4. **Medium.** Make the shell report `[pid] Done` when a background job
   finishes, the next time it prints a prompt. Where does the shell learn
   that the job is done?
5. **Hard.** The `LIST` node forks for the left side today. After this
   lesson it does not. Write a script whose behavior differs between the
   two designs, and explain why.
6. **Hard.** Thompson's `goto` worked by seeking the shell's standard
   input. Implement `goto label` as an external program that seeks file
   descriptor 0, and explain why it works only when the shell reads its
   script from standard input, not from a named file.

---

## Check your understanding

1. Why can't xv6's `runcmd` return a status?
2. What are the five stages, and which existing function implements each
   of the first three?
3. What is the rule for when a command must be a builtin?
4. Why does `cd /x > out` need the shell to save and restore a file
   descriptor?
5. Which rule about file descriptors makes the save-and-restore pattern
   work?
6. How does the shell decide whether to print a prompt?
7. When does the path search not happen at all?
8. What is the difference between exit statuses 126 and 127?
9. What exit status does `sh script` have, and why does `init` care?
