import os
import tempfile
from pathlib import Path

import pytest

from api.app.services.file_discovery import FileDiscoveryService


def _write(root: Path, rel_path: str, content: str = "hello") -> None:
    p = root / rel_path
    p.parent.mkdir(parents=True, exist_ok=True)
    p.write_text(content)


@pytest.fixture
def svc() -> FileDiscoveryService:
    return FileDiscoveryService()


class TestCixignore:
    def test_root_cixignore_excludes_files(self, svc: FileDiscoveryService) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            _write(root, ".cixignore", "*.log\nsecret.txt\n")
            _write(root, "main.go", "package main")
            _write(root, "app.log", "log data")
            _write(root, "secret.txt", "password")
            _write(root, "readme.txt", "hello")

            files = svc.discover(tmp, [], 524288)
            paths = sorted(f.path for f in files)

            assert any("main.go" in p for p in paths)
            assert any("readme.txt" in p for p in paths)
            assert not any("app.log" in p for p in paths)
            assert not any("secret.txt" in p for p in paths)

    def test_cixignore_directory_pattern(self, svc: FileDiscoveryService) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            _write(root, ".cixignore", "submodules/\n")
            _write(root, "main.go", "package main")
            _write(root, "submodules/vendor/lib.go", "package lib")
            _write(root, "src/app.go", "package src")

            files = svc.discover(tmp, [], 524288)
            paths = [f.path for f in files]

            assert any("main.go" in p for p in paths)
            assert any("app.go" in p for p in paths)
            assert not any("submodules" in p for p in paths)

    def test_cixignore_and_gitignore_merged(self, svc: FileDiscoveryService) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            _write(root, ".gitignore", "*.log\n")
            _write(root, ".cixignore", "*.tmp\n")
            _write(root, "main.go", "package main")
            _write(root, "app.log", "log")
            _write(root, "cache.tmp", "temp")
            _write(root, "readme.txt", "hello")

            files = svc.discover(tmp, [], 524288)
            paths = sorted(f.path for f in files)

            assert any("main.go" in p for p in paths)
            assert any("readme.txt" in p for p in paths)
            assert not any("app.log" in p for p in paths)
            assert not any("cache.tmp" in p for p in paths)

    def test_only_cixignore_no_gitignore(self, svc: FileDiscoveryService) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            _write(root, ".cixignore", "generated/\n*.bak\n")
            _write(root, "main.go", "package main")
            _write(root, "config.bak", "old config")
            _write(root, "generated/api.go", "package gen")

            files = svc.discover(tmp, [], 524288)
            paths = [f.path for f in files]

            assert any("main.go" in p for p in paths)
            assert not any("config.bak" in p for p in paths)
            assert not any("generated" in p for p in paths)

    def test_only_gitignore_no_cixignore(self, svc: FileDiscoveryService) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            _write(root, ".gitignore", "*.log\n")
            _write(root, "main.go", "package main")
            _write(root, "app.log", "log data")

            files = svc.discover(tmp, [], 524288)
            paths = [f.path for f in files]

            assert any("main.go" in p for p in paths)
            assert not any("app.log" in p for p in paths)

    def test_no_ignore_files(self, svc: FileDiscoveryService) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            _write(root, "main.go", "package main")
            _write(root, "data.txt", "data")

            files = svc.discover(tmp, [], 524288)
            paths = sorted(f.path for f in files)

            assert len(paths) == 2
            assert any("main.go" in p for p in paths)
            assert any("data.txt" in p for p in paths)