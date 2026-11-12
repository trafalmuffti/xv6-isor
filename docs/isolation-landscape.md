# Containers, sandboxes and virtual machines

"Isolation" covers a wide range of mechanisms, from `chroot` to full
virtual machines, and xv6-isor is one point on that range. This document
places it among the well-known ones and answers the question that decides
everything about an isolation technique: **how much of the host kernel does
the guest's code still reach?** The more it reaches, the faster it runs and
the larger its attack surface.

Contents:

1. [The one question that matters](#the-one-question-that-matters)
2. [The landscape, weakest to strongest isolation](#the-landscape-weakest-to-strongest-isolation)
3. [Where xv6-isor sits](#where-xv6-isor-sits)
4. [The building blocks](#the-building-blocks)
5. [A decision guide](#a-decision-guide)

---

## The one question that matters

Every isolation technique is a trade between **performance** and **attack
surface**, and the dial is: *when the guest makes a system call, whose
kernel code runs?*

```
  more sharing, faster, bigger attack surface
  ◀──────────────────────────────────────────────────────────▶
  same kernel          user-mode kernel            own kernel
  (containers)         (gVisor, xv6-isor)          (virtual machines)
```

- **Share the host kernel** (plain processes, containers): the guest's
  syscalls run the host's kernel code directly. Fastest, but any kernel
  bug is reachable.
- **Interpose a user-mode kernel** (gVisor, xv6-isor): the guest's
  syscalls are handled by a kernel running as a user process, which makes
  only a few, carefully chosen host calls. Slower, much smaller host
  attack surface.
- **Give the guest its own kernel** (virtual machines): the guest runs a
  whole kernel on virtual hardware; the host only emulates devices.
  Strong isolation, highest overhead.

Keep this dial in mind; the table below is just this question asked of each
system.

---

## The landscape, weakest to strongest isolation

| technique | guest's syscalls run... | isolation boundary | shares host kernel? | overhead |
|---|---|---|---|---|
| **plain process** | on the host kernel | DAC + capabilities | fully | none |
| **`chroot`** | on the host kernel | a changed `/` only | fully | none |
| **containers** (Docker/`runc`, LXC) | on the host kernel | namespaces + cgroups + seccomp + caps | fully | near zero |
| **seccomp-sandboxed app** (e.g. a browser renderer) | on the host kernel, if the filter allows | a syscall allow-list | fully | tiny |
| **xv6-isor** | in the sentry; a few rewritten to the host | ptrace interception | only a few host calls | high (ptrace) |
| **gVisor** (`runsc`) | in the Sentry | user-mode kernel + seccomp + namespaces | a minimal host-call set | moderate |
| **microVM** (Firecracker, QEMU microvm) | on the guest's **own** kernel | hardware virtualization (KVM) | no (own kernel; host runs a VMM) | moderate |
| **full VM** (QEMU/KVM, VMware) | on the guest's own kernel | hardware virtualization | no | higher |

The ordering is by how much the guest's code is kept away from the host
kernel, which is roughly inverse to performance.

---

## Where xv6-isor sits

xv6-isor is a **user-mode kernel**, the same category as gVisor, built the
simplest way that still works: `ptrace`. It intercepts every guest system
call and handles it in the sentry (see [syscall-path.md](syscall-path.md)),
passing only a few rewritten calls (`clone`, `execve`, `brk`,
`exit_group`) to the host.

Compared with its neighbors:

- **vs. containers.** A container's processes run their syscalls on the
  host kernel; isolation comes from namespaces, cgroups and seccomp
  *around* the same kernel. xv6-isor instead *replaces* the kernel the
  guest talks to. So a container is far faster but exposes the whole host
  syscall surface; xv6-isor is slow but exposes almost none of it. (It
  also lacks the container's defense-in-depth *around* the sentry, see
  below.)
- **vs. gVisor.** Same idea, same "user-mode kernel" category. The
  differences are maturity and layering:
  - **Interception:** xv6-isor uses `ptrace` (two stops per call); gVisor
    uses `systrap` (a `SIGSYS` in a stub thread, via shared memory) or KVM.
  - **Confinement of the kernel itself:** gVisor wraps its Sentry in
    seccomp, namespaces and an empty mount namespace, with a separate
    gofer for files. xv6-isor's sentry runs with the user's full rights
    and no filter (see [breakout.md](breakout.md) and
    [security-models.md](security-models.md)).
  - **Scope:** gVisor implements a large fraction of Linux; xv6-isor
    implements xv6's ~21 calls.
- **vs. virtual machines.** A VM gives the guest its *own* kernel on
  virtual hardware, so the host only runs a VMM and device emulation.
  xv6-isor shares the host kernel for the handful of calls it forwards and
  for everything non-syscall (page faults, ELF loading). A VM's isolation
  is stronger and hardware-backed; its overhead and memory cost are higher.
  Interestingly, gVisor's KVM platform uses the *same* virtualization
  hardware as a VM, which is why "uses virtualization" alone doesn't make
  something more secure (gVisor's own FAQ makes this point).

In one line: **xv6-isor is a minimal, readable gVisor, built on ptrace,
without the second layer of defense that confines the kernel process
itself.**

---

## The building blocks

The systems above are assembled from a small set of Linux primitives. Each
has its own document or section where xv6-isor touches it.

| primitive | what it isolates | used by | in xv6-isor |
|---|---|---|---|
| **DAC + capabilities** | files and privileged operations, by uid/caps | everything | not present (xv6 has no users); see [security-models.md](security-models.md) |
| **`chroot` / `pivot_root`** | the filesystem root | containers | the moral equivalent is `openat2(RESOLVE_IN_ROOT)`; see [filesystem.md](filesystem.md) |
| **mount namespace** | the set of mounts | containers, gVisor | not used (the sentry runs in the host's) |
| **pid namespace** | process IDs | containers, gVisor | virtual pids in the sentry; see [processes.md](processes.md) |
| **user namespace** | uid/gid ranges | rootless containers, gVisor | not used |
| **network namespace** | interfaces, ports | containers, gVisor | none (no networking) |
| **cgroups** | CPU, memory, pids as resources | containers | not used; resource exhaustion is open (see [breakout.md](breakout.md)) |
| **seccomp** | the set of allowed syscalls | sandboxed apps, gVisor's Sentry | not used on the sentry; the xv6 syscall table is the guest's allow-list |
| **`ptrace`** | syscalls, by interception | debuggers, xv6-isor | the core mechanism; see [syscall-path.md](syscall-path.md) |
| **KVM** | a whole guest kernel | VMs, gVisor's KVM platform | not used |

A container is "just" several of these namespaces plus cgroups plus
seccomp plus capabilities, wrapped by a runtime like `runc`. There is no
single "container" kernel feature. gVisor adds a user-mode kernel *on top*
of several of these. xv6-isor uses one of them (`ptrace`) as its whole
mechanism, and borrows the *idea* of two others (root confinement, virtual
pids) without the kernel feature.

---

## A decision guide

For real workloads (not for xv6-isor, which is a teaching tool), the
trade-off usually goes:

- **Trust the code, want speed:** a **container**. Shares the kernel;
  isolation is namespaces + cgroups + seccomp. The default for most
  deployments.
- **Run untrusted code, want container-like speed and density:**
  **gVisor** or a **microVM** (Firecracker). gVisor shrinks the host
  syscall surface with a user-mode kernel; Firecracker gives each guest
  its own kernel with fast VM startup. Both are used for multi-tenant
  "serverless" platforms.
- **Need the strongest isolation or a different OS:** a **full VM**.
- **Want to learn how a user-mode kernel works:** **xv6-isor**. It makes
  the core technique, intercept syscalls, implement them yourself, legible
  in ~1,600 lines, then points at everything a production system adds on
  top (this `docs/` directory).

The honest placement: xv6-isor is to gVisor what xv6 is to Linux, a small,
readable version built to be understood, not deployed. Its value is that
you can read the whole thing and see exactly where the "real" systems'
extra layers go.

---

## Further reading in this repo

- [syscall-path.md](syscall-path.md): how one system call is intercepted,
  and the cost of doing it with ptrace.
- [processes.md](processes.md): fork/exec/exit/wait and virtual pids.
- [memory.md](memory.md): sbrk, COW fork, demand paging.
- [filesystem.md](filesystem.md): root confinement vs chroot/pivot_root.
- [breakout.md](breakout.md): what isolation xv6-isor does and doesn't
  provide.
- [security-models.md](security-models.md): gVisor's layered security,
  DAC, ACLs, SMACK.

## Sources

- gVisor: `g3doc/architecture_guide/security.md` (user-mode kernel vs VM,
  "Is this just a ptrace sandbox?", the FAQ on virtualization hardware),
  `g3doc/architecture_guide/platforms.md` (ptrace, systrap, KVM).
- Firecracker: its design documentation (microVMs on KVM with a minimal
  VMM).
- Manual pages: `namespaces(7)`, `cgroups(7)`, `seccomp(2)`,
  `capabilities(7)`, `chroot(2)`, `pivot_root(2)`, `ptrace(2)`.
