# xv6-isor: build the sentry (Go) and the xv6 user programs (C).
#
#   make            build ./xv6-isor and the guest root directory ./fs
#   make run        boot xv6's init + sh with ./fs as "/"
#   make test       run the end-to-end tests

GO      ?= go
CC      ?= gcc
FS      := fs

# The guest programs are freestanding static x86-64 ELF executables: no
# libc, no dynamic linker -- they only make xv6 system calls (usys.S).
CFLAGS  := -O2 -Wall -Wno-unused-result -Wno-infinite-recursion \
           -ffreestanding -fno-builtin -fno-stack-protector -fno-pie \
           -fno-omit-frame-pointer -fno-asynchronous-unwind-tables -Iuser
LDFLAGS := -static -nostdlib -no-pie -Wl,--build-id=none -Wl,-z,noexecstack

ULIB    := user/start.o user/usys.o user/ulib.o user/printf.o user/umalloc.o
UPROGS  := cat echo forktest grep init kill ln ls mkdir rm sh stressfs wc \
           zombie isortest sleep

.PHONY: all run test clean
all: xv6-isor $(addprefix $(FS)/,$(UPROGS)) $(FS)/README

xv6-isor: $(wildcard sentry/*.go) go.mod
	$(GO) build -o $@ ./sentry

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
