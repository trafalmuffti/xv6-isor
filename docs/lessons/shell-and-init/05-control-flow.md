# Lesson 5: Control flow

With exit statuses, `&&` and variables in place, the shell is ready for
real control flow: `if`, `while`, `until`, `for`, `case`, and the `test`
command that most conditions use. These constructs are what make the
shell a programming language rather than a command launcher, and they are
what `/etc/rc` is written in. This lesson also makes the parser read more
than one line for a single command, which changes how input reaches the
parser.

---

## Objectives

After this lesson, a student should be able to:

1. Write and read scripts that use `if`, `while`, `until`, `for` and
   `case`.
2. Explain why the condition of an `if` is a command, not an expression.
3. Implement compound commands in a recursive-descent parser, including
   reserved words that are only special in some positions.
4. Make a parser ask for more input when a command is incomplete.
5. Implement `test` and `[` as a builtin, and explain why it was once a
   separate program.
6. Explain the difference between `{ …; }` and `( … )`.

---

## Background

### Conditions are commands

In most languages, `if` tests an expression. In the shell, `if` runs a
command and tests its exit status. This is the idea that unifies the
whole language:

```sh
if mkdir /tmp/x; then echo made; fi     # mkdir is the condition
if test -d /tmp/x; then echo dir; fi    # so is test
if ! ls /none; then echo missing; fi    # and a negated pipeline
```

There is no special expression syntax for the condition. Anything that
can run can be a condition, and its success or failure decides the
branch. Students who absorb this stop thinking of `[` as syntax and start
reading it as what it is: a command named `[`.

### The `test` command

Because conditions are commands, the shell needs a command that does
comparisons. That command is `test`, also spelled `[`, in which case its
last argument must be `]`. In Seventh Edition, `test` was a separate
program in `/bin`, and `[` was a link to the same program. Modern shells
build it in because it is used so often, and because running a separate
program for every comparison in a loop is slow. On xv6-isor, where every
program start costs many intercepted system calls (see
[syscall-path.md](../../syscall-path.md)), the difference is large enough
to measure.

### Algol in disguise

Stephen Bourne had worked on an Algol 68 compiler before writing his
shell, and the syntax shows it. The closing keywords are the opening ones
spelled backwards: `if` … `fi`, `case` … `esac`. Loops end with `done`
rather than `od`, because `od` was already the name of the octal dump
program. The source code of the original Bourne shell went further and
used C preprocessor macros to make C look like Algol. That source is
often told as one of the inspirations for the International Obfuscated C
Code Contest, a story worth telling in class as folklore rather than as
documented fact.

---

## Design

### The grammar

The `command` rule from Lesson 3 gains compound commands:

```
command   ::= simple | compound redirections*
compound  ::= "{" list "}"
            | "(" list ")"
            | "if" list "then" list ( "elif" list "then" list )* ( "else" list )? "fi"
            | "while" list "do" list "done"
            | "until" list "do" list "done"
            | "for" NAME ( "in" word* )? sep "do" list "done"
            | "case" word "in" ( pattern ( "|" pattern )* ")" list ";;" )* "esac"
list      ::= line, but newlines also separate commands
sep       ::= ";" | newline
```

Two things in this grammar are new for students.

- **Lists can span lines.** Inside a compound command, a newline works
  like `;`. So a newline must become a token, not just white space.
- **Compound commands take redirections.** `while read line; do …; done
  < file` redirects the whole loop. That falls out of the grammar for
  free, because redirections attach to `command`.

### Reserved words

Words such as `if`, `then`, `do` and `done` are **reserved words**. They
are special only where a command could start. So `echo if then fi`
prints three words, while `if` at the start of a line begins an `if`
statement. The tokenizer cannot decide this alone, because it does not
know where a command starts. The usual solution, which this lesson uses,
is for the parser to look at a word token and treat it as reserved only
when it is expecting the start of a command.

### New node types

| node | fields | `eval` behavior | status |
|---|---|---|---|
| `IF` | conditions, bodies, else body | runs each condition in turn; runs the body of the first that succeeds, else the else body | the body's status, or 0 if nothing ran |
| `WHILE` / `UNTIL` | condition, body | repeats the body while the condition succeeds (or fails, for `until`) | the last body's status, or 0 |
| `FOR` | variable, words, body | expands the words, then assigns each in turn and runs the body | the last body's status, or 0 |
| `CASE` | word, patterns, bodies | expands the word, runs the body of the first matching pattern | the body's status, or 0 |
| `BRACE` | list | runs the list in the current shell | the list's status |
| `SUBSHELL` | list | forks; the child runs the list and exits | the child's status |

`FOR` expands its word list once, before the first iteration, but
expands each command in the body afresh every time. Lesson 4 explained
why expansion has to wait until run time; this is where that decision
pays off.

`CASE` patterns use the same matcher as pathname expansion from Lesson
4, applied to a string instead of to file names. Reusing it shows that
wildcards are a general idea, not something tied to directories.

### `{ }` and `( )`

Both group commands. The difference is the process:

- `{ cd /x; ls; }` runs in the current shell, so the `cd` lasts after the
  group ends.
- `( cd /x; ls )` runs in a forked child, so the `cd` disappears with it.

The braces are reserved words, so they need spaces around them and a `;`
or newline before the closing brace. The parentheses are operators, so
they don't. This asymmetry is a historical accident that every shell
user eventually trips over, and it is worth pointing out explicitly.

xv6's shell already supports `( … )` through `parseblock`. Today it does
not fork for the group itself, because every command runs in a forked
child anyway. With the evaluator from Lesson 2, the `SUBSHELL` node must
fork explicitly, or the group would leak its `cd` into the shell.

### `break` and `continue`

`break` leaves the innermost loop, and `continue` jumps to its next
iteration. Both take an optional count for nested loops. The simplest
implementation sets a flag in the shell's state that every loop and every
list checks after running a command. A `longjmp` would also work, but xv6
has no `setjmp`, and the flag version is easier to read.

### `test` and `[`

The builtin supports these forms, which cover what `/etc/rc` needs:

| expression | true when |
|---|---|
| `-e file` | the file exists |
| `-f file` | it exists and is a regular file |
| `-d file` | it exists and is a directory |
| `-z s` / `-n s` | the string is empty / not empty |
| `s1 = s2`, `s1 != s2` | the strings are equal / different |
| `n1 -eq n2`, `-ne`, `-lt`, `-le`, `-gt`, `-ge` | integer comparisons |
| `! expr` | the expression is false |
| `expr -a expr`, `expr -o expr` | and / or, with `-a` binding tighter |

File tests use `stat`, which uses `fstat`, whose `type` field from xv6's
`struct stat` distinguishes files, directories and devices. Note that
there is no `-x`, because xv6 has no permission bits (see
[security-models.md](../../security-models.md)).

`[` is the same builtin, and it checks that its last argument is `]`.
Students sometimes find it odd that `]` is just an argument. The best way
to make it click is to run `[ 1 = 1` without the bracket and read the
error message.

### Reading more input

`if true` followed by a newline is not a complete command. The parser
must ask for another line, and an interactive shell should show a
different prompt, called the secondary prompt and stored in `PS2` (by
default `> `), so the user knows the shell is waiting.

This changes how the parser gets input. Until now, `main` read a line and
handed it to the parser. Now, the parser asks the input source from
Lesson 2 for more lines whenever it runs out of tokens in the middle of a
command. The input source returns end of file if there is no more input,
and the parser reports "unexpected end of file". This is a small change
to the code, but a large change to its structure, and students should
draw the call graph before and after.

---

## Walkthrough

### Step 1: write the tests

Tests include:

- `if true; then echo a; else echo b; fi` prints `a`.
- An `if` with `elif` that picks the second branch.
- `if false; then echo a; fi; echo $?` prints 0.
- A `while` loop that counts with a variable and `test`, printing 1 to 3.
- `until` doing the same.
- `for x in a b c; do echo $x; done` prints three lines.
- `for f in /etc/*; do echo $f; done` lists files.
- `case abc in a*) echo yes;; *) echo no;; esac` prints `yes`.
- `{ cd /; }; /ls` lists `/`, and `( cd /etc ); /ls` also lists `/`.
- `while true; do break; done; echo out` prints `out`.
- `echo if then fi` prints `if then fi`.
- A script whose `if` spans five lines.
- `[ -d /etc ] && echo dir` prints `dir`.

### Step 2: make newlines tokens

Change the tokenizer to return a newline token. Change the existing
parser so a newline ends a command at the top level, exactly as before.

### Step 3: make the parser pull input

Give the parser access to the input source. When it needs a token and has
none, it reads another line, with the secondary prompt when interactive.

### Step 4: add the compound commands

Add them one at a time, `if` first, with their tests. Reserved words are
checked in the function that parses a command.

### Step 5: add `test`, `break` and `continue`

`test` is a standalone function that takes an argument list and returns
a status. Writing it as its own small parser for expressions makes a nice
second example of recursive descent.

---

## Lab

**Assignment.** Add the compound commands, reserved words, multi-line
input, `break`, `continue` and `test`/`[`.

**Done when:**

- All tests pass, old and new.
- A script `/bin/countdown` (or `/countdown`, depending on `PATH`) counts
  down from its first argument to 1 using `while` and `test`, and exits
  with status 2 if no argument is given.
- A paragraph explains why `if`'s condition is a command, using an example
  where the condition is not `test`.

---

## Pitfalls

- **Treating `then` as reserved everywhere.** `echo then` must print
  `then`.
- **Forgetting the semicolon before `}`.** In the shell's own grammar,
  `{ echo a }` is not finished, because `}` is an argument to `echo`. A
  student's test that expects it to work is wrong, and real shells agree.
- **Expanding the `for` body once.** Every iteration must expand the body
  afresh, or every pass sees the first value.
- **Leaking `break` out of a function.** In Lesson 6, a `break` inside a
  function called from a loop should not break the caller's loop in this
  shell. Decide the rule now and test it.
- **Status of an empty `if`.** An `if` whose conditions all fail and that
  has no `else` returns 0, not the status of the last condition. That is
  POSIX, and scripts depend on it.
- **Reading past the end of a script.** When the input source returns end
  of file in the middle of a compound command, the shell must report an
  error, not loop forever asking for more.

---

## Exercises

1. **Easy.** Write a script that prints the names of every directory in
   `/`, using `for`, a wildcard and `test -d`.
2. **Easy.** Rewrite the countdown with `until`.
3. **Medium.** Add here-documents (`<<WORD`), which feed the following
   lines of the script to a command's standard input until a line
   containing only `WORD`. Where in the parser must they be read, and why
   is that awkward?
4. **Medium.** Add the `-nt` (newer than) test. What does it need from
   `fstat` that xv6 doesn't provide, and how could the sentry supply it?
5. **Hard.** Write `test` as a separate user program as well as a builtin,
   and measure the time for a loop of 1,000 comparisons with each under
   the sentry. Explain the difference using the cost figures in
   [syscall-path.md](../../syscall-path.md).
6. **Hard.** Implement `case` fall-through with `;&`, as some modern shells
   do. Is it worth the complexity in a teaching shell?

---

## Check your understanding

1. What does an `if` statement actually test?
2. Why is `[` a command and not part of the shell's syntax?
3. When is `then` a reserved word, and when is it an ordinary word?
4. What is the difference between `{ cd /x; }` and `( cd /x )`?
5. Why does `{ echo a }` not end the group?
6. When does a `for` loop expand its word list, and when does it expand
   its body?
7. What is the status of an `if` whose conditions all fail and which has
   no `else`?
8. Why must the parser, not `main`, read additional lines?
9. Which pattern matcher does `case` use, and where else is it used?
