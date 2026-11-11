#!/bin/bash
# End-to-end tests: boot init + sh on a scratch copy of fs/ and feed the
# shell a script, then check the console output.
set -u
cd "$(dirname "$0")"

root=$(mktemp -d)
trap 'rm -rf "$root"' EXIT
cp -r fs/. "$root/"
ln -s /etc "$root/escape"          # a symlink pointing out of the root

out=$(timeout 60 ./xv6-isor "$root" 2>&1 <<'SCRIPT'
echo hello xv6
ls | grep sh | wc
mkdir d
cd d
/echo nested > f
/cat f
cd /
rm d/f
rm d
ls d
cat escape/passwd
sleep 10
forktest
isortest
SCRIPT
)
status=$?

fail=0
expect() {
  if grep -qF -- "$1" <<<"$out"; then
    echo "ok   $2"
  else
    echo "FAIL $2 (missing: $1)"; fail=1
  fi
}
reject() {
  if grep -qF -- "$1" <<<"$out"; then
    echo "FAIL $2 (unexpected: $1)"; fail=1
  else
    echo "ok   $2"
  fi
}

expect "init: starting sh"   "init starts the shell"
expect "hello xv6"           "echo"
expect "1 4 30"              "pipeline ls | grep | wc"
expect "nested"              "redirection, mkdir, chdir"
expect "ls: cannot open d"   "rm of file and directory"
expect "cat: cannot open escape/passwd" "symlink cannot escape root"
expect "fork test OK"        "forktest"
expect "ALL TESTS PASSED"    "isortest"
reject "FAIL"                "no isortest failures"
[ "$status" -eq 0 ] && echo "ok   sentry exits cleanly at end of input" ||
  { echo "FAIL sentry exit status $status"; fail=1; }

if [ $fail -ne 0 ]; then
  echo "---- console output ----"; echo "$out"
  exit 1
fi
echo "all end-to-end tests passed"
