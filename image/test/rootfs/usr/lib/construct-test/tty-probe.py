#!/usr/bin/python3
# A program in a terminal for cmd/image test's shortcuts matrix: it puts its terminal in raw mode
# with no signals and no flow control, so every byte a key sends reaches it (^C, ^S, ^Z too),
# turns on bracketed paste so a paste is told from typing, and appends what it reads to OUT (its
# first argument) as hex, one line per read. F9 (ESC [ 2 0 ~) is the harness's mark between
# chords; the terminal shows "abc" for a select-all to take.
import os
import sys
import termios
import tty

out = sys.argv[1]
fd = sys.stdin.fileno()
tty.setraw(fd)
attrs = termios.tcgetattr(fd)
attrs[0] &= ~(termios.IXON | termios.IXOFF)
termios.tcsetattr(fd, termios.TCSANOW, attrs)
title = sys.argv[2] if len(sys.argv) > 2 else "construct-tty-probe"
os.write(sys.stdout.fileno(), b"\x1b]2;" + title.encode() + b"\x07\x1b[?2004habc")
os.makedirs(os.path.dirname(out), exist_ok=True)
with open(out, "a", buffering=1) as f:
    f.write("start\n")
    while True:
        b = os.read(fd, 4096)
        if not b:
            break
        f.write(b.hex() + "\n")
