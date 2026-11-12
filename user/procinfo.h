// isor: process information returned by the getprocs() system call.
// Not part of xv6; the sentry fills these in (sentry/procinfo.go).

#define PI_RUNNING  1   // executing user code
#define PI_SLEEPING 2   // blocked in a system call (see wchan)
#define PI_ZOMBIE   3   // exited, not yet wait()ed for

struct procinfo {
  int pid;
  int ppid;            // parent pid (0 for init)
  int state;           // PI_*
  int killed;          // kill() was called, process is dying
  char wchan[8];       // system call it is blocked in, e.g. "wait"
  uint64 sz;           // memory size in bytes
  uint64 brk;          // program break (0 until the first sbrk)
  uint cputicks;       // CPU time used, in clock ticks (100/s)
  uint start;          // uptime() tick when the process was created
  char name[16];       // program name
  char cwd[128];       // current directory
  char ofile[NOFILE];  // per fd: 0 closed, 'c' console, 'f' file,
                       // 'd' directory, 'r'/'w' pipe read/write end
};
