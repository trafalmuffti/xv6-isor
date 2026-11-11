// sleep: pause for a number of clock ticks (100 per second).
// (Not part of the original xv6 user programs; handy for trying kill.)

#include "types.h"
#include "user.h"

int
main(int argc, char *argv[])
{
  if(argc != 2){
    printf(2, "usage: sleep ticks\n");
    exit();
  }
  sleep(atoi(argv[1]));
  exit();
}
