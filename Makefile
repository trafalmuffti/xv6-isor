# xv6-isor: build the sentry (Go) and the xv6 user programs (C).
#
#   make                  build ./xv6-isor and the guest root directory ./fs
#   make run              boot xv6's init + sh with ./fs as "/"
#   make test             run the end-to-end tests
#   make GOCOMPILER=gccgo build the sentry with gccgo instead of the go tool
#
# After switching GOCOMPILER, run "make clean" (or rm xv6-isor) so the
# sentry is rebuilt with the other compiler.

CC      ?= gcc
FS      := fs

# Go compiler for the sentry: gc (the `go` tool, the default) or gccgo.
# gccgo compiles the sentry's .go files directly, like C, and links them
# against libgo; no Go toolchain or module download is needed. It needs
# a gccgo whose libgo implements Go 1.21 or newer (atomic.Bool, min);
# GCC 15 and earlier implement Go 1.18 and cannot build the sentry yet.
GOCOMPILER ?= gc
GCCGO      ?= gccgo
GCCGOFLAGS ?= -O2 -g
GO         ?= go
SENTRY_SRC := $(wildcard sentry/*.go)

ifeq ($(filter $(GOCOMPILER),gc gccgo),)
  $(error GOCOMPILER must be gc or gccgo, not "$(GOCOMPILER)")
endif
ifeq ($(GOCOMPILER),gccgo)
  ifeq ($(shell command -v $(GCCGO) 2>/dev/null),)
    $(error GOCOMPILER=gccgo but $(GCCGO) was not found; install gccgo or set GCCGO=/path/to/gccgo)
  endif
endif

# The guest programs are freestanding static x86-64 ELF executables: no
# libc, no dynamic linker -- they only make xv6 system calls (usys.S).
CFLAGS  := -O2 -Wall -Wno-unused-result -Wno-infinite-recursion \
           -ffreestanding -fno-builtin -fno-stack-protector -fno-pie \
           -fno-omit-frame-pointer -fno-asynchronous-unwind-tables -Iuser
LDFLAGS := -static -nostdlib -no-pie -Wl,--build-id=none -Wl,-z,noexecstack

ULIB    := user/start.o user/usys.o user/ulib.o user/printf.o user/umalloc.o
UPROGS  := cat echo forktest grep init kill ln ls mkdir rm sh stressfs wc \
           zombie isortest sleep pslist

.PHONY: all run test clean
all: xv6-isor $(addprefix $(FS)/,$(UPROGS)) $(FS)/README

xv6-isor: $(SENTRY_SRC) go.mod
ifeq ($(GOCOMPILER),gccgo)
	$(GCCGO) $(GCCGOFLAGS) -o $@ $(SENTRY_SRC)
else
	$(GO) build -o $@ ./sentry
endif

user/%.o: user/%.c user/*.h
	$(CC) $(CFLAGS) -c -o $@ $<

user/%.o: user/%.S user/syscall.h
	$(CC) $(CFLAGS) -c -o $@ $<

$(FS)/%: user/%.o $(ULIB)
	@mkdir -p $(FS)
	$(CC) $(LDFLAGS) -o $@ $(ULIB) $<

# forktest has its own tiny printf and avoids malloc, as in xv6.
$(FS)/forktest: user/forktest.o user/start.o user/usys.o user/ulib.o
	@mkdir -p $(FS)
	$(CC) $(LDFLAGS) -o $@ $^

$(FS)/README: README.md
	@mkdir -p $(FS)
	cp $< $@

run: all
	./xv6-isor $(FS)

test: all
	./test.sh

clean:
	rm -rf xv6-isor $(FS) user/*.o

.PRECIOUS: user/%.o
