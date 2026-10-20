"""Portable PE import check fixtures; no compiler or Windows host required."""

import struct
import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from windows_native import NativeDependencyError, validate_portable_windows_dll


def fixture_pe(imports=("KERNEL32.dll",), machine=0x8664, delay=False):
    data = bytearray(0x600)
    data[:2] = b"MZ"
    struct.pack_into("<I", data, 0x3c, 0x80)
    data[0x80:0x84] = b"PE\0\0"
    struct.pack_into("<HH", data, 0x84, machine, 1)
    struct.pack_into("<HH", data, 0x94, 0xf0, 0x2022)
    struct.pack_into("<H", data, 0x98, 0x20b)
    entry_size = 32 if delay else 20
    directory_size = entry_size * (len(imports) + 1)
    struct.pack_into("<II", data, 0x170 if delay else 0x110, 0x1000, directory_size)
    section = 0x188
    data[section:section + 8] = b".rdata\0\0"
    struct.pack_into("<IIII", data, section + 8, 0x400, 0x1000, 0x400, 0x200)
    name_offset = 0x200 + directory_size
    for index, imported in enumerate(imports):
        encoded = imported.encode("ascii") + b"\0"
        if delay:
            struct.pack_into("<I", data, 0x200 + entry_size * index, 1)
        struct.pack_into("<I", data, 0x200 + entry_size * index + (4 if delay else 12),
                         0x1000 + name_offset - 0x200)
        data[name_offset:name_offset + len(encoded)] = encoded
        name_offset += len(encoded)
    return bytes(data)


class WindowsNativeTest(unittest.TestCase):
    def test_known_folder_system_imports(self):
        for delay in (False, True):
            with self.subTest(delay=delay):
                imports = ("KERNEL32.dll", "SHELL32.dll", "ole32.dll")
                self.assertEqual(validate_portable_windows_dll(
                    fixture_pe(imports, delay=delay), "win_amd64"), imports)

    def test_delay_imports_keep_runtime_and_unknown_refusals(self):
        for name in ("VCRUNTIME140.dll", "MSVCP140.dll", "libgcc_s_seh-1.dll",
                     "unreviewed.dll", "uuid.dll", "shell32-extra.dll", "ole32-extra.dll"):
            for delay in (False, True):
                with self.subTest(name=name, delay=delay), self.assertRaises(NativeDependencyError):
                    validate_portable_windows_dll(fixture_pe((name,), delay=delay), "win_amd64")

    def test_delay_va_and_unterminated_table_still_refuse(self):
        data = bytearray(fixture_pe(("SHELL32.dll",), delay=True))
        struct.pack_into("<I", data, 0x200, 0)
        with self.assertRaisesRegex(NativeDependencyError, "VA-based"):
            validate_portable_windows_dll(bytes(data), "win_amd64")
        data = bytearray(fixture_pe(("SHELL32.dll",), delay=True))
        struct.pack_into("<I", data, 0x174, 32)
        with self.assertRaisesRegex(NativeDependencyError, "terminator"):
            validate_portable_windows_dll(bytes(data), "win_amd64")

    def test_accepts_system_imports(self):
        self.assertEqual(validate_portable_windows_dll(fixture_pe(("KERNEL32.dll", "msi.dll")), "win_amd64"),
                         ("KERNEL32.dll", "msi.dll"))

    def test_refuses_msvc_and_mingw_runtimes(self):
        for runtime in ("VCRUNTIME140_1.dll", "MSVCP140.dll", "libgcc_s_seh-1.dll",
                        "libstdc++-6.dll", "libwinpthread-1.dll"):
            with self.subTest(runtime=runtime), self.assertRaisesRegex(NativeDependencyError, runtime.replace("+", r"\+")):
                validate_portable_windows_dll(fixture_pe(("KERNEL32.dll", runtime)), "win_amd64")

    def test_refuses_wrong_architecture_or_malformed_imports(self):
        with self.assertRaises(NativeDependencyError):
            validate_portable_windows_dll(fixture_pe(machine=0xaa64), "win_amd64")
        with self.assertRaises(NativeDependencyError):
            validate_portable_windows_dll(b"not a DLL", "win_amd64")

    def test_refuses_unqualified_dependency(self):
        for name in ("libssl.dll", "api-ms-win-crt-unreviewed-l1-1-0.dll"):
            with self.subTest(name=name), self.assertRaisesRegex(NativeDependencyError, name):
                validate_portable_windows_dll(fixture_pe(("KERNEL32.dll", name)), "win_amd64")

    def test_accepts_windows_ucrt_api_set(self):
        imports = ("KERNEL32.dll", "api-ms-win-crt-string-l1-1-0.dll")
        self.assertEqual(validate_portable_windows_dll(fixture_pe(imports), "win_amd64"), imports)


if __name__ == "__main__":
    unittest.main()
