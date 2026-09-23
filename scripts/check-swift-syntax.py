#!/usr/bin/env python3
"""Syntax-only parse of the macOS app. NOT a compiler and NOT a test run.

There is no Swift toolchain in the environment this app is edited from, and CI only
builds the mac target on workflow_dispatch, so a syntax error can otherwise reach a
commit unnoticed. This catches unbalanced braces and malformed declarations. It cannot
catch type errors, missing members, or anything semantic.
"""
from pathlib import Path
import json
import sys

ROOT = Path(__file__).resolve().parents[1] / "macos"
sys.path.insert(0, str(Path(__file__).resolve().parents[2] / "nexal-ios/validation/python-tools"))
try:
    import tree_sitter
    import tree_sitter_swift
except ImportError:
    print("tree-sitter/tree-sitter-swift unavailable; syntax parse NOT run.", file=sys.stderr)
    sys.exit(2)

parser = tree_sitter.Parser(tree_sitter.Language(tree_sitter_swift.language()))
report = {"tool": "tree-sitter-swift",
          "scope": "syntax parsing only; NOT compilation, type checking or XCTest execution",
          "files": [], "errors": []}

targets = sorted(list((ROOT / "Sources").rglob("*.swift")) + list((ROOT / "Tests").rglob("*.swift")))
pkg = ROOT / "Package.swift"
if pkg.exists():
    targets.append(pkg)

for path in targets:
    tree = parser.parse(path.read_bytes())
    rel = str(path.relative_to(ROOT))
    report["files"].append(rel)
    # Walk for ERROR/MISSING nodes; a parse that "succeeds" can still contain them.
    stack = [tree.root_node]
    while stack:
        node = stack.pop()
        if node.type == "ERROR" or node.is_missing:
            report["errors"].append({"file": rel, "line": node.start_point[0] + 1,
                                     "type": node.type,
                                     "text": node.text.decode("utf8", "replace")[:120]})
        stack.extend(node.children)

report["file_count"] = len(report["files"])
del report["files"]
print(json.dumps(report, indent=2))
sys.exit(1 if report["errors"] else 0)
