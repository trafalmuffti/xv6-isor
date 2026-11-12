# Memory

A process needs a way to grow its heap, and `fork` needs to duplicate an
address space. This document covers how xv6-isor handles memory: the one
call it implements (`sbrk`), a register trick it uses to learn the current
break, and the copy-on-write `fork` it gets for free. It then sets these
beside Linux's and gVisor's memory management, and connects to the lazy
allocation and `mmap` in the companion `xv6-lazy-complete-mmap` repo.

The theme again: **the host kernel owns the address space; the sentry
only steers it.** Unlike files or pipes, the sentry has no copy of guest
memory to manage. It translates `sbrk` into `brk` and otherwise lets the
host's virtual memory system do the work.

Contents:

1. [What a guest's memory looks like](#what-a-guests-memory-looks-like)
2. [sbrk and the restart trick](#sbrk-and-the-restart-trick)
3. [Copy-on-write fork, for free](#copy-on-write-fork-for-free)
4. [Reading and writing guest memory](#reading-and-writing-guest-memory)
5. [What xv6-isor leaves out: lazy allocation and mmap](#what-xv6-isor-leaves-out-lazy-allocation-and-mmap)
6. [Comparison](#comparison)

---

## What a guest's memory looks like

A guest is an ordinary static x86-64 ELF, loaded by the host kernel's
`execve`. So its address space is laid out by Linux, not by the sentry:
the text and data segments from the ELF, a stack the kernel sets up, and a
heap that grows upward from the program break. `user/start.S` is the entry
point that reads `argc`/`argv` off that kernel-provided stack (see
[processes.md](processes.md)).

The sentry stores just one number per process: `Proc.brk`, the current
program break (`sentry/kernel.go`). Everything else about the layout lives
in the host kernel's `mm_struct` for that process.

---

## sbrk and the restart trick

xv6's `sbrk(n)` grows the process by `n` bytes and returns the *old*
break; `malloc` (`user/umalloc.c`) is built on it. The host equivalent is
`brk(addr)`, which sets the break to an absolute address. So `sbrk(n)`
becomes `brk(old + n)`. Two things make this interesting.

**The sentry needs to know `old`.** The first time a process calls
`sbrk`, the sentry doesn't yet know where its break is. It can't compute
`old + n` without asking the host. `sysSbrk` (`sentry/syscall.go:288`)
handles this by *restarting* the guest's own system call:

```go
if p.brk == 0 {
    saved := *r
    r.Orig_rax = hostBrk
    r.Rdi = 0                 // brk(0) returns the current break
    return host(func(r *syscall.PtraceRegs) {
        p.brk = r.Rax         // now we know the break
        *r = saved            // restore the guest's original registers
        r.Rax = saved.Orig_rax // put SYS_sbrk back in rax
        r.Rip -= 2            // back up over the 2-byte `syscall` instruction
        p.tracemsg = ""
    })
}
```

It turns this first `sbrk` into `brk(0)`, which returns the current break
without changing it. Then, in the exit hook, it records the break,
restores every original register, and **moves the instruction pointer back
two bytes**, onto the `syscall` instruction (`0f 05`, two bytes on
x86-64). When the guest resumes, it executes its `sbrk` again, and this
time `p.brk` is known, so the normal path runs. This is exactly how a
kernel restarts a system call interrupted by a signal (`ERESTARTSYS`),
and `rip -= 2` is the same back-up Linux does on the `syscall`
instruction.

**The normal path** (`sentry/syscall.go:306`) is then simple:

```go
newBrk := old + n
r.Orig_rax = hostBrk
r.Rdi = newBrk
// exit hook:
if r.Rax == newBrk {        // host brk succeeded
    p.brk = newBrk
    r.Rax = old             // sbrk returns the OLD break
} else {
    r.Rax = -1              // out of memory
}
```

`brk` returns the new break on success, so the hook checks that it got
what it asked for, updates `p.brk`, and returns the *old* break to match
xv6's contract. A shrink (`n < 0`) works the same way. `isortest` checks
grow, the old-break return value, and shrink.

---

## Copy-on-write fork, for free

One of the nicest consequences of letting the host own memory: xv6-isor
gets copy-on-write `fork` without writing any VM code. When `sysFork`
rewrites `fork` into `clone` (see [processes.md](processes.md)), the host
kernel marks the parent's and child's pages copy-on-write and copies each
page only when one of them writes it.

Real xv6 (the version in this project's `user/`) does **not** do this: its
`fork` calls `copyuvm`, which allocates and byte-copies every page of the
parent eagerly. So xv6-isor's `fork` is actually closer to modern Linux
than the xv6 it runs. The sentry does nothing to arrange this; it's a
property of the host `clone` it already uses.

---

## Reading and writing guest memory

System calls like `write`, `read`, `exec` and `fstat` have to move data
between the guest and the sentry. In xv6 the kernel shares the page table
with the process, so `copyin`/`copyout` just walk it. The sentry is a
*separate host process*, so it uses `process_vm_readv` and
`process_vm_writev` (`sentry/mem.go`), which copy directly between two
processes' address spaces. The host allows it because the sentry is the
guest's `ptrace` tracer.

`copyInStr` reads a NUL-terminated string page by page, so a string at the
very end of a mapping doesn't fault by over-reading, the same care xv6's
`fetchstr` takes. See [syscall-path.md](syscall-path.md), step 5, for
where this fits in a call.

An alternative would be to read the guest's memory through
`/proc/<pid>/mem` or `PTRACE_PEEKDATA`. `process_vm_readv` is faster: one
call moves many bytes, where `PTRACE_PEEKDATA` moves eight at a time.

---

## What xv6-isor leaves out: lazy allocation and mmap

xv6-isor implements only `sbrk`, and eagerly: the host `brk` reserves the
address range immediately. It has **no `mmap`**. The companion repository,
`xv6-lazy-complete-mmap`, adds both to *real* xv6 (the in-kernel version),
and it's a good contrast because it shows the machinery xv6-isor avoids by
delegating to the host.

In that repo (`src/mmap.c`, `src/trap.c`):

- **Lazy allocation.** `mmap` and `sbrk` don't allocate physical pages up
  front. They only extend the process's size (or record an `mmap_region`).
  The comment in `mmap.c` is explicit: *"allocuvm to increase the process
  VAS is not applicable in the case of lazy mapping."*
- **A page-fault handler does the work.** When the process first touches a
  lazily-mapped page, the CPU raises a page fault. The handler in
  `trap.c` reads the faulting address from `CR2`, walks the `mmap_region`
  list to check the address is in a valid region with the right
  protection, then calls `kalloc` + `mappages` to back it with a real
  page (and, for file-backed mappings, reads the file in).

That fault-driven allocation is **demand paging**, and it is precisely
what xv6-isor never had to write: when a guest touches a fresh heap page,
the fault goes to the *host* kernel, which demand-pages it as it would for
any Linux process. The sentry isn't even notified. So:

| | xv6 (base) | xv6-lazy-complete-mmap | xv6-isor |
|---|---|---|---|
| `sbrk` | eager `allocuvm` | lazy: grow size, fault later | `brk`; host decides when to page |
| `mmap` | none | lazy, file + anonymous, own fault handler | none (host `mmap` not exposed to guests) |
| page faults | kill the process | handled to back lazy pages | handled by the **host** kernel |
| who writes the pager | xv6 | the lab author | nobody: it's the host's |

If you wanted `mmap` in xv6-isor, the clean way would mirror `exec`:
rewrite the guest `mmap` into a host `mmap` on a file descriptor the
sentry controls, and let the host page it in. That's an exercise below.

---

## Comparison

| concept | xv6 | xv6-isor | Linux | gVisor |
|---|---|---|---|---|
| **heap growth** | `sbrk` → `allocuvm`, eager | `sbrk` → host `brk`; break tracked in `Proc` | `brk`/`sbrk`, lazy | the Sentry manages the guest address space |
| **general mappings** | none | none | `mmap` with demand paging | the Sentry implements `mmap` over host memory |
| **fork** | eager copy (`copyuvm`) | host COW `clone` | COW | the Sentry copies mappings COW |
| **demand paging** | no (fault = death) | yes, done by the host | yes | yes, in the Sentry |
| **who owns page tables** | xv6 | the host kernel | the kernel | the Sentry (systrap/KVM) |
| **cross-space copy** | shared page table | `process_vm_readv/writev` | `copy_to/from_user` | the Sentry maps guest memory |

The key difference from gVisor: **gVisor manages guest memory itself.**
Its platforms (systrap, KVM) give the Sentry control of the guest's
address space and page tables, so it does its own demand paging and COW.
xv6-isor takes the opposite shortcut, leaning entirely on the host's VM
system. That's simpler and needs no VM code, but it's also why the
sentry can't, for instance, implement its own lazy `mmap` without
round-tripping through host calls, and why a guest's memory use is
invisible to the sentry except through `/proc` (which is how `pslist`
reports it; see [the pslist section](../README.md#listing-processes-pslist)).

---

## Try it yourself

- `./xv6-isor -strace fs`, then run anything using `malloc` (like `sh`
  itself), and watch the first `sbrk` restart: you'll see the break get
  established, then the real growth.
- Run `isortest` to see the `sbrk` grow/shrink checks pass.
- Exercises:
  1. Cache the break at `exec` time (reset in the exec hook already sets
     `p.brk = 0`) by reading it once, to avoid the restart trick on the
     first `sbrk`. What's the simplest place to learn the initial break?
  2. Add a guest `mmap(len)` for anonymous memory: rewrite it into a host
     `mmap(NULL, len, PROT_READ|PROT_WRITE, MAP_PRIVATE|MAP_ANONYMOUS,
     -1, 0)` and return the address. How would `munmap` work?
  3. Read `xv6-lazy-complete-mmap`'s `trap.c` fault handler and explain,
     in one paragraph, why xv6-isor needs no equivalent.

---

## Sources

- The code: `sentry/syscall.go` (`sysSbrk`, the restart trick, `hostBrk`),
  `sentry/kernel.go` (`Proc.brk`, `sysFork` → `clone`), `sentry/mem.go`
  (`process_vm_readv/writev`, `copyInStr`), `user/umalloc.c`.
- Companion repo `xv6-lazy-complete-mmap`: `src/mmap.c`, `src/trap.c`
  (lazy allocation and the page-fault handler), commit "Adding xv6 support
  for file backed and anonymous mmap with lazy allocation".
- gVisor: `g3doc/architecture_guide/platforms.md` (the platform owns the
  address space).
- Manual pages: `brk(2)`, `mmap(2)`, `process_vm_readv(2)`, `clone(2)`
  (COW semantics), `ptrace(2)` (`PTRACE_PEEKDATA`).
