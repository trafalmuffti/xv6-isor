// isortest: exercises each xv6 system call through the sentry.
// (A much smaller cousin of xv6's usertests.)

#include "types.h"
#include "stat.h"
#include "user.h"
#include "fcntl.h"
#include "fs.h"
#include "param.h"
#include "procinfo.h"

static int failures;

#define CHECK(cond, msg) do { \
    if(cond) printf(1, "ok   %s\n", msg); \
    else { printf(1, "FAIL %s\n", msg); failures++; } \
  } while(0)

void
filetest(void)
{
  struct stat st;
  char buf[32];
  int fd;

  fd = open("isortest.tmp", O_CREATE|O_RDWR);
  CHECK(fd >= 0, "open O_CREATE");
  CHECK(write(fd, "hello, sentry", 13) == 13, "write file");
  CHECK(fstat(fd, &st) == 0 && st.type == T_FILE && st.size == 13, "fstat file");
  close(fd);

  fd = open("isortest.tmp", O_RDONLY);
  memset(buf, 0, sizeof(buf));
  CHECK(read(fd, buf, sizeof(buf)) == 13 && strcmp(buf, "hello, sentry") == 0, "read file");
  CHECK(read(fd, buf, sizeof(buf)) == 0, "read at EOF");
  CHECK(write(fd, "x", 1) == -1, "write to read-only fd fails");
  close(fd);
  CHECK(read(fd, buf, 1) == -1, "read closed fd fails");

  CHECK(link("isortest.tmp", "isortest.lnk") == 0, "link");
  CHECK(stat("isortest.lnk", &st) == 0 && st.nlink == 2, "link count");
  CHECK(unlink("isortest.tmp") == 0 && unlink("isortest.lnk") == 0, "unlink");
  CHECK(open("isortest.tmp", O_RDONLY) == -1, "unlinked file is gone");

  CHECK(mkdir("isortest.d") == 0, "mkdir");
  CHECK(mkdir("isortest.d") == -1, "mkdir existing fails");
  CHECK(open("isortest.d", O_RDWR) == -1, "open dir for write fails");
  CHECK(chdir("isortest.d") == 0, "chdir");
  fd = open("f", O_CREATE|O_WRONLY);
  CHECK(fd >= 0, "create in subdir");
  close(fd);
  CHECK(chdir("..") == 0, "chdir ..");
  CHECK(unlink("isortest.d") == -1, "unlink non-empty dir fails");
  CHECK(unlink("isortest.d/f") == 0 && unlink("isortest.d") == 0, "unlink dir");

  CHECK(chdir("/../../..") == 0 && open("init", O_RDONLY) >= 0, "can't climb above /");
  CHECK(open("/proc/self/environ", O_RDONLY) == -1, "host files are invisible");
}

void
dirtest(void)
{
  struct dirent de;
  int fd, found = 0;

  fd = open(".", O_RDONLY);
  while(read(fd, &de, sizeof(de)) == sizeof(de))
    if(de.inum != 0 && strcmp(de.name, "isortest") == 0)
      found = 1;
  close(fd);
  CHECK(found, "read directory entries");
}

void
pipetest(void)
{
  int fds[2], pid, i, n, total;
  char buf[100];

  CHECK(pipe(fds) == 0, "pipe");
  pid = fork();
  if(pid == 0){
    close(fds[0]);
    // More than PIPESIZE so the writer has to block.
    for(i = 0; i < 20; i++)
      write(fds[1], "0123456789012345678901234567890123456789012345678901234567890123456789", 70);
    exit();
  }
  close(fds[1]);
  total = 0;
  while((n = read(fds[0], buf, sizeof(buf))) > 0)
    total += n;
  close(fds[0]);
  CHECK(total == 20*70, "pipe transfers data across fork");
  CHECK(wait() == pid, "wait returns child pid");
}

void
proctest(void)
{
  int pid, i, start;
  char *a, *b;

  CHECK(getpid() > 0, "getpid");
  CHECK(wait() == -1, "wait without children");

  for(i = 0; i < 10; i++){
    pid = fork();
    if(pid == 0)
      exit();
  }
  for(i = 0; i < 10; i++)
    if(wait() < 0)
      break;
  CHECK(i == 10, "fork 10 children and reap them");

  pid = fork();
  if(pid == 0){
    for(;;)
      ;
  }
  CHECK(kill(pid) == 0 && wait() == pid, "kill a spinning child");
  CHECK(kill(12345) == -1, "kill unknown pid fails");

  start = uptime();
  CHECK(sleep(20) == 0 && uptime() - start >= 19, "sleep and uptime");

  a = sbrk(4096);
  CHECK(a != (char*)-1, "sbrk grow");
  a[0] = 'x'; a[4095] = 'y';
  b = sbrk(0);
  CHECK(b == a + 4096, "sbrk returns old break");
  CHECK(sbrk(-4096) == b && sbrk(0) == a, "sbrk shrink");

  a = malloc(100000);
  CHECK(a != 0, "malloc via sbrk");
  free(a);

  pid = fork();
  if(pid == 0){
    char *argv[] = { "echo", "exec", "works", 0 };
    exec("echo", argv);
    printf(1, "exec failed\n");
    exit();
  }
  wait();
  CHECK(exec("nonexistent", 0) == -1, "exec missing file fails");
  CHECK(exec("README", 0) == -1, "exec non-ELF fails");

  pid = fork();
  if(pid == 0){
    *(volatile int*)0 = 1;    // page fault
    exit();
  }
  CHECK(wait() == pid, "faulting child is killed");
}

void
sandboxtest(void)
{
  long r;
  int pid;

  // Linux system call 39 is getpid(); the sentry only knows xv6's
  // numbers, so the host kernel never sees it.
  asm volatile("syscall" : "=a"(r) : "a"(39L) : "rcx", "r11", "memory");
  CHECK(r == -1, "Linux system calls are rejected");

  // The 32-bit entry path (int $0x80) uses a different syscall table;
  // the sentry kills a process that tries it.
  pid = fork();
  if(pid == 0){
    asm volatile("int $0x80" : "=a"(r) : "a"(7L) : "memory");
    printf(1, "int $0x80 was not stopped!\n");
    exit();
  }
  CHECK(wait() == pid, "int $0x80 kills the process");

  CHECK(open("escape/etc/passwd", O_RDONLY) == -1, "symlinks can't leave the root");
}

struct procinfo pinfo[NPROC];

struct procinfo*
findproc(int n, int pid)
{
  int i;
  for(i = 0; i < n; i++)
    if(pinfo[i].pid == pid)
      return &pinfo[i];
  return 0;
}

void
procinfotest(void)
{
  struct procinfo *pi;
  int n, pid;

  n = getprocs(pinfo, NPROC);
  CHECK(n >= 3, "getprocs lists processes");
  pi = findproc(n, getpid());
  CHECK(pi && strcmp(pi->name, "isortest") == 0 && pi->state == PI_RUNNING &&
        pi->ofile[1] == 'c', "getprocs describes the caller");
  CHECK(pinfo[0].pid == 1 && pinfo[0].state == PI_SLEEPING &&
        strcmp(pinfo[0].wchan, "wait") == 0, "init is sleeping in wait");

  pid = fork();
  if(pid == 0){
    for(;;)
      ;
  }
  sleep(30);
  n = getprocs(pinfo, NPROC);
  pi = findproc(n, pid);
  CHECK(pi && pi->ppid == getpid() && pi->state == PI_RUNNING && pi->cputicks > 0,
        "spinning child shows up using CPU");
  kill(pid);
  wait();
  n = getprocs(pinfo, NPROC);
  CHECK(findproc(n, pid) == 0, "reaped child is gone");

  CHECK(getprocs(pinfo, 1) == 1, "getprocs respects max");
  CHECK(getprocs((struct procinfo*)1, NPROC) == -1, "getprocs with bad pointer fails");
}

int
main(int argc, char *argv[])
{
  printf(1, "isortest starting\n");
  filetest();
  dirtest();
  pipetest();
  proctest();
  sandboxtest();
  procinfotest();
  if(failures)
    printf(1, "isortest: %d FAILED\n", failures);
  else
    printf(1, "ALL TESTS PASSED\n");
  exit();
}
