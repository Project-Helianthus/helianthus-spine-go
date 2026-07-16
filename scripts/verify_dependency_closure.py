#!/usr/bin/env python3
"""Verify that tracked dependency controls resolve only reviewed fork modules."""

from __future__ import annotations

import argparse
import hashlib
import json
import re
import subprocess
import sys
from collections import deque
from pathlib import Path, PurePosixPath
from typing import Any


EVIDENCE_SCHEMA = "helianthus.dependency-closure-evidence.v1"
UPSTREAM_MODULES = (
    "github.com/enbility/ship-go",
    "github.com/enbility/spine-go",
)
PROJECT_FORK_RE = re.compile(
    r"github\.com/Project-Helianthus/helianthus-(?:eebus|ship|spine)-go"
)
REPLACE_RE = re.compile(r"(?m)^\s*replace(?:\s|\()")
WORKSPACE_USE_RE = re.compile(r"(?m)^\s*use(?:\s|\()")
BUILD_RELEASE_SCRIPT_RE = re.compile(
    r"(?:^|[-_.])(build|package|publish|release)(?:$|[-_.])", re.IGNORECASE
)
CONTROL_HINT_RE = re.compile(
    r"(?:^|[-_.])(build|dependencies|dependency|package|publish|release)(?:$|[-_.])",
    re.IGNORECASE,
)
CONFIG_NAMES = {
    ".goreleaser.json",
    ".goreleaser.toml",
    ".goreleaser.yaml",
    ".goreleaser.yml",
    ".mockery.yaml",
    ".mockery.yml",
}


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repo", required=True)
    parser.add_argument("--manifest", required=True)
    parser.add_argument("--inventory-output", required=True)
    parser.add_argument("--evidence-output", required=True)
    return parser.parse_args()


def tracked_inventory(repo: Path) -> bytes:
    return subprocess.run(
        ["git", "ls-files", "-z"],
        cwd=repo,
        check=True,
        stdout=subprocess.PIPE,
    ).stdout


def inventory_paths(inventory: bytes) -> list[str]:
    if not inventory:
        return []
    return [value.decode("utf-8") for value in inventory.rstrip(b"\0").split(b"\0")]


def write_bytes(path: Path, data: bytes) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_bytes(data)


def stable_path(value: str) -> str | None:
    path = PurePosixPath(value)
    if not value or path.is_absolute() or ".." in path.parts or "\0" in value:
        return None
    return path.as_posix()


def auto_class(path: str) -> str | None:
    if path == "go.mod":
        return "go_module"
    if path == "go.sum":
        return "go_checksum"
    if path == "go.work":
        return "workspace"
    if path == "go.work.sum":
        return "workspace_checksum"
    if path == "vendor/modules.txt":
        return "vendor_manifest"
    if path.startswith(".github/workflows/"):
        return "workflow"
    if path.startswith(".github/actions/"):
        return "local_action"

    name = PurePosixPath(path).name
    if name in {"GNUmakefile", "Makefile", "makefile"} or name.endswith(".mk"):
        return "makefile"
    if path.startswith("config/") or name in CONFIG_NAMES:
        return "config"
    if path.startswith("scripts/") and BUILD_RELEASE_SCRIPT_RE.search(name):
        return "build_release_control"
    return None


def declared_class(path: str, recursive: bool) -> str:
    classified = auto_class(path)
    if classified is not None:
        return classified
    if recursive:
        return "release_input"
    if path.startswith("release/"):
        return "release_config"
    return "dependency_control_input"


def looks_unclassified(path: str) -> bool:
    if path.startswith("contracttests/") or path.startswith("provenance/"):
        return False
    if path == "scripts/verify_dependency_closure.py":
        return False
    return path.startswith("release/") or CONTROL_HINT_RE.search(
        PurePosixPath(path).name
    ) is not None


def nested_inputs(data: Any) -> list[str]:
    found: list[str] = []

    def visit(value: Any) -> None:
        if isinstance(value, dict):
            for key, child in value.items():
                if isinstance(key, str) and key.endswith("_inputs"):
                    if isinstance(child, str):
                        found.append(child)
                    elif isinstance(child, list):
                        found.extend(item for item in child if isinstance(item, str))
                visit(child)
        elif isinstance(value, list):
            for child in value:
                visit(child)

    visit(data)
    return found


def add_violation(
    violations: set[tuple[str, str, str]], path: str, input_class: str, reason: str
) -> None:
    violations.add((path, input_class, reason))


def scan_versions(
    text: str,
    path: str,
    input_class: str,
    reviewed: dict[str, str],
    violations: set[tuple[str, str, str]],
) -> None:
    for match in PROJECT_FORK_RE.finditer(text):
        module = match.group(0)
        suffix = text[match.end() :]
        version_match = re.match(r"(?:[ \t]+|@)([^\s\"'`,;#]+)", suffix)
        if version_match is None:
            continue
        version = version_match.group(1)
        if version.endswith("/go.mod"):
            version = version[: -len("/go.mod")]
        if not (version.startswith("v") or version.startswith("helianthus-")):
            continue
        if reviewed.get(module) != version:
            add_violation(
                violations,
                path,
                input_class,
                "unreviewed_project_fork_version",
            )


def canonical_evidence(
    result: str,
    inventory: bytes,
    inputs: dict[str, str],
    violations: set[tuple[str, str, str]],
) -> bytes:
    evidence = {
        "inputs": [
            {"class": input_class, "path": path}
            for path, input_class in sorted(inputs.items())
        ],
        "result": result,
        "schema": EVIDENCE_SCHEMA,
        "tracked_inventory_sha256": hashlib.sha256(inventory).hexdigest(),
        "violations": [
            {"class": input_class, "path": path, "reason": reason}
            for path, input_class, reason in sorted(violations)
        ],
    }
    return (json.dumps(evidence, sort_keys=True, separators=(",", ":")) + "\n").encode()


def main() -> int:
    args = parse_args()
    repo = Path(args.repo).resolve()
    inventory = tracked_inventory(repo)
    write_bytes(Path(args.inventory_output), inventory)
    tracked = set(inventory_paths(inventory))

    violations: set[tuple[str, str, str]] = set()
    inputs: dict[str, str] = {}
    manifest_path = stable_path(args.manifest)
    manifest: dict[str, Any] = {}
    if manifest_path is None or manifest_path not in tracked:
        shown_path = manifest_path or "invalid-manifest-path"
        add_violation(violations, shown_path, "provenance", "invalid_manifest")
    else:
        try:
            value = json.loads((repo / manifest_path).read_text(encoding="utf-8"))
            if isinstance(value, dict):
                manifest = value
            else:
                add_violation(
                    violations, manifest_path, "provenance", "invalid_manifest"
                )
        except (OSError, UnicodeError, json.JSONDecodeError):
            add_violation(violations, manifest_path, "provenance", "invalid_manifest")

    reviewed: dict[str, str] = {}
    dependencies = manifest.get("reviewed_dependencies", [])
    if isinstance(dependencies, list):
        for dependency in dependencies:
            if not isinstance(dependency, dict):
                continue
            module = dependency.get("module")
            version = dependency.get("version")
            if isinstance(module, str) and isinstance(version, str):
                reviewed[module] = version

    queue: deque[tuple[str, bool]] = deque()
    declared = manifest.get("dependency_control_inputs", [])
    if isinstance(declared, list):
        for value in declared:
            if isinstance(value, str):
                queue.append((value, False))
    else:
        add_violation(
            violations,
            manifest_path or "invalid-manifest-path",
            "provenance",
            "invalid_manifest",
        )

    visited_declared: set[str] = set()
    while queue:
        raw_path, recursive = queue.popleft()
        path = stable_path(raw_path)
        if path is None:
            add_violation(
                violations,
                "invalid-declared-path",
                "dependency_control_input",
                "invalid_declared_input",
            )
            continue
        if path in visited_declared:
            continue
        visited_declared.add(path)
        input_class = declared_class(path, recursive)
        inputs[path] = input_class
        if path not in tracked:
            add_violation(violations, path, input_class, "declared_input_untracked")
            continue
        if PurePosixPath(path).suffix.lower() != ".json":
            continue
        try:
            value = json.loads((repo / path).read_text(encoding="utf-8"))
        except (OSError, UnicodeError, json.JSONDecodeError):
            continue
        for child in nested_inputs(value):
            queue.append((child, True))

    for path in tracked:
        if path == manifest_path or path == "scripts/verify_dependency_closure.py":
            continue
        classified = auto_class(path)
        if classified is not None:
            inputs.setdefault(path, classified)

    for path in tracked:
        if path not in inputs and path != manifest_path and looks_unclassified(path):
            add_violation(
                violations, path, "unclassified", "unclassified_dependency_control"
            )

    for path, input_class in sorted(inputs.items()):
        if path not in tracked:
            continue
        try:
            text = (repo / path).read_text(encoding="utf-8")
        except (OSError, UnicodeError):
            add_violation(violations, path, input_class, "unreadable_input")
            continue
        if any(module in text for module in UPSTREAM_MODULES):
            add_violation(
                violations, path, input_class, "upstream_module_identity"
            )
        if input_class in {"go_module", "workspace"} and REPLACE_RE.search(text):
            add_violation(violations, path, input_class, "replace_directive")
        if input_class == "workspace" and WORKSPACE_USE_RE.search(text):
            add_violation(
                violations, path, input_class, "workspace_local_selection"
            )
        scan_versions(text, path, input_class, reviewed, violations)

    result = "fail" if violations else "pass"
    write_bytes(
        Path(args.evidence_output),
        canonical_evidence(result, inventory, inputs, violations),
    )
    if violations:
        for path, input_class, reason in sorted(violations):
            print(
                f"dependency-closure: FAIL reason={reason} path={path} class={input_class}",
                file=sys.stderr,
            )
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
