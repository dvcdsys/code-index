import hashlib
from dataclasses import dataclass
from pathlib import Path

import pathspec

from ..core.language import detect_language
from .project_config import load_project_config, parse_submodule_paths


@dataclass
class DiscoveredFile:
    path: str               # container path
    host_path: str          # original host path
    size: int
    content_hash: str       # SHA256
    language: str | None    # detected from extension


class FileDiscoveryService:
    def discover(
        self,
        project_container_path: str,
        exclude_patterns: list[str],
        max_file_size: int,
    ) -> list[DiscoveredFile]:
        root = Path(project_container_path)
        if not root.exists():
            return []

        # Load .gitignore and .cixignore if present (same format, merged)
        ignore_patterns: list[str] = []
        for ignore_file in (".gitignore", ".cixignore"):
            ignore_path = root / ignore_file
            if ignore_path.exists():
                with open(ignore_path, "r", errors="ignore") as f:
                    ignore_patterns.extend(f.readlines())

        # Load .cixconfig.yaml — if ignore.submodules is true, exclude submodule paths
        proj_cfg = load_project_config(project_container_path)
        if proj_cfg.ignore.submodules:
            for sp in parse_submodule_paths(project_container_path):
                ignore_patterns.append(sp + "/\n")

        ignore_spec = pathspec.PathSpec.from_lines("gitwildmatch", ignore_patterns) if ignore_patterns else None

        discovered = []
        exclude_set = set(exclude_patterns)

        for file_path in root.rglob("*"):
            if not file_path.is_file():
                continue

            # Check excluded directory names
            parts = file_path.relative_to(root).parts
            if any(part in exclude_set for part in parts):
                continue

            # Check .gitignore / .cixignore
            relative = str(file_path.relative_to(root))
            if ignore_spec and ignore_spec.match_file(relative):
                continue

            # Check file size
            try:
                size = file_path.stat().st_size
            except OSError:
                continue
            if size > max_file_size or size == 0:
                continue

            # Detect language
            language = detect_language(str(file_path))

            # Compute hash
            try:
                content_hash = self._hash_file(file_path)
            except OSError:
                continue

            host_path = str(file_path)

            discovered.append(
                DiscoveredFile(
                    path=str(file_path),
                    host_path=host_path,
                    size=size,
                    content_hash=content_hash,
                    language=language,
                )
            )

        return discovered

    def get_changed_files(
        self,
        discovered: list[DiscoveredFile],
        stored_hashes: dict[str, str],
    ) -> tuple[list[DiscoveredFile], list[str]]:
        changed_or_new = []
        current_paths = set()

        for f in discovered:
            current_paths.add(f.host_path)
            stored_hash = stored_hashes.get(f.host_path)
            if stored_hash is None or stored_hash != f.content_hash:
                changed_or_new.append(f)

        deleted = [p for p in stored_hashes if p not in current_paths]
        return changed_or_new, deleted

    @staticmethod
    def _hash_file(path: Path) -> str:
        h = hashlib.sha256()
        with open(path, "rb") as f:
            for chunk in iter(lambda: f.read(8192), b""):
                h.update(chunk)
        return h.hexdigest()


file_discovery_service = FileDiscoveryService()
