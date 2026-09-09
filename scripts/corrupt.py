#!/usr/bin/env python3
"""Corrupt a file in place without changing its length.

The shape of damage that matters: bit rot, a misdirected write, a bad DIMM and a
dropped range all leave the size alone, so nothing that checks lengths — which is
all llama.cpp and Ollama check — can see them. G2c measured the cost: 512
inverted bytes in half a gigabyte and the model stops answering.

    corrupt.py <path> [nbytes] [mode]

    nbytes  how many bytes to damage (default 512)
    mode    invert (default) | zero
            zero is a lost block or a sparse hole, and is usually harmless;
            invert is arbitrary bytes, and usually is not. G2c has the numbers.

Writes at 90% of the file, which is tensor data in any real model.
"""
import os
import sys

path = sys.argv[1]
count = int(sys.argv[2]) if len(sys.argv) > 2 else 512
mode = sys.argv[3] if len(sys.argv) > 3 else "invert"

size = os.path.getsize(path)
off = size * 9 // 10
with open(path, "r+b") as f:
    f.seek(off)
    before = f.read(count)
    after = bytes(len(before)) if mode == "zero" else bytes(b ^ 0xFF for b in before)
    f.seek(off)
    f.write(after)

print("%s %d bytes at offset %d of %d" % (mode, count, off, size))
print("  before %s" % before[:16].hex())
print("  after  %s" % after[:16].hex())
print("  size   %d (unchanged: %s)" % (os.path.getsize(path), os.path.getsize(path) == size))
