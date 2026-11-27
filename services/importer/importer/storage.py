"""Content-addressed store for original uploads, kept byte for byte (D-12, FR-3 item 1)."""

from __future__ import annotations

import hashlib
import os
import re
import secrets
from pathlib import Path


class StorageError(RuntimeError):
    pass


def sha256_hex(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def _file_sha256(path: Path) -> str:
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


def relative_path(sha: str, original_name: str) -> str:
    """`ab/abcdef….xlsx`: first two hex digits as folder, the hash as name, a sanitised extension."""
    ext = Path(original_name).suffix.lower().lstrip(".")
    suffix = f".{ext}" if re.fullmatch(r"[a-z0-9]{1,8}", ext) else ""
    return f"{sha[:2]}/{sha}{suffix}"


def store(files_dir: Path, data: bytes, original_name: str) -> tuple[str, str]:
    """Writes data once and returns (sha256, path relative to files_dir). An existing file with the same name is
    reused only if its bytes still hash to the same value."""
    sha = sha256_hex(data)
    rel = relative_path(sha, original_name)
    path = files_dir / rel
    if path.exists():
        if _file_sha256(path) != sha:
            raise StorageError(f"stored file {rel} no longer matches its hash")
        return sha, rel
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp = path.with_name(f".{path.name}.{secrets.token_hex(8)}.tmp")
    try:
        with open(tmp, "xb") as f:
            f.write(data)
            f.flush()
            os.fsync(f.fileno())
        os.chmod(tmp, 0o440)  # originals are never modified (NFR-3)
        os.replace(tmp, path)
    finally:
        tmp.unlink(missing_ok=True)
    if _file_sha256(path) != sha:
        raise StorageError(f"stored file {rel} does not match the upload")
    return sha, rel


STORED_PATH = re.compile(r"[0-9a-f]{2}/[0-9a-f]{64}(\.[a-z0-9]{1,8})?")


def load(files_dir: Path, rel: str, sha: str) -> bytes:
    """The stored original, checked against its hash before anything reads it again (D-12)."""
    if not STORED_PATH.fullmatch(rel) or not rel.startswith(sha[:2] + "/" + sha):
        raise StorageError(f"not a stored file path: {rel!r}")
    data = (files_dir / rel).read_bytes()
    if sha256_hex(data) != sha:
        raise StorageError(f"stored file {rel} no longer matches its hash")
    return data
