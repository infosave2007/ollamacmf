#!/usr/bin/env python3
"""Validate, and optionally safely extract, a locked Cortiq runtime archive.

Without ``--extract-to`` this is metadata-only: it never creates an output
file or executes a payload. It streams only the declared, bounded sole member
to prove the archive reaches its end markers. With ``--extract-to`` it first
performs that validation, then reopens and validates the sole member before
streaming it into a newly created private output file. It never uses
``extractall`` or trusts archive paths for a destination.

The helper uses only the Python standard library so release-intake and PR
validation workflows can invoke the same policy.
"""

from __future__ import annotations

import argparse
from contextlib import contextmanager
import gzip
import os
from pathlib import Path
import stat
import struct
import sys
from typing import BinaryIO, Callable, Iterator, Sequence
import zipfile


# The currently reviewed Cortiq payloads are below 51 MiB uncompressed.  The
# default leaves room for a normal growth in a native runtime while preventing
# an accidental or hostile multi-gigabyte archive from becoming a packaging
# input.  A larger payload needs an explicit, reviewed workflow invocation.
DEFAULT_MAX_UNCOMPRESSED_BYTES = 512 * 1024 * 1024
DEFAULT_MAX_ARCHIVE_BYTES = 512 * 1024 * 1024
_COPY_BUFFER_BYTES = 1024 * 1024


class ValidationError(ValueError):
    """An archive or requested extraction violates the runtime payload contract."""


def _require_positive_bound(value: int, name: str) -> int:
    if value <= 0:
        raise ValidationError(f"{name} must be greater than zero")
    return value


def _validate_expected_entry(entry: str) -> None:
    if not entry or entry in {".", ".."}:
        raise ValidationError("expected entry must be a non-empty file name")
    if "\x00" in entry or "/" in entry or "\\" in entry:
        raise ValidationError(
            "expected entry must be a single file name, not a path"
        )


def _validate_archive_file(path: Path, max_archive_bytes: int) -> None:
    try:
        info = path.lstat()
    except OSError as exc:
        raise ValidationError(f"cannot stat archive {path}: {exc}") from exc

    if not stat.S_ISREG(info.st_mode):
        raise ValidationError(f"archive {path} is not a regular file")
    if info.st_size <= 0:
        raise ValidationError(f"archive {path} is empty")
    if info.st_size > max_archive_bytes:
        raise ValidationError(
            f"archive {path} is {info.st_size} bytes, above the "
            f"{max_archive_bytes}-byte archive limit"
        )


def _validate_member_size(name: str, size: int, max_uncompressed_bytes: int) -> None:
    if size <= 0:
        raise ValidationError(f"archive entry {name!r} is empty")
    if size > max_uncompressed_bytes:
        raise ValidationError(
            f"archive entry {name!r} is {size} bytes, above the "
            f"{max_uncompressed_bytes}-byte uncompressed limit"
        )


_TAR_BLOCK_BYTES = 512
_TAR_TYPE_OFFSET = 156
_TAR_LINKNAME_OFFSET = 157
_TAR_LINKNAME_END = 257
_TAR_PREFIX_OFFSET = 345
_TAR_PREFIX_END = 500
_TAR_CHECKSUM_OFFSET = 148
_TAR_CHECKSUM_END = 156
_TAR_SIZE_OFFSET = 124
_TAR_SIZE_END = 136
_TAR_MODE_OFFSET = 100
_TAR_MODE_END = 108


def _read_exact(source: BinaryIO, count: int, context: str) -> bytes:
    """Read exactly ``count`` bytes without trusting a stream's short reads."""
    chunks: list[bytes] = []
    remaining = count
    while remaining:
        chunk = source.read(remaining)
        if not chunk:
            raise ValidationError(f"tar.gz archive is truncated while reading {context}")
        if not isinstance(chunk, bytes):
            raise ValidationError(f"tar.gz archive produced non-byte data while reading {context}")
        chunks.append(chunk)
        remaining -= len(chunk)
    return b"".join(chunks)


def _skip_exact(source: BinaryIO, count: int, context: str) -> None:
    """Discard a bounded span without allocating the member payload."""
    remaining = count
    while remaining:
        chunk = source.read(min(_COPY_BUFFER_BYTES, remaining))
        if not chunk:
            raise ValidationError(f"tar.gz archive is truncated while reading {context}")
        if not isinstance(chunk, bytes):
            raise ValidationError(f"tar.gz archive produced non-byte data while reading {context}")
        remaining -= len(chunk)


def _read_tar_block(source: BinaryIO, context: str) -> bytes:
    return _read_exact(source, _TAR_BLOCK_BYTES, context)


def _parse_tar_octal(field: bytes, label: str) -> int:
    # POSIX tar fields use ASCII octal padded with NUL and/or space. Base-256
    # values are deliberately unsupported: this package accepts a narrow,
    # reviewable one-file runtime contract rather than every historical tar
    # extension.
    if field and field[0] & 0x80:
        raise ValidationError(f"tar.gz archive {label} uses unsupported base-256 encoding")
    value = field.strip(b" \x00")
    if not value:
        return 0
    if any(byte < ord("0") or byte > ord("7") for byte in value):
        raise ValidationError(f"tar.gz archive has malformed {label}")
    return int(value, 8)


def _decode_tar_string(field: bytes, label: str) -> str:
    raw = field.split(b"\x00", 1)[0]
    try:
        return raw.decode("utf-8")
    except UnicodeDecodeError as exc:
        raise ValidationError(f"tar.gz archive {label} is not valid UTF-8") from exc


def _validate_tar_checksum(header: bytes) -> None:
    stored = _parse_tar_octal(
        header[_TAR_CHECKSUM_OFFSET:_TAR_CHECKSUM_END], "header checksum"
    )
    with_spaces = (
        header[:_TAR_CHECKSUM_OFFSET]
        + b" " * (_TAR_CHECKSUM_END - _TAR_CHECKSUM_OFFSET)
        + header[_TAR_CHECKSUM_END:]
    )
    unsigned = sum(with_spaces)
    signed = sum(byte if byte < 128 else byte - 256 for byte in with_spaces)
    if stored not in {unsigned, signed}:
        raise ValidationError("tar.gz archive header checksum does not match")


def _tar_kind(typeflag: bytes) -> str | None:
    if typeflag in {b"\x00", b"0"}:
        return None
    kinds = {
        b"1": "a hard link",
        b"2": "a symbolic link",
        b"3": "a character device",
        b"4": "a block device",
        b"5": "a directory",
        b"6": "a FIFO",
        b"7": "a reserved file type",
        b"g": "a PAX global extended header",
        b"x": "a PAX extended header",
        b"K": "a GNU long-link extension",
        b"L": "a GNU long-name extension",
        b"S": "a GNU sparse file",
    }
    return kinds.get(typeflag, "an unsupported non-regular file")


def _parse_tar_member_header(
    header: bytes, expected_entry: str, max_uncompressed_bytes: int
) -> int:
    """Validate the first tar header before consuming its declared payload."""
    if header == b"\x00" * _TAR_BLOCK_BYTES:
        raise ValidationError("tar.gz archive must contain exactly one entry; found 0")

    _validate_tar_checksum(header)
    name = _decode_tar_string(header[:100], "entry name")
    prefix = _decode_tar_string(
        header[_TAR_PREFIX_OFFSET:_TAR_PREFIX_END], "entry prefix"
    )
    if prefix:
        name = f"{prefix}/{name}"

    kind = _tar_kind(header[_TAR_TYPE_OFFSET : _TAR_TYPE_OFFSET + 1])
    if kind is not None:
        # This check intentionally comes before reading/skipping any declared
        # extension payload. In particular PAX/GNU extension records cannot
        # force gzip/tar to process an unbounded value before policy rejects it.
        raise ValidationError(f"tar.gz archive entry {name!r} is {kind}")
    if name != expected_entry:
        raise ValidationError(
            f"tar.gz archive entry must be {expected_entry!r}, got {name!r}"
        )

    linkname = _decode_tar_string(
        header[_TAR_LINKNAME_OFFSET:_TAR_LINKNAME_END], "link name"
    )
    if linkname:
        raise ValidationError(
            f"tar.gz archive entry {name!r} unexpectedly has link metadata"
        )

    mode = _parse_tar_octal(header[_TAR_MODE_OFFSET:_TAR_MODE_END], "entry mode")
    if mode & (stat.S_ISUID | stat.S_ISGID):
        raise ValidationError(
            f"tar.gz archive entry {name!r} has setuid or setgid bits"
        )
    if not (mode & stat.S_IXUSR):
        raise ValidationError(f"tar.gz archive entry {name!r} is not owner-executable")

    size = _parse_tar_octal(header[_TAR_SIZE_OFFSET:_TAR_SIZE_END], "entry size")
    _validate_member_size(name, size, max_uncompressed_bytes)
    return size


def _tar_padding(size: int) -> int:
    return (-size) % _TAR_BLOCK_BYTES


def _finish_tar_archive(source: BinaryIO, member_size: int) -> None:
    _skip_exact(source, _tar_padding(member_size), "tar member padding")
    first = _read_tar_block(source, "first tar end marker")
    if first != b"\x00" * _TAR_BLOCK_BYTES:
        # Do not inspect a second header's payload: one payload is the entire
        # contract, and returning here keeps processing bounded.
        raise ValidationError("tar.gz archive must contain exactly one entry; found at least 2")
    second = _read_tar_block(source, "second tar end marker")
    if second != b"\x00" * _TAR_BLOCK_BYTES:
        raise ValidationError("tar.gz archive is missing the second end marker")


def _validate_tar_gz(
    path: Path, expected_entry: str, max_uncompressed_bytes: int
) -> None:
    try:
        with gzip.open(path, mode="rb") as archive:
            member_size = _parse_tar_member_header(
                _read_tar_block(archive, "tar header"),
                expected_entry,
                max_uncompressed_bytes,
            )
            # A declared regular payload has already passed the strict maximum;
            # discarding it is therefore bounded before we inspect the trailer.
            _skip_exact(archive, member_size, "tar member payload")
            _finish_tar_archive(archive, member_size)
    except ValidationError:
        raise
    except (EOFError, OSError, gzip.BadGzipFile) as exc:
        raise ValidationError(f"cannot read tar.gz archive {path}: {exc}") from exc


class _TarMemberReader:
    """A bounded view over exactly one validated tar member payload."""

    def __init__(self, source: BinaryIO, size: int) -> None:
        self._source = source
        self.remaining = size

    def read(self, size: int = -1) -> bytes:
        if self.remaining <= 0:
            return b""
        if size is None or size < 0:
            size = _COPY_BUFFER_BYTES
        want = min(size, self.remaining)
        chunk = self._source.read(want)
        if not chunk:
            return b""
        if not isinstance(chunk, bytes):
            raise ValidationError("tar.gz archive produced non-byte member data")
        self.remaining -= len(chunk)
        return chunk


_ZIP_EOCD_SIGNATURE = b"PK\x05\x06"
_ZIP_EOCD_BYTES = 22
_ZIP_MAX_COMMENT_BYTES = 0xFFFF
_ZIP_MAX_CENTRAL_DIRECTORY_BYTES = 1024 * 1024


def _preflight_zip(path: Path) -> None:
    """Bound the ZIP central directory before ``ZipFile`` materializes it.

    ``ZipFile.infolist`` constructs one ``ZipInfo`` per central-directory
    record, so inspect the EOCD directly first. This narrow payload contract
    allows one non-Zip64, single-disk member and a small central directory.
    """
    try:
        file_size = path.stat().st_size
        if file_size < _ZIP_EOCD_BYTES:
            raise ValidationError("zip archive is too short for an end-of-central-directory record")
        tail_size = min(file_size, _ZIP_EOCD_BYTES + _ZIP_MAX_COMMENT_BYTES)
        with path.open("rb") as source:
            source.seek(file_size - tail_size)
            tail = source.read(tail_size)
    except ValidationError:
        raise
    except OSError as exc:
        raise ValidationError(f"cannot read zip archive {path}: {exc}") from exc

    # A ZIP comment can contain the EOCD signature, so accept only a record
    # whose declared comment ends exactly at EOF and search backward.
    index = tail.rfind(_ZIP_EOCD_SIGNATURE)
    while index >= 0:
        if index + _ZIP_EOCD_BYTES <= len(tail):
            (
                _,
                disk_number,
                central_directory_disk,
                entries_on_disk,
                entries_total,
                central_directory_size,
                central_directory_offset,
                comment_size,
            ) = struct.unpack_from("<4s4H2LH", tail, index)
            if index + _ZIP_EOCD_BYTES + comment_size == len(tail):
                break
        index = tail.rfind(_ZIP_EOCD_SIGNATURE, 0, index)
    else:
        raise ValidationError("zip archive has no valid end-of-central-directory record")

    if disk_number != 0 or central_directory_disk != 0:
        raise ValidationError("zip archive must be single-disk")
    # Sentinel values require Zip64 extension records. Reject them rather than
    # asking ZipFile to parse unbounded metadata for this one-file contract.
    if (
        entries_on_disk == 0xFFFF
        or entries_total == 0xFFFF
        or central_directory_size == 0xFFFFFFFF
        or central_directory_offset == 0xFFFFFFFF
    ):
        raise ValidationError("zip archive uses unsupported Zip64 metadata")
    if entries_on_disk != 1 or entries_total != 1:
        raise ValidationError(
            f"zip archive must contain exactly one entry; found {entries_total}"
        )
    if central_directory_size > _ZIP_MAX_CENTRAL_DIRECTORY_BYTES:
        raise ValidationError(
            "zip archive central directory exceeds the "
            f"{_ZIP_MAX_CENTRAL_DIRECTORY_BYTES}-byte limit"
        )

    eocd_offset = file_size - tail_size + index
    central_directory_end = central_directory_offset + central_directory_size
    if central_directory_end > eocd_offset:
        raise ValidationError("zip archive central directory lies outside the archive")


def _zip_is_posix(info: zipfile.ZipInfo) -> bool:
    # 3 is Unix.  Several tools use 19 (macOS) with the same POSIX external
    # attribute layout.  Values are defined by the ZIP "version made by" host.
    return info.create_system in {3, 19}


def _validate_zip_member(
    member: zipfile.ZipInfo, expected_entry: str, max_uncompressed_bytes: int
) -> None:
    """Check a sole ZIP member without opening or extracting its payload."""
    if member.filename != expected_entry:
        raise ValidationError(
            f"zip archive entry must be {expected_entry!r}, got {member.filename!r}"
        )
    if member.is_dir() or (member.external_attr & 0x10):
        raise ValidationError(f"zip archive entry {member.filename!r} is a directory")
    if member.flag_bits & 0x1:
        raise ValidationError(f"zip archive entry {member.filename!r} is encrypted")

    posix_mode = (member.external_attr >> 16) & 0xFFFF
    if _zip_is_posix(member):
        file_type = stat.S_IFMT(posix_mode)
        if not stat.S_ISREG(posix_mode):
            if stat.S_ISLNK(posix_mode):
                kind = "a symbolic link"
            elif file_type:
                kind = "a special file"
            else:
                kind = "missing a POSIX regular-file type"
            raise ValidationError(f"zip archive entry {member.filename!r} is {kind}")
        if posix_mode & (stat.S_ISUID | stat.S_ISGID):
            raise ValidationError(
                f"zip archive entry {member.filename!r} has setuid or setgid bits"
            )
        if not (posix_mode & stat.S_IXUSR):
            raise ValidationError(
                f"zip archive entry {member.filename!r} is not owner-executable"
            )

    _validate_member_size(member.filename, member.file_size, max_uncompressed_bytes)


def _single_zip_member(
    archive: zipfile.ZipFile, expected_entry: str, max_uncompressed_bytes: int
) -> zipfile.ZipInfo:
    members = archive.infolist()
    if len(members) != 1:
        raise ValidationError(
            f"zip archive must contain exactly one entry; found {len(members)}"
        )
    member = members[0]
    _validate_zip_member(member, expected_entry, max_uncompressed_bytes)
    return member


def _validate_zip(path: Path, expected_entry: str, max_uncompressed_bytes: int) -> None:
    try:
        # Do this before ZipFile reads the untrusted central directory into a
        # ZipInfo list.
        _preflight_zip(path)
        with zipfile.ZipFile(path) as archive:
            _single_zip_member(archive, expected_entry, max_uncompressed_bytes)
    except ValidationError:
        raise
    except (OSError, zipfile.BadZipFile, zipfile.LargeZipFile) as exc:
        raise ValidationError(f"cannot read zip archive {path}: {exc}") from exc


def validate_archive(
    archive: Path,
    archive_format: str,
    expected_entry: str,
    *,
    max_uncompressed_bytes: int = DEFAULT_MAX_UNCOMPRESSED_BYTES,
    max_archive_bytes: int = DEFAULT_MAX_ARCHIVE_BYTES,
) -> None:
    """Validate a runtime archive without extracting its contents.

    Raises:
        ValidationError: if the archive does not meet the one-executable
            runtime-payload contract.
    """

    _validate_expected_entry(expected_entry)
    _require_positive_bound(max_uncompressed_bytes, "maximum uncompressed size")
    _require_positive_bound(max_archive_bytes, "maximum archive size")
    _validate_archive_file(archive, max_archive_bytes)

    if archive_format == "tar.gz":
        _validate_tar_gz(archive, expected_entry, max_uncompressed_bytes)
    elif archive_format == "zip":
        _validate_zip(archive, expected_entry, max_uncompressed_bytes)
    else:
        raise ValidationError(
            f"unsupported archive format {archive_format!r}; expected 'tar.gz' or 'zip'"
        )


def _validate_extract_directory(path: Path) -> None:
    """Require an existing real directory before an output is created there."""
    try:
        info = path.lstat()
    except OSError as exc:
        raise ValidationError(f"cannot stat extraction directory {path}: {exc}") from exc

    if stat.S_ISLNK(info.st_mode):
        raise ValidationError(f"extraction directory {path} must not be a symbolic link")
    if not stat.S_ISDIR(info.st_mode):
        raise ValidationError(f"extraction directory {path} is not a directory")


def _can_anchor_output_directory() -> bool:
    """Whether this platform can safely use an opened directory as a base."""
    return (
        hasattr(os, "O_DIRECTORY")
        and hasattr(os, "O_NOFOLLOW")
        and os.open in os.supports_dir_fd
        and os.lstat in os.supports_dir_fd
        and os.unlink in os.supports_dir_fd
    )


@contextmanager
def _opened_output_directory(path: Path) -> Iterator[int | None]:
    """Yield a directory FD on POSIX, or a checked path fallback elsewhere."""
    _validate_extract_directory(path)
    if not _can_anchor_output_directory():
        # Windows does not expose the POSIX directory-FD APIs used below.  The
        # destination still uses O_EXCL and is checked with lstat before open.
        yield None
        return

    flags = os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW
    flags |= getattr(os, "O_CLOEXEC", 0)
    try:
        directory_fd = os.open(path, flags)
    except OSError as exc:
        raise ValidationError(f"cannot safely open extraction directory {path}: {exc}") from exc

    try:
        if not stat.S_ISDIR(os.fstat(directory_fd).st_mode):
            raise ValidationError(f"extraction directory {path} is not a directory")
        yield directory_fd
    finally:
        os.close(directory_fd)


def _lstat_destination(
    output_directory: Path, expected_entry: str, directory_fd: int | None
) -> os.stat_result | None:
    try:
        if directory_fd is None:
            return (output_directory / expected_entry).lstat()
        return os.lstat(expected_entry, dir_fd=directory_fd)
    except FileNotFoundError:
        return None
    except OSError as exc:
        destination = output_directory / expected_entry
        raise ValidationError(f"cannot stat output {destination}: {exc}") from exc


def _refuse_existing_destination(
    output_directory: Path, expected_entry: str, directory_fd: int | None
) -> None:
    destination = output_directory / expected_entry
    info = _lstat_destination(output_directory, expected_entry, directory_fd)
    if info is None:
        return
    if stat.S_ISREG(info.st_mode):
        raise ValidationError(f"refusing to overwrite existing regular output {destination}")
    raise ValidationError(f"refusing to replace existing non-regular output {destination}")


def _open_new_destination(
    output_directory: Path, expected_entry: str, directory_fd: int | None
) -> int:
    """Atomically create one output entry without following its final component."""
    destination = output_directory / expected_entry
    _refuse_existing_destination(output_directory, expected_entry, directory_fd)

    flags = os.O_WRONLY | os.O_CREAT | os.O_EXCL
    flags |= getattr(os, "O_NOFOLLOW", 0)
    flags |= getattr(os, "O_CLOEXEC", 0)
    flags |= getattr(os, "O_BINARY", 0)
    try:
        if directory_fd is None:
            return os.open(destination, flags, 0o700)
        return os.open(expected_entry, flags, 0o700, dir_fd=directory_fd)
    except FileExistsError as exc:
        # O_EXCL closes the race between the lstat check and creation, including
        # when a competing process creates a symlink, directory, or special file.
        raise ValidationError(f"refusing to overwrite existing output {destination}") from exc
    except OSError as exc:
        raise ValidationError(f"cannot create output {destination}: {exc}") from exc


def _unlink_if_same_destination(
    output_directory: Path,
    expected_entry: str,
    directory_fd: int | None,
    created_info: os.stat_result | None,
) -> None:
    """Remove a failed partial only if its directory entry is still our file."""
    if created_info is None:
        return
    current = _lstat_destination(output_directory, expected_entry, directory_fd)
    if current is None:
        return
    if not stat.S_ISREG(current.st_mode):
        return
    if (current.st_dev, current.st_ino) != (created_info.st_dev, created_info.st_ino):
        return

    try:
        if directory_fd is None:
            os.unlink(output_directory / expected_entry)
        else:
            os.unlink(expected_entry, dir_fd=directory_fd)
    except FileNotFoundError:
        pass
    except OSError:
        # Preserve the original archive/extraction error.  A caller can inspect
        # the remaining private output if an unusual filesystem refused unlink.
        pass


def _stream_member_to_destination(
    source: BinaryIO,
    output_directory: Path,
    expected_entry: str,
    declared_size: int,
    max_uncompressed_bytes: int,
    directory_fd: int | None,
    after_copy: Callable[[], None] | None = None,
) -> Path:
    """Copy an already validated member to one atomically-created regular file."""
    _validate_member_size(expected_entry, declared_size, max_uncompressed_bytes)
    destination = output_directory / expected_entry
    output_fd: int | None = None
    created_info: os.stat_result | None = None
    completed = False

    try:
        output_fd = _open_new_destination(
            output_directory, expected_entry, directory_fd
        )
        created_info = os.fstat(output_fd)
        if not stat.S_ISREG(created_info.st_mode):
            raise ValidationError(f"new output {destination} is not a regular file")
        if created_info.st_nlink != 1:
            raise ValidationError(f"new output {destination} has unexpected link count")
        if os.name != "nt":
            # The creation mode is subject to umask; force the promised private
            # executable mode on Unix only after verifying the opened inode.
            os.fchmod(output_fd, 0o700)

        output = os.fdopen(output_fd, "wb", closefd=True)
        output_fd = None
        with output:
            actual_size = 0
            while True:
                chunk = source.read(_COPY_BUFFER_BYTES)
                if not chunk:
                    break
                if not isinstance(chunk, bytes):
                    raise ValidationError(
                        f"archive entry {expected_entry!r} produced non-byte data"
                    )
                actual_size += len(chunk)
                if actual_size > declared_size:
                    raise ValidationError(
                        f"archive entry {expected_entry!r} exceeded its declared size "
                        f"of {declared_size} bytes while extracting"
                    )
                if actual_size > max_uncompressed_bytes:
                    raise ValidationError(
                        f"archive entry {expected_entry!r} exceeded the "
                        f"{max_uncompressed_bytes}-byte uncompressed limit while extracting"
                    )
                output.write(chunk)

            if actual_size != declared_size:
                raise ValidationError(
                    f"archive entry {expected_entry!r} declared {declared_size} bytes "
                    f"but yielded {actual_size} bytes while extracting"
                )
            output.flush()
            os.fsync(output.fileno())
            # A raw tar extraction validates the padding/end markers only
            # after its bounded member reader reaches EOF. Keep the private
            # output provisional until that final archive check succeeds.
            if after_copy is not None:
                after_copy()
        completed = True
    finally:
        if output_fd is not None:
            os.close(output_fd)
        if not completed:
            _unlink_if_same_destination(
                output_directory, expected_entry, directory_fd, created_info
            )

    return destination


def _extract_tar_gz(
    path: Path,
    expected_entry: str,
    max_uncompressed_bytes: int,
    output_directory: Path,
    directory_fd: int | None,
) -> Path:
    try:
        with gzip.open(path, mode="rb") as archive:
            member_size = _parse_tar_member_header(
                _read_tar_block(archive, "tar header"),
                expected_entry,
                max_uncompressed_bytes,
            )
            member = _TarMemberReader(archive, member_size)

            def finish() -> None:
                if member.remaining:
                    raise ValidationError("tar.gz archive member ended before its declared size")
                _finish_tar_archive(archive, member_size)

            return _stream_member_to_destination(
                member,
                output_directory,
                expected_entry,
                member_size,
                max_uncompressed_bytes,
                directory_fd,
                finish,
            )
    except ValidationError:
        raise
    except (EOFError, OSError, gzip.BadGzipFile) as exc:
        raise ValidationError(f"cannot extract tar.gz archive {path}: {exc}") from exc


def _extract_zip(
    path: Path,
    expected_entry: str,
    max_uncompressed_bytes: int,
    output_directory: Path,
    directory_fd: int | None,
) -> Path:
    try:
        _preflight_zip(path)
        with zipfile.ZipFile(path) as archive:
            member = _single_zip_member(archive, expected_entry, max_uncompressed_bytes)
            with archive.open(member, mode="r") as source:
                return _stream_member_to_destination(
                    source,
                    output_directory,
                    expected_entry,
                    member.file_size,
                    max_uncompressed_bytes,
                    directory_fd,
                )
    except ValidationError:
        raise
    except (OSError, zipfile.BadZipFile, zipfile.LargeZipFile) as exc:
        raise ValidationError(f"cannot extract zip archive {path}: {exc}") from exc


def extract_archive(
    archive: Path,
    archive_format: str,
    expected_entry: str,
    extract_to: Path,
    *,
    max_uncompressed_bytes: int = DEFAULT_MAX_UNCOMPRESSED_BYTES,
    max_archive_bytes: int = DEFAULT_MAX_ARCHIVE_BYTES,
) -> Path:
    """Validate and then stream the sole verified member into ``extract_to``.

    ``extract_to`` must be an existing real directory.  The returned path is
    ``extract_to / expected_entry`` and must not already exist.  The source is
    re-opened and its metadata is checked again immediately before streaming so
    a replacement after the initial metadata-only validation cannot bypass the
    member policy.
    """
    validate_archive(
        archive,
        archive_format,
        expected_entry,
        max_uncompressed_bytes=max_uncompressed_bytes,
        max_archive_bytes=max_archive_bytes,
    )

    with _opened_output_directory(extract_to) as directory_fd:
        # The initial call above is deliberately visible and reusable as the
        # metadata-only mode.  Check the source again before a second open,
        # then validate the sole member on that opened archive below.
        _validate_archive_file(archive, max_archive_bytes)
        if archive_format == "tar.gz":
            return _extract_tar_gz(
                archive,
                expected_entry,
                max_uncompressed_bytes,
                extract_to,
                directory_fd,
            )
        if archive_format == "zip":
            return _extract_zip(
                archive,
                expected_entry,
                max_uncompressed_bytes,
                extract_to,
                directory_fd,
            )

    # validate_archive has already rejected this; retain a defensive error if
    # a future format branch changes that invariant.
    raise ValidationError(
        f"unsupported archive format {archive_format!r}; expected 'tar.gz' or 'zip'"
    )


def _positive_int(value: str) -> int:
    try:
        parsed = int(value, 10)
    except ValueError as exc:
        raise argparse.ArgumentTypeError("must be a base-10 integer") from exc
    if parsed <= 0:
        raise argparse.ArgumentTypeError("must be greater than zero")
    return parsed


def parse_args(argv: Sequence[str] | None = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Validate metadata for a single-file CMF runtime archive."
    )
    parser.add_argument("--archive", required=True, type=Path)
    parser.add_argument("--format", required=True, choices=("tar.gz", "zip"))
    parser.add_argument("--entry", required=True, help="expected sole executable name")
    parser.add_argument(
        "--extract-to",
        type=Path,
        metavar="DIRECTORY",
        help=(
            "after validation, create the verified executable in this existing "
            "non-symlink directory; omitted means metadata-only validation"
        ),
    )
    parser.add_argument(
        "--max-uncompressed-bytes",
        type=_positive_int,
        default=DEFAULT_MAX_UNCOMPRESSED_BYTES,
        help=(
            "maximum uncompressed executable bytes "
            f"(default: {DEFAULT_MAX_UNCOMPRESSED_BYTES})"
        ),
    )
    parser.add_argument(
        "--max-archive-bytes",
        type=_positive_int,
        default=DEFAULT_MAX_ARCHIVE_BYTES,
        help=(
            "maximum compressed archive bytes "
            f"(default: {DEFAULT_MAX_ARCHIVE_BYTES})"
        ),
    )
    return parser.parse_args(argv)


def main(argv: Sequence[str] | None = None) -> int:
    args = parse_args(argv)
    try:
        if args.extract_to is None:
            validate_archive(
                args.archive,
                args.format,
                args.entry,
                max_uncompressed_bytes=args.max_uncompressed_bytes,
                max_archive_bytes=args.max_archive_bytes,
            )
        else:
            destination = extract_archive(
                args.archive,
                args.format,
                args.entry,
                args.extract_to,
                max_uncompressed_bytes=args.max_uncompressed_bytes,
                max_archive_bytes=args.max_archive_bytes,
            )
    except ValidationError as exc:
        print(
            f"error: CMF runtime archive validation/extraction failed: {exc}",
            file=sys.stderr,
        )
        return 1

    if args.extract_to is None:
        print(
            f"validated {args.format} CMF runtime archive {args.archive} "
            f"with sole executable {args.entry!r}"
        )
    else:
        print(
            f"validated and extracted {args.format} CMF runtime archive "
            f"{args.archive} to {destination}"
        )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
