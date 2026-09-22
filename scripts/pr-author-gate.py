#!/usr/bin/env python3
"""Enforce Teseo authorship, one GitHub committer exception, and no co-authors."""

import argparse
from pathlib import Path
import re
import subprocess
import sys


ROOT = Path(__file__).resolve().parent.parent
DEFAULT_ALLOW = ROOT / ".github/pr-author-allow.txt"
TESEO_EMAIL = "sol@javiermellado.com"
GITHUB_NOREPLY_COMMITTER = "noreply@github.com"
TRAILER = re.compile(r"(?i)^Co-authored-by:")
RECORD = "\x1e"
FIELD = "\x1f"


def load_allow(path):
    if not path.is_file():
        raise SystemExit(f"pr-author: missing allowlist {path}")
    emails = {line.strip().lower() for line in path.read_text().splitlines()
              if line.strip() and not line.lstrip().startswith("#")}
    if not emails:
        raise SystemExit(f"pr-author: empty allowlist {path}")
    return emails


def git(repo, *args):
    return subprocess.check_output(
        ["git", "-C", str(repo), "-c", "core.quotePath=false", *args],
        encoding="utf-8", errors="replace",
    )


def commits(repo, base, head):
    raw = git(repo, "log", f"--format=%H{FIELD}%ae{FIELD}%ce{FIELD}%B{RECORD}",
              f"{base}..{head}")
    rows = []
    for record in raw.split(RECORD):
        record = record.strip("\n")
        if not record:
            continue
        sha, author, committer, body = record.split(FIELD, 3)
        rows.append((sha, author, committer, body))
    return rows


def findings(rows, allow):
    out = []
    for sha, author, committer, body in rows:
        short = sha[:12]
        for line in body.splitlines():
            if TRAILER.match(line):
                out.append(f"pr-author: {short}: Co-authored-by trailer")
                break
        if author.lower() not in allow:
            out.append(f"pr-author: {short}: author email not on allowlist: {author}")
        committer_email = committer.lower()
        teseo_github_commit = (
            author.lower() == TESEO_EMAIL
            and committer_email == GITHUB_NOREPLY_COMMITTER
        )
        if committer_email not in allow and not teseo_github_commit:
            out.append(f"pr-author: {short}: committer email not on allowlist: {committer}")
    return out


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base", required=True)
    parser.add_argument("--head", required=True)
    parser.add_argument("--allow", type=Path, default=DEFAULT_ALLOW)
    parser.add_argument("--repo", type=Path, default=Path("."))
    args = parser.parse_args()
    allow = load_allow(args.allow)
    found = findings(commits(args.repo, args.base, args.head), allow)
    for line in found:
        print(line)
    if not found:
        print("pr-author: clean")
    return int(bool(found))


if __name__ == "__main__":
    sys.exit(main())
