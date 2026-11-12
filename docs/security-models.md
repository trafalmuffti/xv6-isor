# Security models for gVisor, root users, POSIX ACLs and SMACK

xv6 has no permissions at all. This document explains the security models instead of adding them to the code:

1. [How any permission check works](#1-how-any-permission-check-works)
2. [gVisor's security model](#2-gvisors-security-model)
3. [Root users in gVisor](#3-root-users-in-gvisor)
4. [POSIX permissions and ACLs](#4-posix-permissions-and-acls)
5. [SMACK](#5-smack)
6. [Comparison](#6-comparison)
7. [Where each model would fit in xv6-isor](#7-where-each-model-would-fit-in-xv6-isor)

Statements about gVisor were checked against its documentation and source
code, and statements about SMACK against the Linux kernel documentation.
See [Sources](#sources).

---

## 1. How any permission check works

Every access-control model has the same four parts:

| part | question | Unix example |
|---|---|---|
| **subject** | who is asking? | a process, identified by its uid and gid |
| **object** | what is being accessed? | a file, identified by its inode |
| **operation** | what do they want to do? | read, write, execute |
| **policy** | who decided what is allowed? | the file's owner, using `chmod` |

The kernel combines them at one place, the **reference monitor**. That
is a check that every access must pass through and that the subject cannot
bypass or tamper with. If the check fails, the system call returns an
error instead of doing the work:

```
cat notes
 └─ open("notes", O_RDONLY)
     └─ kernel: subject (uid 1000) + object (owner 1001, mode 0640) + read
         └─ denied → open returns -1, errno = EACCES
             └─ cat prints "cat: notes: Permission denied"
```

"Permission denied" is just libc's text for the error number `EACCES`.
The kernel only returns the number.

The models below differ in what the subject and object carry (numbers,
lists, labels) and above all in **who sets the policy**:

- **DAC (discretionary access control):** the owner of an object decides.
  Unix mode bits and POSIX ACLs work this way.
- **MAC (mandatory access control):** an administrator decides, and
  ordinary owners can't override it. SMACK, SELinux and AppArmor work this
  way.

---

## 2. gVisor's security model

### The threat

gVisor exists because the host kernel's system call interface is the
most common way untrusted code escapes a container. Its security
documentation sorts attacks into classes:

- **System API:** bugs reachable through system calls, files and sockets,
  for example Dirty COW, which raced threads over a `/proc` file. This is
  the class gVisor is designed to reduce.
- **System ABI:** bugs in how the hardware or kernel handles traps and
  interrupts, which need only native code execution.
- **Side channels:** Spectre and similar hardware leaks.

gVisor relies on the host kernel and the hardware platform for the last
two, and on host cgroups against resource exhaustion. Its documentation
says plainly: *"A sandbox is not a substitute for a secure
architecture."*

### Two principles, two layers

The documentation states the design as two principles:

> First, the application's direct interactions with the host System API
> are intercepted by the Sentry, which implements the System API instead.
> Second, the System API accessible to the Sentry itself is minimized to a
> safer, restricted set.

```
 ┌──────────── application (untrusted) ────────────┐
 │  makes Linux system calls                       │
 └──────────────────────┬──────────────────────────┘
       layer 1          │  intercepted (systrap / KVM / ptrace platform)
 ┌──────────────────────▼──────────────────────────┐
 │  Sentry: a Linux kernel written in Go           │
 │  - its own implementation of every syscall      │
 │  - emulated users, groups, capabilities, ACLs   │
 └──────────────────────┬──────────────────────────┘
       layer 2          │  small, enumerated set of host calls
 ┌──────────────────────▼──────────────────────────┐
 │  confinement of the Sentry itself               │
 │  - empty mount namespace, own user namespace    │
 │  - runs as "nobody", seccomp-filtered           │
 │  - no opening files, no new sockets             │
 └──────────────────────┬──────────────────────────┘
                        │  files only via the Gofer
 ┌──────────────────────▼──────────────────────────┐
 │  Gofer: separate process that owns the          │
 │  container's filesystem, hands over fds         │
 └─────────────────────────────────────────────────┘
```

**Layer 1: the application talks only to the Sentry.** No system call is
passed through to the host. Every supported call has its own
implementation in the Sentry, which is unlikely to share the host
kernel's bugs. The application can never put its own arguments into a
host system call. As the documentation puts it, a `read` on a file may
still end up as a host `read`, but one *made by the Sentry, not by
passing through arguments from the application*.

**Layer 2: the Sentry is confined as if it were compromised.** gVisor
assumes a bug in the Sentry could be exploited, so the Sentry itself is
locked down:

- **Files:** it runs in an empty mount namespace, and the Gofer always
  owns the container's filesystem. In the default "directfs" mode the
  Gofer donates a descriptor for each mount point, and the Sentry opens
  files relative to those with calls like `openat`, with `O_NOFOLLOW`
  enforced by seccomp. With `--directfs=false` the Sentry runs under
  stricter seccomp filters and the Gofer does every file operation for it.
- **Host system calls:** it may make only a minimal, explicitly listed
  set: duplicating and closing descriptors, synchronization, timers,
  signals. That set does not include opening files (unless directfs is
  enabled) or creating sockets (unless host networking is enabled).
- **Networking:** it reads and writes packets on a virtual Ethernet
  device, with gVisor's own network stack.

The project also has engineering rules meant to keep the Sentry itself
safe: no cgo, all `unsafe` code isolated in files named `*unsafe.go`, few
external imports, continuous fuzzing.

### Why gVisor is not "just a ptrace sandbox"

gVisor's FAQ draws the line that matters for xv6-isor. A classic ptrace
sandbox *inspects* system calls and lets approved ones run on the host
kernel. That has two problems: the approved calls still reach the host's
code, and checking arguments that another thread can change afterwards is
a race you can't win.

In gVisor the traced process *"is never allowed to continue execution
into the host kernel and complete a call directly. Instead, all system
calls are interpreted and handled by the Sentry itself."*

xv6-isor sits between the two:

- **Like gVisor:** it emulates most xv6 calls itself and never passes a
  guest's own system call through.
- **Unlike gVisor:**
  - It still asks the host kernel to run `clone`, `execve`, `brk` and
    `exit_group` on the guest's behalf.
  - Layer 2 is missing: the xv6-isor sentry runs with all the rights of
    whoever started it.
  - It uses `ptrace`, which gVisor replaced with `systrap` (based on
    `seccomp`'s `SECCOMP_RET_TRAP`) as its default platform in mid-2023.

### What gVisor does about DAC and MAC

| | inside the sandbox (for the application) | outside (for the sandbox) |
|---|---|---|
| **DAC** | fully emulated: uids, gids, mode bits, POSIX ACLs, capabilities, user namespaces (sections 3 and 4) | the host's own DAC applies to the Sentry and Gofer |
| **MAC** | not provided: no SELinux, AppArmor or SMACK inside | the confinement itself (namespaces, seccomp, the Gofer) is mandatory and can't be changed from inside |

What happens to the LSM settings in a container's OCI spec
(`runsc/specutils/specutils.go`):

- **SELinux:** a label is rejected with *"SELinux is not supported"*.
- **AppArmor:** a profile, which Docker sets by default, is logged as
  *"being ignored"*.

The design reason is that a MAC policy inside the sandbox would only
restrict behavior the Sentry already controls completely. The security
that matters is in layer 2.

gVisor *does* emulate seccomp for applications: if a program installs a
seccomp filter, the Sentry evaluates it. That is system call filtering,
not label-based MAC.

---

## 3. Root users in gVisor

There are several different "roots" around a gVisor sandbox, and they
have nothing in common except the number 0.

### Kernels know numbers, not names

A user name like `alice` exists only in user space. The kernel stores a
uid (an integer) in each process and in each file's inode, and compares
numbers. `/etc/passwd` is read by programs: `login` checks a password,
libc's `getpwuid()` turns 1000 into `alice` so `ls -l` can print it. The
kernel never opens the file. Login works like this:

```
login (runs as uid 0)
  ├─ checks the name and password against /etc/passwd and /etc/shadow
  ├─ setgid(1000); setuid(1000)
  └─ exec("/bin/sh")       ← the shell, and everything it starts, has uid 1000
```

gVisor follows the same model.

### Root inside the sandbox: uid 0 in the Sentry's bookkeeping

Each task in the Sentry carries a `Credentials` structure
(`pkg/sentry/kernel/auth/credentials.go`) that mirrors Linux's
`struct cred`:

- real, effective, saved and filesystem uids and gids
- supplementary groups
- five capability sets: permitted, inheritable, effective, bounding and
  ambient
- a user namespace

`setuid`, `setgroups`, `capset`, `chmod`, `chown` and setuid binaries all
update these structures.

So "root in a gVisor container" means uid 0 with capabilities **in the
Sentry's memory**. The host kernel never sees the application's uid. The
security documentation states the consequence:

> Even with appropriate capabilities, a user in a gVisor sandbox will
> only be able to manipulate virtualized system resources (e.g. the
> system time, kernel settings or filesystem attributes) and not
> underlying host system resources.

### Where the application's uid comes from

1. **Names are resolved before gVisor runs.** The container image says
   `USER app`. The container engine (e.g. containerd under Docker) looks
   up `app` in **the image's** `/etc/passwd` and `/etc/group`.
2. **`runsc` receives numbers only.** The OCI spec has
   `process.user.uid`, `gid` and `additionalGids`.
3. **The Sentry starts the first process** with those credentials.

gVisor itself reads a passwd file in one place only: if `$HOME` isn't
set, `MaybeAddExecUserHome` (`pkg/sentry/fsimpl/user/user.go`) reads
`/etc/passwd` **inside the container** to find the user's home
directory. It never reads the host's passwd file, and it doesn't use
the file for access decisions.

### Root outside the sandbox: who runs `runsc`

| who | runs as | why |
|---|---|---|
| `runsc` started by Docker/containerd | usually host root | it needs privileges to create namespaces, a minimal chroot and cgroups |
| the sandbox process (the Sentry) | **`nobody` (uid 65534)** in a **new user namespace** | started by `runsc`, given only the capabilities it needs to build its own empty root |
| `runsc --rootless` | the calling user, mapped to root inside a new user namespace | for running without host root, e.g. `runsc do` |

The `nobody` row comes from `runsc/sandbox/sandbox.go`:

- When `runsc` has `CAP_SETUID` and `CAP_SETGID`, it starts the sandbox
  with `Credential{Uid: nobody, Gid: nobody}` in a new user namespace.
  Normally it maps `nobody` inside to `nobody` on the host. In rootless
  mode it maps `nobody` inside to the calling user.
- The sandbox gets the ambient capabilities `CAP_SYS_ADMIN`,
  `CAP_SYS_CHROOT` and `CAP_SETPCAP`. The source comment says these are
  needed to *construct an empty root* and to clear the bounding set.

So even when `runsc` itself is started as host root, the Sentry that
runs untrusted code is not.

To summarize the four uid 0s:

- **The application's root:** uid 0 in the Sentry's emulated credentials.
- **The root that built the sandbox:** `runsc`'s, usually host root.
- **Root in rootless mode:** your own user, mapped to 0 in a user
  namespace.
- **The Sentry itself:** none of these. It is `nobody`.

---

## 4. POSIX permissions and ACLs

### Mode bits: the classic Unix model

Each inode stores an owner uid, a group gid and 12 mode bits: `rwx` for
the owner, the group and everyone else, plus setuid, setgid and sticky.

```
-rw-r-----  1 bob  staff  42  notes
 │└┬┘└┬┘└┬┘
 │ │  │  └── other: ---
 │ │  └───── group: r--
 │ └──────── owner: rw-
 └────────── file type
```

**The check** picks exactly one set of bits:

```
if process uid == file owner        use the owner bits
else if process is in file's group  use the group bits
else                                use the other bits
allowed if every requested bit is set in that set
```

These rules surprise most students:

- **The first matching class wins, not the most generous.** With mode
  `0077` the owner is refused even though "other" has full access.
- **Directories reuse the bits with different meanings.** `r` means "may
  list the names". `x` means "may look up names in it" (search), and
  every directory on the path needs it. `w` means "may create, delete
  and rename entries". So deleting a file needs write permission on the
  *directory*, not on the file.
- **The check happens at `open`.** Afterwards the descriptor carries the
  right. Changing a file's mode doesn't revoke access for processes
  that already have it open.
- **Root (uid 0) bypasses the check.** On Linux that power is split into
  capabilities, notably `CAP_DAC_OVERRIDE` (any read or write, search
  any directory) and `CAP_DAC_READ_SEARCH` (read any file, search any
  directory). Execute is still refused unless some execute bit is set.
- **Special bits:**
  - **setuid/setgid on a program:** it runs with the file owner's (or
    group's) identity. This is how `passwd` can write `/etc/shadow`.
  - **Sticky bit on a directory:** only a file's owner may delete it,
    which is why `/tmp` is safe to share.
  - **The `umask`:** clears bits from the mode of every new file.

### POSIX ACLs: per-user and per-group entries

Mode bits allow only one owner and one group. **Access Control Lists**
add more entries. They come from POSIX.1e, a draft standard that was
withdrawn but is implemented by Linux, FreeBSD and gVisor, among others.
(macOS uses a different, NFSv4-style ACL model.)

```
$ setfacl -m u:alice:r notes
$ getfacl notes
# owner: bob
# group: staff
user::rw-          ← ACL_USER_OBJ   (the owner bits)
user:alice:r--     ← ACL_USER       (a named user)
group::r--         ← ACL_GROUP_OBJ  (the owning group bits)
mask::r--          ← ACL_MASK       (upper limit for the group class)
other::---         ← ACL_OTHER      (the other bits)
```

**The check**, from `acl(5)`, also picks one class:

1. If the process is the **owner**, use `user::`. Allow or deny.
2. Else if it matches a **named user** entry, use that entry *and* the
   mask. Allow or deny.
3. Else if it is in the **owning group**, or in any **named group**:
   allow if any matching group entry, limited by the mask, grants the
   access. Otherwise deny.
4. Else use `other::`.

Further details:

- **The mask:** named users, named groups and the owning group together
  form the "group class", and the mask is the most that class may get.
  When an ACL has a mask, `ls -l` shows the mask in the group position of
  the mode, and `chmod g=…` changes the mask. That is how old tools keep
  working.
- **Default ACLs:** set on a directory, these are copied to new files
  created in it. Mode bits alone can't express inheritance like this.
- **Storage:** Linux keeps ACLs in the extended attributes
  `system.posix_acl_access` and `system.posix_acl_default`.
- **Still DAC:** the file's owner edits the ACL. ACLs make the
  discretionary model more precise, not mandatory.

gVisor implements both checks inside the Sentry, in the same function
(`pkg/sentry/vfs/permissions.go`):

- `GenericCheckPermissions` uses the ACL if the file has one, and the
  owner/group/other mode bits otherwise.
- It then applies the `CAP_DAC_READ_SEARCH` and `CAP_DAC_OVERRIDE`
  exceptions, and returns `EACCES` if nothing allowed the access.
- The ACL algorithm (`pkg/sentry/vfs/posix_acl.go`) quotes the steps of
  `acl(5)` in its comments, which makes it a readable reference.

### What it takes to add this to a kernel

| piece | where |
|---|---|
| uid/gid (and groups) in every process | process structure, inherited on `fork` |
| owner, group, mode (and an optional ACL) on every file | inode |
| the check | `open`, `exec`, creating and deleting files, `chdir`, path lookup |
| ways to change identity and policy | `setuid`/`setgid` (for `login`), `chmod`, `chown`, `setfacl` |
| a passwd file | **only** for user names: logging in by name, `ls -l` showing names |

---

## 5. SMACK

SMACK, the **Simplified Mandatory Access Control Kernel**, is a Linux
security module whose documentation lists simplicity among its primary
design goals. It is used in the Tizen operating system.

### Labels instead of identities

Every subject (task) and every object (file, directory, task, socket,
IPC object) carries a **label**: a short text string with no spaces,
such as `Secret`, `Game` or `_`. Labels are not users. There is no
passwd file, no owner and no `chmod`. A process's label is usually set
by the system's startup code. A new file gets the label of the process
that creates it.

### The seven rules

From the kernel documentation, applied in this order:

1. Any access requested by a task labeled `*` is denied.
2. A read or execute access requested by a task labeled `^` is permitted.
3. A read or execute access requested on an object labeled `_` is
   permitted.
4. Any access requested on an object labeled `*` is permitted.
5. Any access requested by a task on an object with the same label is
   permitted.
6. Any access requested that is explicitly defined in the loaded rule set
   is permitted.
7. Any other access is denied.

In other words:

- **`_` (floor):** system files everyone may read and run.
- **`^` (hat):** a task that may read everything, e.g. a backup daemon.
- **`*` (star):** objects everyone may use, such as `/dev/null` or a
  terminal.
- **Same label:** always allowed.
- **Everything else:** forbidden unless a rule says otherwise.

### Rules

A rule is `subject-label object-label access`, where access is built from
these letters:

| letter | meaning |
|---|---|
| `r` | read |
| `w` | write |
| `x` | execute |
| `a` | append |
| `t` | transmute: files created in a transmuting directory get the directory's label |
| `b` | report this rule for bring-up (debugging) |
| `-` | placeholder. A lone `-` means no access |

```
TopSecret  Secret    rx
Secret     Unclass   R
User       HR        w
Closed     Off       -
```

There's no need to write a rule for a label accessing its own objects;
rule 5 already allows that.

### How operations map to accesses

| operation | access needed |
|---|---|
| open a file for reading | `r` on the file |
| search a directory | `x` on the directory |
| create a file | `r` and `w` on the containing directory |
| delete a file | `r` and `w` on the file and on the containing directory |
| send a signal (`kill`) | `w` from the sending task to the target task |
| send a network packet | `w` from the source task to the destination |

### Where SMACK keeps things

- **Labels on files:** extended attributes in the `security` namespace.
  - `SMACK64`: the file's label.
  - `SMACK64EXEC`: the label a process runs with after `exec`ing this
    program. This is MAC's version of setuid.
  - `SMACK64TRANSMUTE`: on a directory, new files get the directory's
    label (if the rule includes `t`).
  - Tools: `chsmack -a Secret notes`, or `attr -S -s SMACK64 -V Secret notes`.
- **Labels on processes:** read with `cat /proc/self/attr/current`.
  Changing your own label requires privilege (see below).
- **Rules and configuration:** text written into files under
  `/sys/fs/smackfs`, for example `echo "User HR w" > /sys/fs/smackfs/load2`.
  Rules take effect immediately.

### Who may change things

Changing labels or rules requires `CAP_MAC_ADMIN`. `CAP_MAC_OVERRIDE`
lets a process ignore SMACK's decisions. The `onlycap` file can restrict
those capabilities to processes with particular labels.

That is what makes SMACK *mandatory*: a file's creator can't decide who
else may read it. Only the rule table can.

### Why it suits teaching

The whole policy fits in one small function and one table:

```
smack_access(subject, object, want):
    if subject == "*":                          deny
    if subject == "^" and want ⊆ {r, x}:        allow
    if object  == "_" and want ⊆ {r, x}:        allow
    if object  == "*":                          allow
    if subject == object:                       allow
    if rules[subject][object] ⊇ want:           allow
    deny
```

Compared with POSIX permissions:

- **No identity to manage:** no users, no passwd file.
- **No per-file metadata beyond one label.**
- **No owner whose choices have to be respected.**

gVisor does not implement SMACK (section 2).

---

## 6. Comparison

| | POSIX mode bits | POSIX ACLs | SMACK |
|---|---|---|---|
| kind | DAC | DAC | MAC |
| subject carries | uid, gid, groups | uid, gid, groups | one label |
| object carries | owner, group, 9 bits (+3 special) | the same + entry list | one label |
| who sets policy | the file's owner (`chmod`) | the file's owner (`setfacl`) | the administrator (rule table) |
| how a process changes identity | `setuid`, setuid binaries, `login` | same | `SMACK64EXEC`, or writing `/proc/self/attr/current` with `CAP_MAC_ADMIN` |
| needs users and a passwd file | for names only | for names only | no |
| policy readable in one place | no, spread over every inode | no, spread over every inode | yes, one rule table |
| in gVisor (for applications) | yes | yes | no |

---

## 7. Where each model would fit in xv6-isor

xv6-isor implements none of these. If you wanted to add one as an
exercise, the work is easier than it looks, because the sentry is already
a reference monitor: every guest file operation goes through a few
functions in `sentry/fs.go` (`open`, `parent`, `execPath`), and process
operations through `sysKill` and `sysGetprocs`. Each model needs the same
three things:

1. **Something on each process:** a uid, or a label. It goes in
   `struct Proc`, copied in `forkChild`.
2. **Something on each file:** an owner and mode, or a label. xv6-isor's
   files are host files, and an unprivileged sentry can't `chown` them or
   set `security.*` attributes, so this would be kept in a `user.*`
   extended attribute or in a table inside the sentry.
3. **One check function**, called at those few places, plus a message
   when it fails. The point of the exercise is the visible denial:

   ```
   $ cat notes
   access denied: user -> secret (r) open /notes
   cat: cannot open notes
   ```

The models differ in how the policy gets changed:

- **POSIX mode bits** need `setuid` (for something like `login`),
  `chmod`, `getuid`, and a passwd file if you want names. That's several
  new system calls and a user-space program.
- **SMACK** needs no new system calls if it copies Linux's interface:
  - **Device files** read and written by the sentry (xv6 already has
    them: `console` is device 1), one each for the rule table, the
    current process's label and relabeling a file.
  - **A tiny `runas` program** that sets its own label and then `exec`s
    a command. It's needed because `echo user > label` would only
    relabel the short-lived `echo` process.

  That turns the shell into the policy editor:
  `echo user secret r > smackload`.

Neither would make xv6-isor secure: anyone in the guest could edit the
rules or switch labels. Real security would need the rule table to be
writable only by a privileged label, and it would need gVisor's layer 2,
confining the sentry itself, which xv6-isor doesn't have.

---

## Sources

gVisor (`github.com/google/gvisor`, master branch):

- `g3doc/architecture_guide/security.md`: threat model, the two
  principles, what the sandbox may do on the host, defense-in-depth rules,
  "Is this just a ptrace sandbox?"
- `g3doc/architecture_guide/platforms.md`: KVM and systrap platforms;
  systrap replacing ptrace as the default in mid-2023
- `g3doc/user_guide/rootless.md`: rootless modes and user namespaces
- `pkg/sentry/kernel/auth/credentials.go`: the `Credentials` structure
- `pkg/sentry/vfs/permissions.go`: `GenericCheckPermissions`
- `pkg/sentry/vfs/posix_acl.go`: the POSIX ACL check
- `pkg/sentry/fsimpl/user/user.go`: reading the container's `/etc/passwd`
  for `$HOME`
- `runsc/sandbox/sandbox.go`: running the sandbox as `nobody` in a new user
  namespace
- `runsc/specutils/specutils.go`: rejecting SELinux labels, ignoring
  AppArmor profiles
- `g3doc/user_guide/filesystem.md`: directfs (on by default) and its
  seccomp restrictions
- `runsc/boot/filter/`: the seccomp filters installed on the Sentry
- `pkg/sentry/kernel/seccomp.go`: seccomp emulated for applications

Linux (`github.com/torvalds/linux`):

- `Documentation/admin-guide/LSM/Smack.rst`: SMACK labels, the seven
  rules, access letters, extended attributes, smackfs, capabilities

Manual pages: `acl(5)`, `setfacl(1)`, `getfacl(1)`, `capabilities(7)`,
`credentials(7)`, `user_namespaces(7)`, `passwd(5)`.
