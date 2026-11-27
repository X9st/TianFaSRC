#!/usr/bin/env python3
"""Pack and extract TianFa data trees without following links or tar paths."""

import os
import stat
import sys
import tarfile
from pathlib import Path, PurePosixPath


def safe_parts(name: str) -> tuple[str, ...]:
    if not name or any(part in ("", ".", "..") for part in name.split("/")):
        raise ValueError(f"unsafe archive path: {name!r}")
    path = PurePosixPath(name)
    parts = path.parts
    if path.is_absolute() or not parts or any(part in ("", ".", "..") for part in parts):
        raise ValueError(f"unsafe archive path: {name!r}")
    if any("\\" in part or any(ord(char) < 32 for char in part) for part in parts):
        raise ValueError(f"unsafe archive path: {name!r}")
    return parts


def pack(source: Path, archive: Path) -> None:
    if source.is_symlink():
        raise ValueError("source must not be a symbolic link")
    source = source.resolve(strict=True)
    if not source.is_dir():
        raise ValueError("source must be a directory")
    with tarfile.open(archive, "w") as output:
        for base, directories, files in os.walk(source, followlinks=False):
            for name in sorted(directories + files):
                path = Path(base, name)
                info = path.lstat()
                relative = path.relative_to(source).as_posix()
                safe_parts(relative)
                if stat.S_ISDIR(info.st_mode):
                    entry = tarfile.TarInfo(relative + "/")
                    entry.type = tarfile.DIRTYPE
                    entry.mode = 0o700
                    output.addfile(entry)
                elif stat.S_ISREG(info.st_mode):
                    entry = tarfile.TarInfo(relative)
                    entry.size = info.st_size
                    entry.mode = 0o600
                    with path.open("rb") as content:
                        output.addfile(entry, content)
                else:
                    raise ValueError(f"unsupported file type in backup source: {path}")


def extract(archive: Path, destination: Path) -> None:
    if destination.exists():
        raise ValueError("restore destination already exists")
    with tarfile.open(archive, "r") as source:
        entries = source.getmembers()
        if len(entries) > 1_000_000:
            raise ValueError("archive has too many entries")
        for entry in entries:
            safe_parts(entry.name.rstrip("/"))
            if not (entry.isdir() or entry.isfile()):
                raise ValueError(f"unsupported archive entry: {entry.name!r}")
        destination.mkdir(mode=0o700, parents=True)
        for entry in entries:
            target = destination.joinpath(*safe_parts(entry.name.rstrip("/")))
            if entry.isdir():
                target.mkdir(mode=0o700, parents=True, exist_ok=True)
                continue
            target.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
            stream = source.extractfile(entry)
            if stream is None:
                raise ValueError(f"missing archive content: {entry.name!r}")
            with stream, target.open("xb") as output:
                while chunk := stream.read(1024 * 1024):
                    output.write(chunk)
            target.chmod(0o600)


def main() -> None:
    if len(sys.argv) != 4 or sys.argv[1] not in ("pack", "extract"):
        raise SystemExit("usage: backup_files.py pack SOURCE ARCHIVE | extract ARCHIVE DESTINATION")
    if sys.argv[1] == "pack":
        pack(Path(sys.argv[2]), Path(sys.argv[3]))
    else:
        extract(Path(sys.argv[2]), Path(sys.argv[3]))


if __name__ == "__main__":
    main()
