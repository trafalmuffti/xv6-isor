// isor: system call stubs for x86-64.
//
// xv6 used `int $T_SYSCALL`; we use the x86-64 `syscall` instruction so the
// host kernel's ptrace machinery reports a syscall-entry stop to the sentry.
// Arguments are already in %rdi, %rsi, %rdx (System V calling convention),
// which is also where the Linux syscall ABI expects them.
#include "syscall.h"

#define SYSCALL(name) \
  .globl name; \
  name: \
    movl $SYS_ ## name, %eax; \
    syscall; \
    ret

SYSCALL(fork)
SYSCALL(exit)
SYSCALL(wait)
SYSCALL(pipe)
SYSCALL(read)
SYSCALL(write)
SYSCALL(close)
SYSCALL(kill)
SYSCALL(exec)
SYSCALL(open)
SYSCALL(mknod)
SYSCALL(unlink)
SYSCALL(fstat)
SYSCALL(link)
SYSCALL(mkdir)
SYSCALL(chdir)
SYSCALL(dup)
SYSCALL(getpid)
SYSCALL(sbrk)
SYSCALL(sleep)
SYSCALL(uptime)

.section .note.GNU-stack,"",@progbits
