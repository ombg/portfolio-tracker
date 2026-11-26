"""Content-addressed storage of originals (D-12)."""

from __future__ import annotations

import hashlib
import os

import pytest

from importer import storage


def test_store_writes_once_by_hash(tmp_path):
    data = b"synthetic export"
    sha, rel = storage.store(tmp_path, data, "Activities.XLSX")
    assert sha == hashlib.sha256(data).hexdigest()
    assert rel == f"{sha[:2]}/{sha}.xlsx"
    assert (tmp_path / rel).read_bytes() == data
    assert storage.store(tmp_path, data, "Activities.XLSX") == (sha, rel)
    assert [p.name for p in tmp_path.rglob("*")] == [sha[:2], f"{sha}.xlsx"]  # no temp files left


@pytest.mark.parametrize("name", ["no-extension", "evil.x/../../y", "a.thisistoolong", "a.c$v", ""])
def test_unsafe_or_missing_extension_is_dropped(name):
    sha = "ab" + "0" * 62
    assert storage.relative_path(sha, name) == f"ab/{sha}"


def test_tampered_stored_file_is_detected(tmp_path):
    sha, rel = storage.store(tmp_path, b"original", "a.csv")
    path = tmp_path / rel
    os.chmod(path, 0o640)
    path.write_bytes(b"changed")
    with pytest.raises(storage.StorageError):
        storage.store(tmp_path, b"original", "a.csv")
