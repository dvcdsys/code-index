import re
from dataclasses import dataclass, field
from pathlib import Path

import yaml


@dataclass
class IgnoreConfig:
    submodules: bool = False


@dataclass
class ProjectConfig:
    ignore: IgnoreConfig = field(default_factory=IgnoreConfig)


def load_project_config(project_root: str) -> ProjectConfig:
    """Load .cixconfig.yaml from the project root.
    Returns default config if the file does not exist."""
    config_path = Path(project_root) / ".cixconfig.yaml"
    if not config_path.exists():
        return ProjectConfig()

    try:
        data = yaml.safe_load(config_path.read_text())
    except Exception:
        return ProjectConfig()

    if not isinstance(data, dict):
        return ProjectConfig()

    ignore_data = data.get("ignore", {})
    return ProjectConfig(
        ignore=IgnoreConfig(
            submodules=bool(ignore_data.get("submodules", False)),
        )
    )


def parse_submodule_paths(project_root: str) -> list[str]:
    """Parse .gitmodules and return list of submodule paths.
    Returns empty list if .gitmodules does not exist."""
    gitmodules_path = Path(project_root) / ".gitmodules"
    if not gitmodules_path.exists():
        return []

    paths: list[str] = []
    try:
        for line in gitmodules_path.read_text().splitlines():
            line = line.strip()
            if line.startswith("path"):
                parts = line.split("=", 1)
                if len(parts) == 2:
                    p = parts[1].strip()
                    if p:
                        paths.append(p)
    except Exception:
        pass

    return paths