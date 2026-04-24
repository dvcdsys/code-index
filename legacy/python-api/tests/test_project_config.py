import tempfile
from pathlib import Path

import pytest

from api.app.services.project_config import (
    IgnoreConfig,
    ProjectConfig,
    load_project_config,
    parse_submodule_paths,
)


def _write(root: Path, rel_path: str, content: str) -> None:
    p = root / rel_path
    p.parent.mkdir(parents=True, exist_ok=True)
    p.write_text(content)


class TestLoadProjectConfig:
    def test_submodules_true(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            _write(Path(tmp), ".cixconfig.yaml", "ignore:\n  submodules: true\n")
            cfg = load_project_config(tmp)
            assert cfg.ignore.submodules is True

    def test_submodules_false(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            _write(Path(tmp), ".cixconfig.yaml", "ignore:\n  submodules: false\n")
            cfg = load_project_config(tmp)
            assert cfg.ignore.submodules is False

    def test_no_file(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            cfg = load_project_config(tmp)
            assert cfg.ignore.submodules is False

    def test_empty_file(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            _write(Path(tmp), ".cixconfig.yaml", "")
            cfg = load_project_config(tmp)
            assert cfg.ignore.submodules is False

    def test_invalid_yaml(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            _write(Path(tmp), ".cixconfig.yaml", ":::bad{{{yaml")
            cfg = load_project_config(tmp)
            # Should return default config, not crash
            assert cfg.ignore.submodules is False


class TestParseSubmodulePaths:
    def test_standard_gitmodules(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            _write(
                Path(tmp),
                ".gitmodules",
                '[submodule "api/schema"]\n'
                "\tpath = api/schema\n"
                "\turl = https://example.com/schema.git\n"
                '[submodule "libs/vendor"]\n'
                "\tpath = libs/vendor\n"
                "\turl = https://example.com/vendor.git\n",
            )
            paths = parse_submodule_paths(tmp)
            assert sorted(paths) == ["api/schema", "libs/vendor"]

    def test_no_gitmodules(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            paths = parse_submodule_paths(tmp)
            assert paths == []

    def test_empty_gitmodules(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            _write(Path(tmp), ".gitmodules", "")
            paths = parse_submodule_paths(tmp)
            assert paths == []

    def test_single_submodule(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            _write(
                Path(tmp),
                ".gitmodules",
                '[submodule "vendor"]\n\tpath = vendor\n\turl = https://example.com/v.git\n',
            )
            paths = parse_submodule_paths(tmp)
            assert paths == ["vendor"]


class TestFileDiscoveryWithSubmodules:
    def test_submodules_excluded(self) -> None:
        from api.app.services.file_discovery import FileDiscoveryService

        svc = FileDiscoveryService()

        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            _write(root, ".cixconfig.yaml", "ignore:\n  submodules: true\n")
            _write(
                root,
                ".gitmodules",
                '[submodule "vendor"]\n\tpath = vendor\n\turl = https://example.com/v.git\n',
            )
            _write(root, "main.go", "package main")
            _write(root, "vendor/lib.go", "package vendor")
            _write(root, "vendor/deep/util.go", "package deep")
            _write(root, "src/app.go", "package src")

            files = svc.discover(tmp, [], 524288)
            paths = sorted(f.path for f in files)

            assert any("main.go" in p for p in paths)
            assert any("app.go" in p for p in paths)
            assert not any("vendor" in p for p in paths)

    def test_submodules_not_excluded_when_false(self) -> None:
        from api.app.services.file_discovery import FileDiscoveryService

        svc = FileDiscoveryService()

        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            _write(root, ".cixconfig.yaml", "ignore:\n  submodules: false\n")
            _write(
                root,
                ".gitmodules",
                '[submodule "vendor"]\n\tpath = vendor\n\turl = https://example.com/v.git\n',
            )
            _write(root, "main.go", "package main")
            _write(root, "vendor/lib.go", "package vendor")

            files = svc.discover(tmp, [], 524288)
            paths = [f.path for f in files]

            assert any("main.go" in p for p in paths)
            assert any("vendor" in p for p in paths)