#!/usr/bin/env python3
"""Check new public text using only the standard library; never scan history."""

import argparse
import ipaddress
import json
from pathlib import Path
import re
import subprocess
import sys


ROOT = Path(__file__).resolve().parent.parent
# Assemble roots so the detector's own source does not publish path examples.
PATTERNS = {
    "user-home": re.compile(r"/(?:Users|home)/[^/\s]+"),
    "private-temp": re.compile(r"/(?:private/)?var/" + r"folders/"),
    "mounted-volume": re.compile(r"/" + r"Volumes/"),
    "local-host": re.compile(r"(?<![\w.-])[A-Za-z0-9-]+\.local\b(?![\w.-])"),
    "uuid-v4": re.compile(
        r"\b[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}\b",
        re.IGNORECASE,
    ),
    "lan-ip": re.compile(r"(?<![\w.])(?:[0-9]{1,3}\.){3}[0-9]{1,3}(?!\w|\.\w)"),
}
LAN = tuple(ipaddress.ip_network(net) for net in (
    "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16",
))


def scan(text, source, allow, testdata=False):
    """Return source/line/pattern diagnostics; allow exact matches or complete lines."""
    findings = []
    for number, line in enumerate(text.splitlines(), 1):
        for name, pattern in PATTERNS.items():
            if name == "uuid-v4" and testdata:
                continue
            for match in pattern.finditer(line):
                if match.group() in allow or line in allow:
                    continue
                if name == "lan-ip":
                    try:
                        addr = ipaddress.ip_address(match.group())
                    except ValueError:
                        continue
                    if not any(addr in net for net in LAN):
                        continue
                findings.append((source, number, name, line))
    return findings


def git(*args):
    return subprocess.check_output(
        ["git", "-c", "core.quotePath=false", *args], encoding="utf-8", errors="replace",
    )


def changes(base, head, allow):
    """Check PR commits and added diff lines, leaving pre-existing leaks out of scope."""
    findings = scan(git("log", "--format=%B", f"{base}..{head}"), "commits", allow)
    # Ask Git for NUL-separated paths, then a patch per path: quoted filenames and
    # source lines beginning with +++ cannot change the UUID exception's scope.
    paths = git("diff", "--name-only", "-z", f"{base}...{head}").split("\0")
    for path in filter(None, paths):
        testdata = "testdata" in Path(path).parts[:-1]
        findings.extend(scan(path, "diff filename", allow, testdata))
        patch = git("diff", "--no-ext-diff", "--no-textconv", "--no-renames",
                    "--unified=0", f"{base}...{head}", "--", path)
        number = None
        for line in patch.splitlines():
            hunk = re.match(r"@@ -\d+(?:,\d+)? \+(\d+)(?:,\d+)? @@", line)
            if hunk:
                number = int(hunk[1])
            elif number is not None and line.startswith("+"):
                findings.extend((src, number, kind, value) for src, _, kind, value
                                in scan(line[1:], path, allow, testdata))
                number += 1
            elif number is not None and line.startswith(" "):
                number += 1
    return findings


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    mode = parser.add_mutually_exclusive_group(required=True)
    mode.add_argument("--text", action="store_true", help="check a PR body on stdin")
    mode.add_argument("--event", type=Path, help="check a GitHub PR event and its changes")
    mode.add_argument("--issue-comment", type=Path, help="render a safe issue flag comment")
    parser.add_argument("--base")
    parser.add_argument("--head")
    parser.add_argument("--allow", type=Path, default=ROOT / ".github/public-text-allow.txt")
    args = parser.parse_args()
    allow = {line for line in args.allow.read_text().splitlines()
             if line and not line.startswith("#")}
    if args.text:
        findings = scan(sys.stdin.read(), "PR body", allow)
    else:
        event = json.loads((args.event or args.issue_comment).read_text())
        item = event["pull_request" if args.event else "issue"]
        label = "PR" if args.event else "issue"
        findings = scan(item["title"], f"{label} title", allow)
        findings += scan(item.get("body") or "", f"{label} body", allow)
        if args.event:
            if not args.base or not args.head:
                parser.error("--event requires --base and --head")
            findings += changes(args.base, args.head, allow)
    if args.issue_comment:
        if findings:
            # Flag categories and positions, never republish the personal value.
            print("<!-- public-text -->\n🤖 Codex — Public text check found:")
            for source, number, kind, _ in sorted(set(findings)):
                print(f"- {source}, line {number}: `{kind}`")
            print("\nPlease replace personal values with `~`, `$TMPDIR`, `<workspace>`, "
                  "`<host>`, `<lan-ip>`, or rounded counts. See CONTRIBUTING.md#public-text.")
        return 0
    for source, number, kind, line in findings:
        # JSON quoting keeps hostile newlines/control characters out of log commands.
        print(f"public-text: {json.dumps(source)}:{number}: {kind}: {json.dumps(line)}")
    if not findings:
        print("public-text: clean")
    return int(bool(findings))


if __name__ == "__main__":
    sys.exit(main())
