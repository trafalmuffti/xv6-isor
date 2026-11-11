// isor: program entry point (replaces xv6's exec() setting up main's args).
//
// The binaries are ordinary static x86-64 ELF files, so the host kernel's
// execve() loads them and lays out the initial stack the Linux way:
//
//   (%rsp)     argc
//   8(%rsp)    argv[0] ... argv[argc-1], NULL
//
// We pass those to main() and call exit() if main ever returns.
.text
.globl _start
_start:
  xorl %ebp, %ebp          // mark the outermost stack frame
  movq (%rsp), %rdi        // argc
  leaq 8(%rsp), %rsi       // argv
  andq $-16, %rsp          // System V ABI: 16-byte aligned before call
  call main
  call exit

.section .note.GNU-stack,"",@progbits
