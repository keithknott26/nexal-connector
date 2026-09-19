"""Independent ARM instruction assembly/emulation checks, NOT native HVF tests.

Optional developer-only dependencies are pinned in requirements-test.txt.
Nothing here changes macOS or runs a macOS guest.
"""
import re
import struct
import unittest
from pathlib import Path

from keystone import Ks, KS_ARCH_ARM64, KS_MODE_LITTLE_ENDIAN
from unicorn import Uc, UC_ARCH_ARM64, UC_MODE_ARM, UC_HOOK_MEM_READ, UC_HOOK_MEM_WRITE
from unicorn.arm64_const import (
    UC_ARM64_REG_X0, UC_ARM64_REG_X1, UC_ARM64_REG_X2, UC_ARM64_REG_X5,
)

ROOT = Path(__file__).resolve().parents[1]
HEADER = (ROOT / "native/guest_code.h").read_text()
ASM = (ROOT / "native/guest.S").read_text()
PAGE = 16384
CODE = 0x10000
DATA = 0x200000
MASK = (1 << 64) - 1


def code(name):
    text = re.search(rf"{name}_code\[\]\s*=\s*\{{(.*?)\}}", HEADER, re.S)[1]
    words = [int(x, 16) for x in re.findall(r"0x[0-9a-f]+", text)]
    return struct.pack("<" + "I" * len(words), *words)


def seed(page):
    return 0x9E3779B97F4A7C15 ^ (((page + 1) * 0xD1B54A32D192ED03) & MASK)


def pattern(page):
    x = seed(page)
    data = bytearray()
    for _ in range(PAGE // 8):
        data += struct.pack("<Q", x)
        x ^= (x << 13) & MASK
        x ^= x >> 7
        x ^= (x << 17) & MASK
    return data


class GuestTests(unittest.TestCase):
    def test_encodings_match_assembly(self):
        clean = re.sub(r"/\*.*?\*/", "", ASM, flags=re.S)
        parts = {"fill": clean[:clean.index("check:")], "check": clean[clean.index("check:"):]}
        ks = Ks(KS_ARCH_ARM64, KS_MODE_LITTLE_ENDIAN)
        for name, source in parts.items():
            with self.subTest(name=name):
                assembled, _ = ks.asm(source)
                self.assertEqual(code(name), bytes(assembled))

    def machine(self, name, page):
        uc = Uc(UC_ARCH_ARM64, UC_MODE_ARM)
        uc.mem_map(CODE, PAGE)
        uc.mem_map(DATA, PAGE)
        uc.mem_write(CODE, code(name))
        uc.reg_write(UC_ARM64_REG_X0, seed(page))
        uc.reg_write(UC_ARM64_REG_X1, DATA)
        uc.reg_write(UC_ARM64_REG_X2, PAGE // 8)
        uc.reg_write(UC_ARM64_REG_X5, 0)
        return uc

    def execute(self, uc, name):
        # Stop before HVC: emulator success does not test HVF's exit behavior.
        uc.emu_start(CODE, CODE + len(code(name)) - 4, timeout=1_000_000, count=25000)
        self.assertEqual(uc.reg_read(UC_ARM64_REG_X2), 0)

    def test_every_word_written_and_checked(self):
        for page in (0, 31, 255):
            with self.subTest(page=page):
                writes, reads = [], []
                fill = self.machine("fill", page)
                fill.hook_add(UC_HOOK_MEM_WRITE, lambda u, a, addr, size, val, arg: writes.append((addr, size)))
                self.execute(fill, "fill")
                output = bytes(fill.mem_read(DATA, PAGE))
                self.assertEqual(output, pattern(page))
                check = self.machine("check", page)
                check.mem_write(DATA, output)
                check.hook_add(UC_HOOK_MEM_READ, lambda u, a, addr, size, val, arg: reads.append((addr, size)))
                self.execute(check, "check")
                expected = [(DATA + offset, 8) for offset in range(0, PAGE, 8)]
                self.assertEqual(writes, expected)
                self.assertEqual(reads, expected)
                self.assertEqual(check.reg_read(UC_ARM64_REG_X5), 0)

    def test_corruption_detected(self):
        for offset in (0, 8192, PAGE - 1):
            with self.subTest(offset=offset):
                data = pattern(3)
                data[offset] ^= 1
                check = self.machine("check", 3)
                check.mem_write(DATA, bytes(data))
                self.execute(check, "check")
                self.assertNotEqual(check.reg_read(UC_ARM64_REG_X5), 0)


if __name__ == "__main__":
    unittest.main(verbosity=2)
