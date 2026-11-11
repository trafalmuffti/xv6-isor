// isor: only the on-disk directory format is needed by user programs.
// The sentry synthesizes these records when a directory is read.

// Directory is a file containing a sequence of dirent structures.
#define DIRSIZ 14

struct dirent {
  ushort inum;
  char name[DIRSIZ];
};

