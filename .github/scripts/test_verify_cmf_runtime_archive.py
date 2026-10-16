"""Tests for the metadata-only CMF runtime archive verifier.

The fixtures below are deliberately synthetic archive records, not CMF files or
models.  They exercise packaging safety policy without claiming that fabricated
bytes are runnable Cortiq inputs.
"""

from __future__ import annotations

import gzip
import importlib.util
import io
import os
from pathlib import Path
import stat
import struct
import tarfile
import tempfile
import unittest
from unittest import mock
import zipfile


SCRIPT = Path(__file__).with_name("verify_cmf_runtime_archive.py")
SPEC = importlib.util.spec_from_file_location("verify_cmf_runtime_archive", SCRIPT)
assert SPEC is not None and SPEC.loader is not None
verifier = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(verifier)


class RuntimeArchiveVerifierTest(unittest.TestCase):
    def setUp(self) -> None:
        self.tempdir = tempfile.TemporaryDirectory()
        self.addCleanup(self.tempdir.cleanup)
        self.root = Path(self.tempdir.name)

    def tar_path(self, name: str = "runtime.tar.gz") -> Path:
        return self.root / name

    def zip_path(self, name: str = "runtime.zip") -> Path:
        return self.root / name

    def output_dir(self, name: str = "output") -> Path:
        path = self.root / name
        path.mkdir()
        return path

    def assert_private_extraction(self, destination: Path, payload: bytes) -> None:
        info = destination.lstat()
        self.assertTrue(stat.S_ISREG(info.st_mode))
        self.assertEqual(destination.read_bytes(), payload)
        if os.name != "nt":
            self.assertEqual(stat.S_IMODE(info.st_mode), 0o700)

    @staticmethod
    def _tar_regular(name: str, payload: bytes, mode: int = 0o755) -> tarfile.TarInfo:
        member = tarfile.TarInfo(name)
        member.mode = mode
        member.size = len(payload)
        return member

    def _write_tar(
        self,
        path: Path,
        records: list[tuple[tarfile.TarInfo, bytes | None]],
    ) -> None:
        with tarfile.open(path, "w:gz") as archive:
            for member, payload in records:
                archive.addfile(
                    member,
                    io.BytesIO(payload) if payload is not None else None,
                )

    @staticmethod
    def _zip_regular(name: str, payload: bytes, mode: int = 0o755) -> zipfile.ZipInfo:
        member = zipfile.ZipInfo(name)
        member.create_system = 3  # Unix external attributes.
        member.external_attr = (stat.S_IFREG | mode) << 16
        return member

    def _write_zip(
        self,
        path: Path,
        records: list[tuple[zipfile.ZipInfo, bytes]],
    ) -> None:
        with zipfile.ZipFile(path, "w", compression=zipfile.ZIP_DEFLATED) as archive:
            for member, payload in records:
                archive.writestr(member, payload)

    def test_accepts_one_owner_executable_regular_tar_member(self) -> None:
        path = self.tar_path()
        payload = b"cortiq runtime"
        self._write_tar(path, [(self._tar_regular("cortiq", payload), payload)])

        verifier.validate_archive(path, "tar.gz", "cortiq")

    def test_extracts_one_regular_tar_member_to_private_output(self) -> None:
        path = self.tar_path()
        payload = b"cortiq runtime"
        self._write_tar(path, [(self._tar_regular("cortiq", payload), payload)])
        output = self.output_dir()

        destination = verifier.extract_archive(path, "tar.gz", "cortiq", output)

        self.assertEqual(destination, output / "cortiq")
        self.assert_private_extraction(destination, payload)

    def test_rejects_tar_symbolic_link(self) -> None:
        path = self.tar_path()
        member = tarfile.TarInfo("cortiq")
        member.type = tarfile.SYMTYPE
        member.linkname = "elsewhere"
        self._write_tar(path, [(member, None)])

        with self.assertRaisesRegex(verifier.ValidationError, "symbolic link"):
            verifier.validate_archive(path, "tar.gz", "cortiq")

    def test_rejects_tar_hard_link(self) -> None:
        path = self.tar_path()
        member = tarfile.TarInfo("cortiq")
        member.type = tarfile.LNKTYPE
        member.linkname = "elsewhere"
        self._write_tar(path, [(member, None)])

        with self.assertRaisesRegex(verifier.ValidationError, "hard link"):
            verifier.validate_archive(path, "tar.gz", "cortiq")

    def test_rejects_tar_directory(self) -> None:
        path = self.tar_path()
        member = tarfile.TarInfo("cortiq")
        member.type = tarfile.DIRTYPE
        member.mode = 0o755
        self._write_tar(path, [(member, None)])

        with self.assertRaisesRegex(verifier.ValidationError, "directory"):
            verifier.validate_archive(path, "tar.gz", "cortiq")

    def test_rejects_tar_multiple_members(self) -> None:
        path = self.tar_path()
        first = b"cortiq runtime"
        second = b"unexpected"
        self._write_tar(
            path,
            [
                (self._tar_regular("cortiq", first), first),
                (self._tar_regular("extra", second), second),
            ],
        )

        with self.assertRaisesRegex(verifier.ValidationError, "exactly one entry"):
            verifier.validate_archive(path, "tar.gz", "cortiq")

    def test_rejects_tar_non_executable_member(self) -> None:
        path = self.tar_path()
        payload = b"cortiq runtime"
        self._write_tar(
            path,
            [(self._tar_regular("cortiq", payload, mode=0o644), payload)],
        )

        with self.assertRaisesRegex(verifier.ValidationError, "not owner-executable"):
            verifier.validate_archive(path, "tar.gz", "cortiq")

    def test_rejects_empty_tar_member(self) -> None:
        path = self.tar_path()
        self._write_tar(path, [(self._tar_regular("cortiq", b""), b"")])

        with self.assertRaisesRegex(verifier.ValidationError, "is empty"):
            verifier.validate_archive(path, "tar.gz", "cortiq")

    def test_rejects_tar_member_above_bound(self) -> None:
        path = self.tar_path()
        payload = b"123456789"
        self._write_tar(path, [(self._tar_regular("cortiq", payload), payload)])

        with self.assertRaisesRegex(verifier.ValidationError, "uncompressed limit"):
            verifier.validate_archive(
                path, "tar.gz", "cortiq", max_uncompressed_bytes=8
            )

    def test_rejects_oversized_tar_header_before_skipping_payload(self) -> None:
        path = self.tar_path("oversized-header.tar.gz")
        member = tarfile.TarInfo("cortiq")
        member.mode = 0o755
        member.size = verifier.DEFAULT_MAX_UNCOMPRESSED_BYTES + 1
        # Deliberately write only the header.  The verifier must reject its
        # declared size before tarfile tries to skip/decompress its missing,
        # potentially enormous payload to look for a second entry.
        with gzip.open(path, "wb") as archive:
            archive.write(member.tobuf())

        with self.assertRaisesRegex(verifier.ValidationError, "uncompressed limit"):
            verifier.validate_archive(path, "tar.gz", "cortiq")

    def test_rejects_pax_header_before_reading_its_declared_payload(self) -> None:
        path = self.tar_path("pax-header.tar.gz")
        member = tarfile.TarInfo("PaxHeader")
        member.type = tarfile.XHDTYPE
        member.mode = 0o644
        member.size = verifier.DEFAULT_MAX_UNCOMPRESSED_BYTES + 1
        # Deliberately omit the extension payload. A safe verifier recognizes
        # and rejects the extension record from its first 512-byte header;
        # tarfile.next() historically tried to consume PAX/GNU payload first.
        with gzip.open(path, "wb") as archive:
            archive.write(member.tobuf())

        with self.assertRaisesRegex(verifier.ValidationError, "PAX extended header"):
            verifier.validate_archive(path, "tar.gz", "cortiq")

    def test_accepts_one_regular_windows_style_zip_member(self) -> None:
        path = self.zip_path()
        # The current Windows Cortiq release is made by DOS (create_system=0)
        # and has no POSIX mode bits, so this tests the supported ZIP shape.
        member = zipfile.ZipInfo("cortiq.exe")
        member.create_system = 0
        member.external_attr = 0
        self._write_zip(path, [(member, b"cortiq runtime")])

        verifier.validate_archive(path, "zip", "cortiq.exe")

    def test_extracts_one_regular_zip_member_to_private_output(self) -> None:
        path = self.zip_path()
        payload = b"cortiq runtime"
        member = zipfile.ZipInfo("cortiq.exe")
        member.create_system = 0
        member.external_attr = 0
        self._write_zip(path, [(member, payload)])
        output = self.output_dir()

        destination = verifier.extract_archive(path, "zip", "cortiq.exe", output)

        self.assertEqual(destination, output / "cortiq.exe")
        self.assert_private_extraction(destination, payload)

    def test_rejects_zip_symbolic_link(self) -> None:
        path = self.zip_path()
        member = zipfile.ZipInfo("cortiq.exe")
        member.create_system = 3
        member.external_attr = (stat.S_IFLNK | 0o777) << 16
        self._write_zip(path, [(member, b"elsewhere")])

        with self.assertRaisesRegex(verifier.ValidationError, "symbolic link"):
            verifier.validate_archive(path, "zip", "cortiq.exe")

    def test_rejects_zip_special_file(self) -> None:
        path = self.zip_path()
        member = zipfile.ZipInfo("cortiq.exe")
        member.create_system = 3
        member.external_attr = (stat.S_IFIFO | 0o644) << 16
        self._write_zip(path, [(member, b"payload")])

        with self.assertRaisesRegex(verifier.ValidationError, "special file"):
            verifier.validate_archive(path, "zip", "cortiq.exe")

    def test_rejects_zip_directory_attribute(self) -> None:
        path = self.zip_path()
        member = zipfile.ZipInfo("cortiq.exe")
        member.create_system = 0
        member.external_attr = 0x10  # MS-DOS directory attribute.
        self._write_zip(path, [(member, b"payload")])

        with self.assertRaisesRegex(verifier.ValidationError, "directory"):
            verifier.validate_archive(path, "zip", "cortiq.exe")

    def test_rejects_zip_multiple_members(self) -> None:
        path = self.zip_path()
        self._write_zip(
            path,
            [
                (self._zip_regular("cortiq.exe", b"runtime"), b"runtime"),
                (self._zip_regular("extra", b"unexpected"), b"unexpected"),
            ],
        )

        with self.assertRaisesRegex(verifier.ValidationError, "exactly one entry"):
            verifier.validate_archive(path, "zip", "cortiq.exe")

    def test_rejects_zip_entry_count_before_constructing_zipfile(self) -> None:
        path = self.zip_path("central-count.zip")
        self._write_zip(
            path, [(self._zip_regular("cortiq.exe", b"runtime"), b"runtime")]
        )
        data = bytearray(path.read_bytes())
        eocd = self._zip_eocd_offset(data)
        struct.pack_into("<H", data, eocd + 8, 2)
        struct.pack_into("<H", data, eocd + 10, 2)
        path.write_bytes(data)

        with mock.patch.object(verifier.zipfile, "ZipFile") as open_zip:
            with self.assertRaisesRegex(verifier.ValidationError, "exactly one entry"):
                verifier.validate_archive(path, "zip", "cortiq.exe")
        open_zip.assert_not_called()

    def test_rejects_oversized_zip_central_directory_before_constructing_zipfile(self) -> None:
        path = self.zip_path("central-size.zip")
        self._write_zip(
            path, [(self._zip_regular("cortiq.exe", b"runtime"), b"runtime")]
        )
        data = bytearray(path.read_bytes())
        eocd = self._zip_eocd_offset(data)
        struct.pack_into(
            "<I",
            data,
            eocd + 12,
            verifier._ZIP_MAX_CENTRAL_DIRECTORY_BYTES + 1,
        )
        path.write_bytes(data)

        with mock.patch.object(verifier.zipfile, "ZipFile") as open_zip:
            with self.assertRaisesRegex(verifier.ValidationError, "central directory exceeds"):
                verifier.validate_archive(path, "zip", "cortiq.exe")
        open_zip.assert_not_called()

    def test_rejects_zip_encryption_flag(self) -> None:
        path = self.zip_path()
        self._write_zip(
            path,
            [(self._zip_regular("cortiq.exe", b"runtime"), b"runtime")],
        )
        self._set_zip_encryption_flag(path)

        with self.assertRaisesRegex(verifier.ValidationError, "encrypted"):
            verifier.validate_archive(path, "zip", "cortiq.exe")

    def test_rejects_empty_zip_member(self) -> None:
        path = self.zip_path()
        self._write_zip(path, [(self._zip_regular("cortiq.exe", b""), b"")])

        with self.assertRaisesRegex(verifier.ValidationError, "is empty"):
            verifier.validate_archive(path, "zip", "cortiq.exe")

    def test_extraction_refuses_existing_regular_destination(self) -> None:
        path = self.tar_path()
        payload = b"cortiq runtime"
        self._write_tar(path, [(self._tar_regular("cortiq", payload), payload)])
        output = self.output_dir()
        destination = output / "cortiq"
        sentinel = b"do not overwrite"
        destination.write_bytes(sentinel)

        with self.assertRaisesRegex(
            verifier.ValidationError, "refusing to overwrite existing regular output"
        ):
            verifier.extract_archive(path, "tar.gz", "cortiq", output)

        self.assertEqual(destination.read_bytes(), sentinel)

    def test_extraction_refuses_existing_nonregular_destination(self) -> None:
        path = self.zip_path()
        payload = b"cortiq runtime"
        self._write_zip(path, [(self._zip_regular("cortiq.exe", payload), payload)])
        output = self.output_dir()
        destination = output / "cortiq.exe"
        destination.mkdir()

        with self.assertRaisesRegex(
            verifier.ValidationError, "refusing to replace existing non-regular output"
        ):
            verifier.extract_archive(path, "zip", "cortiq.exe", output)

        self.assertTrue(destination.is_dir())

    def test_extraction_refuses_symbolic_link_output_directory(self) -> None:
        path = self.tar_path()
        payload = b"cortiq runtime"
        self._write_tar(path, [(self._tar_regular("cortiq", payload), payload)])
        actual_output = self.output_dir("actual-output")
        output_link = self.root / "output-link"
        try:
            output_link.symlink_to(actual_output, target_is_directory=True)
        except (NotImplementedError, OSError) as exc:
            self.skipTest(f"symbolic links are unavailable: {exc}")

        with self.assertRaisesRegex(verifier.ValidationError, "must not be a symbolic link"):
            verifier.extract_archive(path, "tar.gz", "cortiq", output_link)

        self.assertFalse((actual_output / "cortiq").exists())

    def test_stream_rejects_actual_size_mismatch_and_removes_partial_output(self) -> None:
        output = self.output_dir()

        with self.assertRaisesRegex(verifier.ValidationError, "exceeded its declared size"):
            verifier._stream_member_to_destination(
                io.BytesIO(b"12345"),
                output,
                "cortiq",
                declared_size=3,
                max_uncompressed_bytes=3,
                directory_fd=None,
            )

        self.assertFalse((output / "cortiq").exists())

    def test_main_without_extract_to_remains_metadata_only(self) -> None:
        path = self.tar_path()
        payload = b"cortiq runtime"
        self._write_tar(path, [(self._tar_regular("cortiq", payload), payload)])

        result = verifier.main(
            ["--archive", str(path), "--format", "tar.gz", "--entry", "cortiq"]
        )

        self.assertEqual(result, 0)
        self.assertFalse((self.root / "cortiq").exists())

    @staticmethod
    def _set_zip_encryption_flag(path: Path) -> None:
        """Set the encrypted bit in local and central ZIP headers for a test.

        `zipfile` intentionally cannot write encrypted archives.  The verifier
        reads metadata only, so patching the two standard headers is enough to
        prove that it rejects the flag without needing encryption tooling.
        """

        data = bytearray(path.read_bytes())
        local = data.index(b"PK\x03\x04")
        central = data.index(b"PK\x01\x02")
        for offset in (local + 6, central + 8):
            flags = struct.unpack_from("<H", data, offset)[0]
            struct.pack_into("<H", data, offset, flags | 0x1)
        path.write_bytes(data)

    @staticmethod
    def _zip_eocd_offset(data: bytes | bytearray) -> int:
        offset = data.rfind(b"PK\x05\x06")
        if offset < 0:
            raise AssertionError("test ZIP has no EOCD record")
        return offset


if __name__ == "__main__":
    unittest.main()
