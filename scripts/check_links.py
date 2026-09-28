#!/usr/bin/env python3
"""Check relative links and anchors in a repository's Markdown files.

Usage: check_links.py [ROOT]

Every relative link must point at an existing file or directory, and a
#fragment must match a heading in the target. External links (http, mailto)
are not fetched. Exits 1 and lists each broken link, 0 when all resolve.
"""

import re
import sys
from pathlib import Path

SKIP_DIRS = {".git", "node_modules", "vendor", "bin", "dist", "target"}
LINK = re.compile(r"(?<!!)\[[^\]]*\]\(([^)\s]+)(?:\s+\"[^\"]*\")?\)|!\[[^\]]*\]\(([^)\s]+)\)")
HEADING = re.compile(r"^#{1,6}\s+(.*?)\s*#*\s*$")
FENCE = re.compile(r"^\s*(```|~~~)")


def slug(text: str) -> str:
    """GitHub's heading anchor: lowercase, drop punctuation, spaces to dashes."""
    text = re.sub(r"`([^`]*)`", r"\1", text).strip().lower()
    text = re.sub(r"[^\w\- ]", "", text)
    return text.replace(" ", "-")


def lines_outside_fences(path: Path):
    fenced = False
    for number, line in enumerate(path.read_text(encoding="utf-8").splitlines(), 1):
        if FENCE.match(line):
            fenced = not fenced
            continue
        if not fenced:
            yield number, line


def anchors(path: Path) -> set:
    found, seen = set(), {}
    for _, line in lines_outside_fences(path):
        match = HEADING.match(line)
        if match:
            base = slug(match.group(1))
            count = seen.get(base, 0)
            seen[base] = count + 1
            found.add(base if count == 0 else f"{base}-{count}")
    return found


def markdown_files(root: Path):
    for path in sorted(root.rglob("*.md")):
        if not SKIP_DIRS.intersection(path.relative_to(root).parts):
            yield path


def main() -> int:
    root = Path(sys.argv[1] if len(sys.argv) > 1 else ".").resolve()
    broken = []
    for path in markdown_files(root):
        for number, line in lines_outside_fences(path):
            for match in LINK.finditer(line):
                target = match.group(1) or match.group(2)
                if re.match(r"^[a-z][a-z0-9+.-]*:", target, re.I):
                    continue  # external: http, https, mailto, …
                file_part, _, fragment = target.partition("#")
                dest = path if not file_part else (path.parent / file_part).resolve()
                where = f"{path.relative_to(root)}:{number}"
                if not dest.exists():
                    broken.append(f"{where}: missing target {target}")
                elif fragment and dest.suffix == ".md" and fragment not in anchors(dest):
                    broken.append(f"{where}: no heading for #{fragment} in {file_part or path.name}")
    for item in broken:
        print(item)
    if broken:
        print(f"{len(broken)} broken link(s)", file=sys.stderr)
        return 1
    print("all relative links resolve")
    return 0


if __name__ == "__main__":
    sys.exit(main())
