# Lesson 8: `rc.conf`, `rc.subr` and `rc.d`

A single `/etc/rc` works well for a small system, and it gets harder to
manage as the system grows. Every new program means editing one large
script, and the order of everything in it is implied by its line order.
In 2000, NetBSD replaced that design with `rc.d`: one small script per
service, a shared library of shell functions, one configuration file, and
a program that works out the order from dependencies written in the
scripts themselves. FreeBSD adopted the same system a few years later.
This lesson builds the same system for xv6-isor, entirely in the shell
language, plus two small C programs.

---

## Objectives

After this lesson, a student should be able to:

1. Explain the problems of a single, large `/etc/rc` and how `rc.d`
   solves them.
2. Write a service script that uses a shared library of shell functions.
3. Separate configuration (`rc.conf`) from mechanism (`rc.subr`) and from
   per-service policy (`rc.d/NAME`).
4. Start, stop and query services with a `service` command.
5. Explain how a dependency order is computed from `PROVIDE` and
   `REQUIRE` lines, and implement it as a topological sort.
6. Compare `rc.d` with System V's numbered scripts.

---

## Background

### Why one `/etc/rc` stops scaling

A monolithic `/etc/rc` has three problems. First, adding or removing a
program means editing a shared file, and a mistake anywhere can stop the
whole boot. Second, the order of startup is just the order of lines, so a
program that needs another program to be running must be placed after it
by hand, and nothing checks that the order is right. Third, there is no
standard way to stop or restart one program later, because the code that
started it is buried in the boot script.

### System V's answer: numbered scripts

System V Unix split the boot into one script per service as well. It put
the scripts in directories named after run levels, such as `rc3.d`, and
named the links in those directories with a letter and a number, such as
`S20network` and `K80network`. At each run level, the scripts starting
with `S` ran in numeric order with the argument `start`, and those
starting with `K` ran with `stop`. The number encodes the order, and
choosing numbers is left to whoever writes the scripts.

### The BSD answer: `rc.d` and `rcorder`

NetBSD's `rc.d`, introduced in NetBSD 1.5 and adopted by FreeBSD in 5.0,
keeps one script per service but drops the numbers. Each script states
its dependencies in special comments at the top:

```sh
# PROVIDE: ticker
# REQUIRE: logd
```

A program called `rcorder` reads those lines from every script and prints
the scripts in an order that satisfies all the dependencies. `/etc/rc`
then runs each script in that order with the argument `start`. The order
is computed, so adding a service never means renumbering anything.

Each script is short because the common work is in a library,
`/etc/rc.subr`, which every script sources. The library's main function,
`run_rc_command`, implements `start`, `stop`, `restart` and `status`
from a few variables the script sets. Whether a service starts at all is
decided by a variable in `/etc/rc.conf`, such as `ticker_enable=YES`.

The result is the clearest split of mechanism and policy in any Unix
init system, and that is why this module uses it.

---

## Design

### The files

| file | role | written by |
|---|---|---|
| `/etc/rc` | boot: load configuration, order the scripts, start each | the system |
| `/etc/rc.conf` | settings, as shell variables | the administrator |
| `/etc/rc.subr` | shared shell functions | the system |
| `/etc/rc.d/NAME` | one script per service | whoever packages the service |
| `/etc/rc.local` | the administrator's own boot additions | the administrator |
| `/etc/rc.shutdown` | stop the services in reverse order | the system |
| `/rcorder` | the ordering program (C) | the system |
| `/isalive` | report whether a pid is running (C) | the system |
| `/service` | the command for managing services (a shell script) | the system |

### `/etc/rc.conf`

The configuration file is a shell script that only sets variables:

```sh
# /etc/rc.conf: system settings. Sourced by /etc/rc and by rc.d scripts.
hostname="xv6"

ticker_enable="YES"
ticker_flags="100"        # ticks between lines

crashy_enable="NO"
```

Because it is sourced with `.`, the administrator can use any shell
feature in it, but by convention it holds only assignments. Lesson 4's
`${name:-word}` form lets scripts supply defaults for anything the file
leaves out.

### `/etc/rc.subr`

The library defines a handful of functions. Each one is short, and the
whole library should fit on two screens.

```sh
# /etc/rc.subr: functions for rc.d scripts.

# checkyesno VAR: succeed if $VAR is a "yes" value.
checkyesno()
{
    eval _value=\$$1
    case "$_value" in
    [Yy][Ee][Ss]|[Tt][Rr][Uu][Ee]|[Oo][Nn]|1) return 0 ;;
    [Nn][Oo]|[Ff][Aa][Ll][Ss][Ee]|[Oo][Ff][Ff]|0|"") return 1 ;;
    *) echo "$1 is not set properly"; return 1 ;;
    esac
}

load_rc_config()
{
    . /etc/rc.conf
}

# run_rc_command ARG: implement start, stop, restart, status and run
# using the variables name, command, command_args and pidfile.
run_rc_command()
{
    local rc_arg pid
    rc_arg=$1
    pidfile=${pidfile:-/var/run/$name.pid}
    eval command_args=\${${name}_flags:-\$command_args}

    case "$rc_arg" in
    start)
        checkyesno ${name}_enable || return 0
        if _running; then echo "$name is already running"; return 1; fi
        echo "Starting $name."
        $command $command_args </dev/null >/dev/null &
        echo $! > $pidfile
        ;;
    stop)
        if ! _running; then echo "$name is not running"; return 1; fi
        echo "Stopping $name."
        kill $(cat $pidfile)
        rm $pidfile
        ;;
    restart)
        run_rc_command stop
        run_rc_command start
        ;;
    status)
        if _running; then
            echo "$name is running as pid $(cat $pidfile)"
        else
            echo "$name is not running"
            return 1
        fi
        ;;
    run)
        exec $command $command_args </dev/null >/dev/null
        ;;
    *)
        echo "usage: $0 start|stop|restart|status|run"
        return 2
        ;;
    esac
}

_running()
{
    test -f $pidfile && isalive $(cat $pidfile)
}
```

Students should notice several lessons converging in this one file:
functions and `local` (Lesson 6), `case` with patterns (Lesson 5),
`${name:-word}` and command substitution (Lesson 4), `||` (Lesson 3),
`$!` and `/dev/null` (Lessons 1 and 2). The `eval` lines read a variable
whose name is itself in a variable, such as `ticker_enable`; Lesson 6's
exercise 5 adds `eval`, and this lesson makes it part of the core.

The `run` argument is not used yet. Lesson 9 uses it, when `init`
supervises a service directly: `init` runs the script with `run`, and the
script replaces itself with the service by using `exec`, so the process
`init` started is the service itself.

### A service script

With the library, a service script is a few lines of variables:

```sh
#!/bin/sh
# PROVIDE: ticker
# REQUIRE: logd

. /etc/rc.subr

name=ticker
command=/ticker
command_args=100

load_rc_config
run_rc_command "$1"
```

The script is runnable by name thanks to Lesson 6's script fallback, so
`/etc/rc.d/ticker status` works directly.

### The demonstration services

xv6-isor has no networking and no daemons, so the lesson adds two tiny
programs to act as services. They are written in C, but kept as short as
possible because they are props, not the subject:

- **`ticker`** writes the current `uptime()` value into
  `/var/log/ticker`, replacing the file's contents, every `N` ticks, where
  `N` is its first argument. Its output is easy to watch with `cat`.
- **`crashy`** runs for a number of ticks and then exits with status 1.
  It does nothing useful in this lesson, and in Lesson 9 it is the
  service that keeps crashing.

A third script, `logd`, can be an empty service that provides `logd`, so
that the dependency in `ticker`'s header has something to point at. That
small fiction is a good opening for a discussion of what a real logging
service would do.

### `isalive`

`run_rc_command` has to know whether a pid is still running. BSD systems
check with `kill -0 pid`, which sends no signal but reports whether the
process exists. xv6 has no signals, so this lesson adds a tiny program
instead: `isalive pid` calls `getprocs` (the call behind `pslist`) and
exits with 0 if a running process has that pid, and 1 otherwise. A
zombie counts as not running.

There is a subtle problem here, worth raising in class. Pids are reused.
If `ticker` dies and another program later gets the same pid, `isalive`
reports the pid as running, and `status` gives the wrong answer. Real
systems mitigate this by also checking the process's name. Exercise 3
asks students to do that, using the name field `getprocs` already
provides.

### The new `/etc/rc`

The boot script becomes a loop over the ordered scripts:

```sh
#!/bin/sh
# /etc/rc: boot the system. $1 is "autoboot" on a normal boot.

. /etc/rc.subr
load_rc_config

# ...the device and /var setup from Lesson 7 goes here...

echo "rc: hostname is $hostname"

for script in $(rcorder /etc/rc.d/*); do
    sh $script start
done

test -f /etc/rc.local && . /etc/rc.local
exit 0
```

Each script runs in its own shell, with `sh $script start`, so that one
script's variables cannot leak into the next. That isolation is a design
choice the BSDs also make, and it is the reason each script has to call
`load_rc_config` itself.

### `rcorder`

`rcorder` is the one substantial piece of C in this lesson. Given a list
of files, it:

1. reads the comment lines at the top of each file, collecting the names
   after `# PROVIDE:` and `# REQUIRE:`
2. builds a graph with an edge from each required name to each file that
   requires it
3. prints the files in an order in which every file comes after the files
   that provide what it requires
4. reports an error and exits with status 1 if there is a cycle, or if a
   file requires a name that no file provides

That is a topological sort, which most students have met in an
algorithms course but rarely in a real program. Kahn's algorithm, which
repeatedly outputs a file with no unmet requirements, is the easiest to
explain and to debug. Ties should be broken by file name, so that the
order is the same on every boot, which matters for testing and for
anyone reading boot output.

Teachers who want a lighter lesson can postpone `rcorder` and have
`/etc/rc` run the scripts in alphabetical order, which is what the
wildcard in Lesson 4 already gives. That is close to the System V
approach, and the comparison is itself instructive.

### `/etc/rc.shutdown`

Shutdown stops the services in the reverse of the boot order:

```sh
#!/bin/sh
. /etc/rc.subr
load_rc_config
order=$(rcorder /etc/rc.d/*)
reversed=
for s in $order; do reversed="$s $reversed"; done
for script in $reversed; do
    sh $script stop
done
exit 0
```

Reversing a list in the shell takes a loop, which is a small, honest
reminder that the shell has no arrays.

### `service`

`service NAME ACTION` is a short shell script, installed as `/service`:

```sh
#!/bin/sh
if test $# -ne 2; then
    echo "usage: service name start|stop|restart|status"
    exit 2
fi
if ! test -f /etc/rc.d/$1; then
    echo "service: no such service: $1"
    exit 1
fi
exec sh /etc/rc.d/$1 $2
```

It is deliberately trivial. On FreeBSD, `service` is also a shell script,
and not much longer. The interesting part is everything it delegates to.

---

## Walkthrough

### Step 1: write the tests

- With `ticker_enable=YES`, boot prints `Starting ticker.`, and after a
  few seconds `cat /var/log/ticker` shows a number that grows.
- With `ticker_enable=NO`, boot does not start it, and `service ticker
  status` reports not running with status 1.
- `service ticker stop` stops it, and `status` then says not running.
- `service ticker restart` gives it a new pid.
- `service nosuch status` prints an error and exits 1.
- `rcorder` puts `logd` before `ticker`, and reports a cycle for two test
  scripts that require each other.
- `shutdown` stops `ticker` before `logd`.

### Step 2: write `isalive`, `ticker` and `crashy`

They are short C programs. Test each by hand.

### Step 3: write `rc.subr` and one service script

Get `ticker` working with `sh /etc/rc.d/ticker start`, `status` and
`stop` before touching `/etc/rc`.

### Step 4: write `rcorder`

Test it on its own with a directory of fake scripts that contain only
header comments, including a cycle and a missing requirement.

### Step 5: rewrite `/etc/rc` and `/etc/rc.shutdown`, and add `service`

Then boot, and run the end-to-end tests.

---

## Lab

**Assignment.** Build the `rc.d` system: `rc.conf`, `rc.subr`, at least
three service scripts, `rcorder`, `isalive`, `service`, and the new
`rc` and `rc.shutdown`.

**Done when:**

- All the tests above pass, with every earlier test.
- Adding a new service requires creating one file in `/etc/rc.d` and one
  line in `/etc/rc.conf`, and nothing else. Demonstrate this live.
- A paragraph compares the result with System V's numbered scripts.

---

## Pitfalls

- **Forgetting `</dev/null`.** A service that inherits the console as its
  standard input competes with the shell for the user's typing, and
  characters seem to vanish at random.
- **Not writing the pidfile.** Without it, `stop` and `status` have
  nothing to go on. `$!` must be read immediately after the `&`.
- **Sourcing scripts instead of running them.** If `/etc/rc` sources each
  script with `.`, variables such as `name` and `command` leak from one
  script into the next, and the wrong program starts.
- **An unstable order.** If `rcorder` breaks ties differently on each run,
  boot output changes and tests become flaky.
- **Stale pidfiles.** After a crash, the pidfile remains. `/etc/rc`
  should clear `/var/run` at boot, as Lesson 7's script does, or `start`
  refuses to run a service that it thinks is already running.
- **Stopping a reused pid.** Without the name check from exercise 3,
  `stop` can kill an unrelated program that happens to have the old pid.

---

## Exercises

1. **Easy.** Add `rc_debug=YES` to `rc.conf`, and make `rc.subr` print
   each action it takes when it is set.
2. **Easy.** Add a `rcvar` action that prints the name of the variable
   that enables the service, as FreeBSD's scripts do.
3. **Medium.** Make `_running` check the process name as well as the pid,
   using `getprocs` in `isalive`. Explain the pid-reuse problem it fixes.
4. **Medium.** Add `BEFORE:` lines to `rcorder`, meaning "start me before
   this", and explain how they change the graph.
5. **Medium.** Add a `KEYWORD: nostart` line that makes `/etc/rc` skip a
   script at boot while `service` can still start it by hand.
6. **Hard.** Rewrite `rcorder` in the shell. Is it practical? What does
   the attempt teach about which jobs suit the shell?
7. **Hard.** Implement System V style run levels on top of `rc.d`: a
   `runlevel N` command that starts and stops services so that exactly
   the ones listed for level `N` are running. Compare the complexity with
   the BSD design.

---

## Check your understanding

1. What three problems does `rc.d` solve?
2. Where is each of these decided: whether a service starts, how it
   starts, and in what order?
3. Why does `/etc/rc` run each script with `sh` instead of sourcing it?
4. What does `run_rc_command` need from a service script?
5. Why does a service's standard input come from `/dev/null`?
6. What is `rcorder`'s algorithm, and what does it do with a cycle?
7. Why must `rcorder` break ties the same way every time?
8. How does `isalive` work without signals, and what can go wrong with
   it?
9. Why are services stopped in reverse order?
