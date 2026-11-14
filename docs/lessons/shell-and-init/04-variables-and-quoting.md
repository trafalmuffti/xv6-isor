# Lesson 4: Variables, quoting and expansion

This lesson turns the shell into a language that can remember things.
It adds variables, the parameters a script receives, quoting, command
substitution and filename wildcards. Together these make up the
**expansion** stage that Lesson 2 left empty. Expansion is where most
shell bugs live, in real scripts as much as in student shells, so this
lesson spends as much time on predicting behavior as on implementing it.

---

## Objectives

After this lesson, a student should be able to:

1. Explain the difference between a shell variable and an environment
   variable, and why xv6-isor has only the first kind.
2. List the expansion steps in the order the shell applies them.
3. Predict the words a command line produces, for any combination of
   variables, quotes and wildcards covered here.
4. Explain why expansion has to happen when a command runs, not when it
   is parsed.
5. Explain why a variable set inside one stage of a pipeline is lost.
6. Implement command substitution and explain the processes it creates.
7. Debug a script with `set -x`, and make a script stop at its first
   error with `set -e`.

---

## Background

### Shell variables and the environment

A **shell variable** is a name and a value stored inside the shell's own
memory. When the shell runs a program, the program does not see shell
variables at all.

An **environment variable** is a name and a value that is copied into
every program the shell starts. On Unix, the environment is passed by the
kernel as a third argument to `execve`, next to the argument list. A
shell decides which of its variables go into the environment; the
`export` builtin is how a script says so.

xv6's `exec(path, argv)` has no third argument, and the sentry passes an
empty environment to every guest. So in this module, **every variable is
a shell variable**, and `export` does not exist. This is a deliberate
choice, and students should understand it as a lesson rather than a gap:
it shows exactly what the environment is for. Configuration that one
program needs from another has to travel some other way, as an argument,
in a file, or through a pipe. Exercise 6 adds a real environment.

### Expansion

When the shell runs a command, it does not use the words exactly as they
were typed. It first expands them. A word such as `$HOME/notes` is
expanded by replacing `$HOME` with the variable's value. A word such as
`*.c` is expanded into a list of file names. Quotes control which of
these steps apply, and then the quotes themselves are removed.

POSIX lists the expansion steps in a fixed order. The order used in this
module is a subset of it:

1. **Parameter expansion**: `$name`, `${name}`, `${name:-word}`, and the
   special parameters such as `$?` and `$1`.
2. **Command substitution**: `$(command)`, replaced by the command's
   output.
3. **Field splitting**: the results of steps 1 and 2, where they were not
   quoted, are split into separate words at spaces, tabs and newlines.
4. **Pathname expansion**: words containing unquoted `*`, `?` or `[` are
   replaced by matching file names, if there are any.
5. **Quote removal**: the quote characters that are left are removed.

The order explains most surprising behavior. For example, a variable
whose value contains a space becomes two words because field splitting
comes after parameter expansion, and a variable whose value is `*`
becomes a list of files because pathname expansion comes after that.

### A little history

Variables, quoting and command substitution all arrived with the Bourne
shell in Seventh Edition. The Bourne shell wrote command substitution with
backquotes, `` `command` ``. The `$(command)` form came from the Korn
shell and was standardized by POSIX, because it nests cleanly and the
backquote form does not. This module implements only `$(...)`.

---

## Design

### Variables

The shell keeps a table of variables, each a name and a string value.
Names start with a letter or underscore and contain letters, digits and
underscores, as in POSIX. A table of 64 entries with linear search is
enough, and keeps the code obvious.

**Assignment** is a word of the form `name=value` at the start of a
command. Three cases need decisions.

- **A line that is only assignments**, such as `a=1 b=2`, sets the
  variables in the shell.
- **Assignments before a builtin or function**, such as `x=1 f`, set the
  variables only while that command runs, and then restore the old
  values. This matches POSIX for functions and is easy to build.
- **Assignments before an external program**, such as `x=1 prog`, would in
  POSIX put `x` into `prog`'s environment. Here there is no environment,
  so the shell prints a warning and runs the program without it. That
  warning is a deliberate teaching device: students who see it remember
  why environments exist.

The value of an assignment is itself expanded, but it is not split into
words and not used for pathname expansion. So `a=$b` copies `b`'s value
exactly, even if it contains spaces.

### Parameter expansion

The forms supported are:

| form | meaning |
|---|---|
| `$name`, `${name}` | the value of `name`, or empty if unset |
| `${name:-word}` | the value, or `word` if `name` is unset or empty |
| `${name:=word}` | as above, and also assign `word` to `name` |
| `$?` | the status of the last command |
| `$!` | the pid of the last background command |
| `$$` | the shell's own pid |
| `$#` | the number of positional parameters |
| `$0` | the name of the script, or `sh` |
| `$1` … `$9` | the positional parameters |
| `$@`, `$*` | all positional parameters (see below) |

The `${name:-word}` form earns its place because Lesson 8 needs it for
configuration defaults, such as `${web_enable:-NO}`. The braces in
`${name}` are needed when a name is followed by letters, as in
`${name}s`.

### Positional parameters

When the shell runs `sh script a b c`, the script sees `$0` as `script`,
`$1` as `a`, and so on, and `$#` as 3. Three builtins manage them:

- `shift [n]` drops the first `n` parameters, 1 by default.
- `set -- a b c` replaces them all.
- Functions in Lesson 6 get their own set while they run.

The difference between `$@` and `$*` appears only inside double quotes.
`"$*"` joins all parameters into one word, separated by spaces. `"$@"`
produces one word per parameter, exactly as given. Scripts almost always
want `"$@"`, because it keeps arguments with spaces intact; Lesson 8's
`rc.subr` passes its arguments along this way.

### Quoting

Three kinds of quoting control expansion:

| quoting | what it prevents | example | result |
|---|---|---|---|
| backslash `\c` | any special meaning of the next character | `echo \$a` | `$a` |
| single quotes `'…'` | every expansion; nothing is special inside | `echo '$a  *'` | `$a  *` |
| double quotes `"…"` | field splitting and pathname expansion; `$` still works | `echo "$a  *"` | value of `a`, two spaces, `*` |

Inside double quotes, a backslash is special only before `$`, `"`, `\`
and a newline. Students should learn the table above by heart, because
almost every quoting question reduces to it.

### Where expansion happens in the code

The tokenizer no longer cuts a word at the first space inside quotes. It
returns the word with its quotes still in it, such as `"a b"`. The
parser stores those raw words in the tree. Expansion happens in `eval`,
just before a command runs, by a function with this shape:

```c
int expand(char *word, char **out, int max);  // returns number of fields
```

It turns one raw word into zero or more finished words. Zero is a real
possibility: an unquoted variable that is empty produces no word at all,
so `echo $unset x` prints just `x`.

**Why expansion must happen at run time.** In Lesson 5, a loop such as
`for f in a b; do echo $f; done` parses its body once and runs it twice.
If `$f` were expanded at parse time, it would have the same value both
times. More simply, `a=1; echo $a` is one line: the parser sees both
commands before either runs, so `$a` has no value yet at parse time. This
is one of the most important ideas in the lesson.

### Command substitution

`$(command)` runs the command and replaces itself with the command's
output, with trailing newlines removed. The shell:

1. creates a pipe
2. forks; the child points its standard output at the pipe, evaluates the
   command and exits
3. reads everything from the pipe until end of file
4. waits for the child, and sets `$?` to its status
5. strips trailing newlines and inserts the text

Students should count processes: `x=$(ls | wc)` creates a child for the
substitution, and that child creates two more for the pipeline. The
`-strace` option shows them all.

Lesson 8 depends on command substitution, for example to read a pid
from a file with `pid=$(cat /var/run/web.pid)`.

### Pathname expansion

A word containing an unquoted `*`, `?` or `[...]` is a pattern. The shell
reads the directory and replaces the word with the sorted list of
matching names. `*` matches any string, `?` matches one character, and
`[abc]` matches one of the listed characters. Names beginning with `.`
match only if the pattern itself starts with `.`, which is the Unix rule
that hides dot files.

Two rules keep the implementation small:

- Patterns are allowed only in the **last component** of a path, so
  `/etc/rc.d/*` works but `/*/rc` does not. Lesson 8 needs nothing more.
- If nothing matches, the word is left as it is, which is the POSIX
  behavior. So `echo *.none` prints `*.none`.

Directories are read in xv6's on-disk format, `struct dirent`, which the
sentry builds for each directory (see
[filesystem.md](../../filesystem.md)). Names in it are at most 14
characters long, and a name of exactly 14 characters has no terminating
zero, so the matcher must copy names out carefully.

### Builtins added in this lesson

| builtin | purpose |
|---|---|
| `set -x`, `set +x` | print each command after expansion, before it runs, prefixed by `+ ` |
| `set -e`, `set +e` | exit as soon as a command fails, with some exceptions (below) |
| `set -u`, `set +u` | treat expanding an unset variable as an error |
| `set -- args` | replace the positional parameters |
| `shift [n]` | drop positional parameters |
| `unset name` | remove a variable |
| `read name…` | read a line from standard input and assign its words to the names |
| `. file` | read and run commands from `file` in the current shell |

**`set -e` has exceptions**, and they confuse even experienced script
writers. A failing command does not stop the script if it is the left
side of `&&` or `||`, if it is negated with `!`, or, after Lesson 5, if it
is the condition of an `if` or `while`. Without those exceptions, a
script could never test anything. Students should write a script that
demonstrates each exception.

**`.` (source)** runs a file inside the current shell, so variables it
sets remain afterwards. That is exactly the opposite of `sh file`, which
runs the file in a new process. Lesson 8 reads configuration with
`. /etc/rc.conf` for this reason.

**`read`** reads one byte at a time until a newline, so that it never
consumes input meant for the next command. That is slow, and it is also
exactly what POSIX shells do when reading from a pipe, for the same
reason.

### `PATH` and the prompt

The fixed search path from Lesson 2 becomes the variable `PATH`, a list
of directories separated by `:`. It starts as `/`, and `/etc/profile`
will set it in Lesson 7. The prompt becomes the variable `PS1`, which
starts as `$ `.

---

## Walkthrough

### Step 1: write the tests

Expansion has many edge cases, so this lesson has the largest test set.
A selection:

- `a=hello; echo $a` prints `hello`.
- `a='x  y'; echo $a` prints `x y` (split, then joined by `echo`).
- `a='x  y'; echo "$a"` prints `x  y` (not split).
- `echo '$a'` prints `$a`.
- `echo "${unset:-dflt}"` prints `dflt`.
- `echo $unset x` prints `x`.
- `set -- 'a b' c; echo $#` prints 2.
- After `set -- 'a b' c`, a helper program that prints its argument
  count reports 2 for `"$@"` and 1 for `"$*"`. Loops arrive in Lesson 5,
  so the helper is the simplest way to see the difference now.
- `x=$(echo hi); echo $x` prints `hi`.
- `echo /etc/rc.d/*` lists the scripts, and `echo /nothing*` prints
  `/nothing*`.
- `echo x | read v; echo "[$v]"` prints `[]`.
- `set -e; false; echo no` prints nothing.
- `set -e; false || true; echo yes` prints `yes`.

### Step 2: change the tokenizer to keep quotes

The tokenizer must treat a quoted space as part of a word. It must also
handle `$(`, which can contain spaces and even nested quotes, by counting
parentheses until the matching `)`.

### Step 3: write the variable table and builtins

Write the table, assignment, `unset`, `set` and `shift`. Make `$?`, which
Lesson 2 special-cased in the tokenizer, an ordinary special parameter.

### Step 4: write `expand`

Build it in the order of the steps: parameters, then substitution, then
splitting, then pathnames, then quote removal. Keep a flag on each
character recording whether it was quoted, because splitting and
pathname expansion apply only to unquoted characters. That flag is the
key idea of the implementation, and students who skip it end up with a
shell that splits quoted spaces.

### Step 5: call `expand` from `eval`

Expand every word of an `EXEC` node, and the file name of every `REDIR`
node, just before running it. A redirection target that expands to zero
or several words is an error, reported as "ambiguous redirect".

### Step 6: add `read`, `.`, `set -x`, `set -e` and `PATH`

These are each small once `expand` exists.

---

## Lab

**Assignment.** Implement the expansion stage as described.

**Done when:**

- All tests in `tests/sh/` pass, including the new ones.
- A table, handed in, shows the words produced by ten command lines
  chosen by the teaching assistant, predicted by hand and then checked
  with `set -x`.
- A paragraph explains why `echo x | read v` leaves `v` empty.

---

## Pitfalls

- **Expanding at parse time.** It seems to work until Lesson 5, when every
  loop body sees the same value.
- **Splitting quoted text.** Without a "quoted" flag per character,
  `"$a"` is split anyway.
- **Running `read` in a pipeline and expecting the variable afterwards.**
  Each stage of a pipeline is a separate process. The variable is set in
  the child that ran `read`, which then exits. This is the most famous
  surprise in shell programming, and real shells behave the same way.
- **Reading more than one line.** A `read` that reads a whole buffer
  steals the next line of a script, which then never runs.
- **Forgetting trailing-newline stripping.** Without it, `x=$(echo hi)`
  gives `x` a value ending in a newline, and comparisons fail later.
- **Matching 14-character names.** Names that fill the dirent field have
  no terminating zero.
- **Sorting.** Pathname expansion results must be sorted, or `for f in
  /etc/rc.d/*` starts services in a different order on different runs.

---

## Exercises

1. **Easy.** Add `${name:+word}`, which gives `word` only if `name` is set
   and not empty. Where would a script use it?
2. **Easy.** Add `${#name}`, the length of the value.
3. **Medium.** Add backquote command substitution. Show a nested command
   substitution that is easy with `$(...)` and awkward with backquotes.
4. **Medium.** Add an `IFS` variable that controls field splitting, and
   use it with `read` to split a line on `:`.
5. **Hard.** Allow patterns in every component of a path, so `/*/rc`
   works. What makes this harder than the last-component case?
6. **Hard.** Add a real environment. Extend the sentry's `exec` with a
   third argument, pass it through to the host's `execve`, add `export`
   to the shell, and make `start.S` pass `envp` to `main`. Then write the
   paragraph this lesson promised: what the environment is for.
7. **Hard.** Implement `set -e` with every POSIX exception, then find a
   short script in which its behavior surprises you, and explain it.

---

## Check your understanding

1. What is the difference between a shell variable and an environment
   variable?
2. List the five expansion steps in order.
3. Why does `a='x  y'; echo $a` print `x y` with one space?
4. Why must expansion happen when a command runs, not when it is parsed?
5. What does `echo $unset x` print, and why does `$unset` produce no word?
6. What is the difference between `"$@"` and `"$*"`?
7. Why does `echo x | read v; echo $v` print an empty line?
8. Why does `. file` keep variables that `sh file` would lose?
9. Why does `read` read one byte at a time?
10. When does `set -e` not stop a script after a failing command?
