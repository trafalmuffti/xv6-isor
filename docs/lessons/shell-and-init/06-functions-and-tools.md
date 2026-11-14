# Lesson 6: Functions, scripts as programs, and tools for teaching

This lesson finishes the shell. It adds functions, which Lesson 8 needs
to write a reusable service library, and the few builtins that turn a
script into a well-behaved program: `return`, `exec`, `wait` and `type`.
It also adds two tools that exist mainly for teaching: a syntax-only mode
and a mode that prints the parse tree. Finally, it settles how a script
can be run by name, as if it were a compiled program, without weakening
the sentry's rule that only static ELF files may be executed.

---

## Objectives

After this lesson, a student should be able to:

1. Define and call shell functions, pass them arguments, and return a
   status from them.
2. Explain how a function's positional parameters are saved and restored,
   and why `local` is useful even though POSIX does not define it.
3. Explain why a function body must be copied when it is defined, given
   how xv6's parser stores words.
4. Run a script by name, and explain how the shell decides to treat a
   file as a script.
5. Use `sh -n` to check a script's syntax and `sh -T` to read its parse
   tree.
6. Use `exec` to replace the shell with a program, and explain why service
   scripts in Lesson 8 do this.

---

## Background

### Functions in the shell

A shell function is a named compound command. Defining it stores the
command; calling it runs the command with new positional parameters.
Functions run in the current shell, not in a child process, so they can
change variables and the current directory, and they are as fast as
builtins to call.

```sh
greet() {
    echo "hello, $1"
    return 0
}
greet world        # prints: hello, world
```

The Seventh Edition Bourne shell did not have functions. They were added
to the Bourne shell in System V Release 2, around 1984, and later
standardized by POSIX in the `name() compound-command` form used here.
The Korn shell added a `function name { … }` form, which this module does
not use.

### Scripts as programs

Unix lets you run a script by name, like `./backup`, if its first line is
`#!` followed by an interpreter, as in `#!/bin/sh`. The kernel reads that
line, and runs the interpreter with the script's name as an argument.
Before kernels supported `#!`, shells did the same job themselves: if
`exec` failed because a file was not a valid executable, the shell ran
the file as a shell script. POSIX still describes that fallback, and
calls the error `ENOEXEC`.

xv6-isor's sentry deliberately refuses to run anything but static x86-64
ELF files, because a `#!` line would make the host kernel start a host
interpreter outside the guest's world (see
[breakout.md](../../breakout.md)). So this lesson uses the old fallback,
implemented in the shell: the guest decides for itself that a file is a
script, and runs `/sh` on it. The security rule stays in place, and the
convenience comes back.

---

## Design

### Defining a function

The grammar gains one rule, at the level of a command:

```
command ::= NAME "(" ")" compound redirections*
          | …the rules from Lesson 5
```

When the evaluator reaches a function definition, it stores the name and
the body in a function table, and returns 0. It does not run the body.

**The body must be copied.** xv6's parser does not copy words. It stores
pointers into the line buffer and writes zero bytes into that buffer to
end each word, in `nulterminate`. That is fine for a command that runs
once and is forgotten, but a function body lives on after the line that
defined it, and the next line read would overwrite the buffer. The
function's words would turn into fragments of later commands. So the
definition must make a deep copy of the body: every node and every
string. This is the most instructive bug in the module, because it shows
how a design that is correct for one use breaks when the lifetime of the
data changes. Students who hit it should be encouraged to explain it
before fixing it.

### Calling a function

When the first word of a command names a function, the evaluator:

1. saves the current positional parameters
2. sets them to the command's other arguments
3. evaluates the body
4. restores the saved parameters
5. returns the body's status, or the value given to `return`

The lookup order for a command name becomes: functions, then builtins,
then programs on `PATH`. That is close to POSIX, which puts a few
"special" builtins first; the difference does not matter for this
module, and the simpler order is easier to remember.

### `return`

`return [n]` ends the current function with status `n`, or with the
status of the last command if `n` is missing. It uses the same flag
mechanism as `break` from Lesson 5: it sets a "returning" flag that every
list, loop and conditional checks, so they stop early, until the function
call that started it clears the flag. Using `return` outside a function
is an error.

### `local`

POSIX does not define local variables. Without them, every variable a
function sets is global, which makes reusable libraries fragile. Almost
every real shell, including `dash` and `ash`, provides `local` anyway,
and Lesson 8's `rc.subr` uses it to avoid clobbering the caller's
variables. `local name[=value]` saves the variable's old value on a stack
tied to the current function call, and the call restores it when it
returns. That gives dynamic scope, as in early Lisp, not the lexical
scope of C. Exercise 4 asks students to write a script that shows the
difference.

### More builtins

| builtin | purpose |
|---|---|
| `return [n]` | leave a function |
| `local name[=value]` | make a variable local to the current function |
| `exec cmd args…` | replace the shell with `cmd`, without forking |
| `exec` with only redirections | apply the redirections to the shell itself, permanently |
| `wait [pid]` | wait for a background job, or for all of them; returns its status |
| `type name` | say whether `name` is a function, a builtin or a program, and where |

**`exec`** is worth a moment in class. A shell that runs `exec prog` does
not fork; it becomes `prog`, keeping its pid. Lesson 8's service scripts
end with `exec` for that reason: the pid that `init` started is then the
service's pid, which is how `init` can supervise the service directly in
Lesson 9. `exec > log` with no command redirects the rest of the script,
which is a common way for a service script to send its own output to a
log file.

**`wait`** connects to Lesson 2's pitfall about collecting the wrong
child. The shell keeps a small table of the background jobs it started,
with their statuses once collected. `wait 12` waits until job 12 is done
and returns its status, and `wait` with no argument waits for all of
them.

### Running scripts by name

When the evaluator's child calls `exec` and it fails, the child checks
whether the file exists and is a regular file. If so, it reads its first
bytes:

- If the file starts with the ELF magic bytes, it was a real program that
  could not run, so the child reports status 126.
- Otherwise, it is treated as a script: the child runs `exec("/sh",
  {"sh", path, args…})`.

A first line of `#!/bin/sh` is allowed but not required, because to the
shell it is simply a comment. This is exactly the classic fallback, and
it means `./backup` works for any script. Students should notice that the
guest makes this decision with its own code, using only `open`, `read`
and `exec`, all of which the sentry already checks.

### Teaching tools: `sh -n` and `sh -T`

`sh -n script` parses the script and reports syntax errors, but runs
nothing. Real shells have this option, and it is the quickest way to
check a script after editing it.

`sh -T script` parses the script and prints the parse tree, indented, one
node per line, without running it. Real shells do not have this option.
It exists for teaching, because it makes the parser visible. For example,
for `a | b && c`:

```
AND
  PIPE
    EXEC a
    EXEC b
  EXEC c
```

Students can use it to check their hand-drawn trees from Lesson 3, and to
answer questions about precedence without running anything. Teaching
assistants can use it to show, in seconds, why a student's script parses
differently from what the student expected.

---

## Walkthrough

### Step 1: write the tests

- `f() { echo in f; }; f` prints `in f`.
- `f() { echo $1 $#; }; f a b` prints `a 2`.
- After calling `f a b`, `$1` in the caller has its old value.
- `f() { return 3; }; f; echo $?` prints 3.
- `x=1; f() { local x=2; }; f; echo $x` prints 1.
- `f() { x=2; }; x=1; f; echo $x` prints 2 (variables are global).
- A function defined on one line and called after ten more lines still
  works, which catches a missing deep copy.
- A script file without `#!` runs by name from the shell.
- `exec /echo hi; echo not reached` prints only `hi`.
- `sleep 50 & wait $!; echo $?` prints 0 after a short pause.
- `type cd`, `type f` and `type ls` say builtin, function and program.
- `sh -n` reports a missing `fi` and runs nothing.
- `sh -T` prints the tree above for `a | b && c`.

### Step 2: add the function table and definitions

Add the grammar rule, the table and the deep copy. Test with a function
defined and called on separate lines before anything else.

### Step 3: add calls, `return` and `local`

Add the lookup order to the evaluator. Add `return` using the flag from
Lesson 5. Add `local` with a stack that the function call unwinds.

### Step 4: add `exec`, `wait` and `type`

Each is small. `wait` needs the job table, which Lesson 2's exercise may
already have built.

### Step 5: add the script fallback

Add it to the child, after `exec` fails and before reporting an error.

### Step 6: add `-n` and `-T`

`-T` is a recursive printing function over the tree, one case per node
type. Writing it is also a useful review of every node type the module
has added.

---

## Lab

**Assignment.** Add functions, `return`, `local`, `exec`, `wait`, `type`,
running scripts by name, `-n` and `-T`.

**Done when:**

- All tests pass, old and new.
- `sh.c` stays under about 1,500 lines. If it is much larger, look for
  repeated code before handing it in.
- A short written explanation describes the deep-copy bug in your own
  words, including what the symptom would be if the copy were missing.

---

## Pitfalls

- **Not copying the body.** Covered above. The symptom is a function that
  works when called immediately and runs nonsense when called later.
- **Not restoring positional parameters.** After calling a function, the
  caller's `$1` is the function's.
- **Letting `return` escape.** A `return` inside a loop inside a function
  must stop the loop and the function, but not the function's caller.
- **Recursion without a limit.** `f() { f; }; f` recurses until the stack
  runs out, and in xv6 that is a fault, reported as status 139. Add a
  depth limit, such as 100, with a clear error.
- **Running scripts that are not scripts.** The fallback must check that
  the file is regular and readable first. Otherwise a directory or a
  device file is handed to `/sh`.
- **Treating `exec` failure as fatal for builtins.** `exec nosuch` should
  report status 127, and an interactive shell should keep running rather
  than exit.

---

## Exercises

1. **Easy.** Write a function `die` that prints its arguments to standard
   error and exits with status 1, and use it in a script.
2. **Easy.** Write a function `usage` and make a script call it when it
   gets the wrong number of arguments.
3. **Medium.** Add `getopts`, the POSIX builtin for parsing options such
   as `-v -o file`. Write a script that uses it.
4. **Medium.** Write a script whose output differs between dynamic scope,
   which `local` gives, and lexical scope, which C has. Explain the
   difference.
5. **Medium.** Add an `eval` builtin, which runs its arguments as a shell
   command. Then write an example where `eval` is genuinely needed, and
   one where it is a security problem.
6. **Hard.** Add a `command` builtin that runs a program even when a
   function has the same name. Lesson 8 shows a case where a library
   wants exactly this.
7. **Hard.** Make `sh -T` also print the expanded words when it is given
   a `-x`-style flag at run time. What does this tell you about the
   separation between parsing and expansion?

---

## Check your understanding

1. Where does a function run: in the current shell or in a child? What
   follows from that?
2. Why must a function's body be copied when it is defined?
3. What happens to `$1` when a function is called, and after it returns?
4. What kind of scope does `local` give?
5. What is the lookup order for a command name in this shell?
6. Why do Lesson 8's service scripts end with `exec`?
7. How does the shell decide that a file is a script?
8. Why does this shell implement the script fallback itself instead of
   letting the sentry honor `#!`?
9. What does `sh -T` show that `sh -n` does not?
