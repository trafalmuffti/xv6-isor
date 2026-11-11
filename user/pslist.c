// pslist: list every process and what the kernel knows about it.
// (isor addition, built on the getprocs() system call.)
//
//   $ pslist
//   PID PPID STATE  WCHAN  MEM    CPU   AGE  BRK        NAME    FDS  CWD
//   1   0    sleep  wait   12K    0.00  9    -          init    ccc  /
//   ...

#include "types.h"
#include "stat.h"
#include "user.h"
#include "param.h"
#include "procinfo.h"

struct procinfo table[NPROC];

// Each row is built in a buffer and written with one write() call.
static char line[256];
static int len;

static void
add(const char *s)
{
  while(*s && len < sizeof(line) - 2)
    line[len++] = *s++;
}

// col adds s, padded with spaces to width characters (at least one space).
static void
col(const char *s, int width)
{
  int start = len;
  add(s);
  do {
    add(" ");
  } while(len - start < width);
}

static void
endline(void)
{
  line[len++] = '\n';
  write(1, line, len);
  len = 0;
}

// fmtnum formats n in the given base into buf.
static char*
fmtnum(char *buf, uint64 n, int base)
{
  static char digits[] = "0123456789abcdef";
  char tmp[24];
  int i = 0, j = 0;

  do {
    tmp[i++] = digits[n % base];
    n /= base;
  } while(n);
  while(i > 0)
    buf[j++] = tmp[--i];
  buf[j] = 0;
  return buf;
}

// fmtticks formats clock ticks (100 per second) as seconds, e.g. "1.25".
static char*
fmtticks(char *buf, uint ticks)
{
  char frac[4];
  int n;

  fmtnum(buf, ticks / 100, 10);
  n = strlen(buf);
  fmtnum(frac, ticks % 100, 10);
  buf[n++] = '.';
  if(ticks % 100 < 10)
    buf[n++] = '0';
  strcpy(buf + n, frac);
  return buf;
}

static char*
statename(struct procinfo *pi)
{
  if(pi->state == PI_ZOMBIE)
    return "zombie";
  if(pi->killed)
    return "dying";
  if(pi->state == PI_SLEEPING)
    return "sleep";
  return "run";
}

int
main(int argc, char *argv[])
{
  struct procinfo *pi;
  char buf[32], fds[NOFILE + 1];
  int i, j, n, now, last;

  n = getprocs(table, NPROC);
  if(n < 0){
    printf(2, "pslist: getprocs failed\n");
    exit();
  }
  now = uptime();

  col("PID", 4); col("PPID", 5); col("STATE", 7); col("WCHAN", 7);
  col("MEM", 7); col("CPU", 6); col("AGE", 5); col("BRK", 11);
  col("NAME", 10); col("FDS", NOFILE + 1); add("CWD");
  endline();

  for(i = 0; i < n; i++){
    pi = &table[i];
    col(fmtnum(buf, pi->pid, 10), 4);
    col(fmtnum(buf, pi->ppid, 10), 5);
    col(statename(pi), 7);
    col(pi->wchan[0] ? pi->wchan : "-", 7);
    if(pi->state == PI_ZOMBIE)
      col("-", 7);
    else {
      fmtnum(buf, pi->sz / 1024, 10);
      strcpy(buf + strlen(buf), "K");
      col(buf, 7);
    }
    col(fmtticks(buf, pi->cputicks), 6);
    col(fmtnum(buf, (now - pi->start) / 100, 10), 5);
    if(pi->brk){
      strcpy(buf, "0x");
      fmtnum(buf + 2, pi->brk, 16);
      col(buf, 11);
    } else
      col("-", 11);
    col(pi->name, 10);

    // One character per descriptor, '.' for closed ones (trailing
    // closed descriptors are left out).
    last = -1;
    for(j = 0; j < NOFILE; j++){
      fds[j] = pi->ofile[j] ? pi->ofile[j] : '.';
      if(pi->ofile[j])
        last = j;
    }
    fds[last + 1] = 0;
    col(last >= 0 ? fds : "-", NOFILE + 1);
    add(pi->cwd[0] ? pi->cwd : "-");
    endline();
  }

  printf(1, "\nCPU and AGE in seconds. FDS: one letter per fd from 0: "
            "c console, f file, d directory, r/w pipe read/write end, . closed\n");
  exit();
}
