#!/usr/bin/env python3
"""Pair WhatsApp via terminal QR code and export GitHub Actions secret text files."""

from __future__ import annotations

import argparse
import base64
import os
import re
import secrets
import shutil
import subprocess
import sys
from pathlib import Path

WA_SENDER_DIR = Path(__file__).resolve().parent
DEFAULT_OUTPUT_DIR = WA_SENDER_DIR / "secrets"
GITHUB_SECRET_LIMIT_BYTES = 48 * 1024
GROUP_LINE_RE = re.compile(r"^([^\s]+@g\.us)\t\(([^)]+)\)\t(.*)$")


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description=(
            "Show a WhatsApp QR code in the terminal, pair locally, compact the "
            "encrypted session, and write GitHub secret .txt files."
        )
    )
    parser.add_argument(
        "--output-dir",
        type=Path,
        default=DEFAULT_OUTPUT_DIR,
        help=f"Directory where secret .txt files will be written (default: {DEFAULT_OUTPUT_DIR})",
    )
    parser.add_argument(
        "--parts",
        type=int,
        default=5,
        help="Number of WA_SESSION_ENC_PART files to split the encrypted session into (default: 5)",
    )
    parser.add_argument(
        "--force",
        action="store_true",
        help="Overwrite existing files in the output directory without prompting",
    )
    parser.add_argument(
        "--non-interactive",
        action="store_true",
        help="Skip interactive prompts for choosing group JIDs after pairing",
    )
    return parser.parse_args()


def split_even(text: str, num_parts: int) -> list[str]:
    if num_parts < 1:
        raise ValueError("--parts must be at least 1")
    base, extra = divmod(len(text), num_parts)
    parts: list[str] = []
    start = 0
    for idx in range(num_parts):
        end = start + base + (1 if idx < extra else 0)
        parts.append(text[start:end])
        start = end
    return parts


def run_notifier(args: list[str], env: dict[str, str], capture: bool = False) -> subprocess.CompletedProcess[str]:
    cmd = ["go", "run", "./cmd/notifier", *args]
    return subprocess.run(
        cmd,
        cwd=WA_SENDER_DIR,
        env=env,
        capture_output=capture,
        text=capture,
        encoding="utf-8" if capture else None,
        errors="replace" if capture else None,
        check=True,
    )


def prepare_output_dir(output_dir: Path, force: bool) -> None:
    output_dir.mkdir(parents=True, exist_ok=True)
    existing = [p for p in output_dir.iterdir() if p.is_file()]
    if existing and not force:
        print(f"Found {len(existing)} existing file(s) in {output_dir}:")
        for path in sorted(existing):
            print(f"  - {path.name}")
        answer = input("Overwrite existing session/secret files? [y/N]: ").strip().lower()
        if answer not in {"y", "yes"}:
            print("Aborted.")
            sys.exit(1)

    for stale_name in ("session.db.enc", "session.compact.db.enc", "session.groups.tmp.db.enc"):
        stale_path = output_dir / stale_name
        if stale_path.exists():
            stale_path.unlink()


def parse_groups(output: str) -> list[tuple[str, str, str]]:
    groups: list[tuple[str, str, str]] = []
    for line in output.splitlines():
        match = GROUP_LINE_RE.match(line.strip())
        if match:
            jid, members, name = match.groups()
            groups.append((jid, members, name))
    return groups


def prompt_for_group(
    secret_name: str,
    keyword: str,
    groups: list[tuple[str, str, str]],
    non_interactive: bool,
) -> str | None:
    matches = [idx for idx, (_, _, name) in enumerate(groups, start=1) if keyword in name.casefold()]
    suggested_idx = matches[0] if len(matches) == 1 else None

    if non_interactive:
        return groups[suggested_idx - 1][0] if suggested_idx is not None else None

    if suggested_idx is not None:
        jid, _, name = groups[suggested_idx - 1]
        prompt = (
            f"{secret_name} [Enter for #{suggested_idx} {name!r} ({jid}), "
            "or enter a number / @g.us JID, or 'skip']: "
        )
    else:
        prompt = f"{secret_name} (enter list number or @g.us JID, or press Enter to skip): "

    try:
        raw = input(prompt).strip()
    except EOFError:
        return groups[suggested_idx - 1][0] if suggested_idx is not None else None

    if not raw:
        return groups[suggested_idx - 1][0] if suggested_idx is not None else None
    if raw.casefold() in {"s", "skip"}:
        return None
    if raw.isdigit():
        idx = int(raw)
        if 1 <= idx <= len(groups):
            return groups[idx - 1][0]
        print(f"  Invalid selection {idx}; skipping {secret_name}.")
        return None
    if raw.endswith("@g.us"):
        return raw
    print(f"  Input {raw!r} does not end with @g.us; skipping {secret_name}.")
    return None


def main() -> int:
    args = parse_args()

    if shutil.which("go") is None:
        print("Error: 'go' was not found on PATH. Install Go 1.26+ and try again.", file=sys.stderr)
        return 1

    output_dir = args.output_dir.resolve()
    prepare_output_dir(output_dir, args.force)

    session_key = secrets.token_hex(32)
    env = os.environ.copy()
    env["WA_SESSION_KEY"] = session_key
    # Ensure notifier's CI guard does not block local interactive pairing.
    env.pop("CI", None)
    env.pop("GITHUB_ACTIONS", None)

    raw_enc = output_dir / "session.db.enc"
    compact_enc = output_dir / "session.compact.db.enc"
    groups_tmp_enc = output_dir / "session.groups.tmp.db.enc"

    print("Starting WhatsApp pairing. Scan the QR code in WhatsApp -> Linked Devices...\n")
    try:
        run_notifier(["pair", "--session-enc", str(raw_enc)], env=env)
    except subprocess.CalledProcessError as exc:
        print(f"\nPairing failed (exit code {exc.returncode}).", file=sys.stderr)
        return exc.returncode or 1

    print("\nCompacting encrypted session...")
    try:
        run_notifier(["compact", "--session-enc", str(raw_enc), "--output", str(compact_enc)], env=env)
    except subprocess.CalledProcessError as exc:
        print(f"Compaction failed (exit code {exc.returncode}).", file=sys.stderr)
        return exc.returncode or 1
    finally:
        if raw_enc.exists():
            raw_enc.unlink()

    encoded = base64.b64encode(compact_enc.read_bytes()).decode("ascii")
    chunks = split_even(encoded, args.parts)

    written_files: list[Path] = []
    key_file = output_dir / "WA_SESSION_KEY.txt"
    key_file.write_text(session_key, encoding="utf-8")
    written_files.append(key_file)

    oversized_parts: list[tuple[str, int]] = []
    for idx, chunk in enumerate(chunks, start=1):
        secret_name = f"WA_SESSION_ENC_PART{idx:03d}"
        part_file = output_dir / f"{secret_name}.txt"
        part_file.write_text(chunk, encoding="utf-8")
        written_files.append(part_file)
        byte_len = len(chunk.encode("utf-8"))
        if byte_len > GITHUB_SECRET_LIMIT_BYTES:
            oversized_parts.append((secret_name, byte_len))

    print("\nFetching joined WhatsApp groups...")
    groups_output = ""
    try:
        shutil.copy2(compact_enc, groups_tmp_enc)
        res = run_notifier(["groups", "--session-enc", str(groups_tmp_enc)], env=env, capture=True)
        groups_output = res.stdout.strip()
    except subprocess.CalledProcessError as exc:
        print(f"Warning: could not fetch joined groups ({exc.stderr.strip() or exc}).")
    finally:
        if groups_tmp_enc.exists():
            groups_tmp_enc.unlink()

    if groups_output:
        groups_file = output_dir / "joined_groups.txt"
        groups_file.write_text(groups_output + "\n", encoding="utf-8")
        written_files.append(groups_file)

        groups = parse_groups(groups_output)
        if groups:
            print("\nJoined WhatsApp groups:")
            for idx, (jid, members, name) in enumerate(groups, start=1):
                print(f"  [{idx}] {name}  |  {jid}  ({members})")
            print()

            for secret_name, keyword in (
                ("WA_GROUP_JID_SAVYA", "savya"),
                ("WA_GROUP_JID_NAVRACHANA", "navrachana"),
            ):
                chosen_jid = prompt_for_group(secret_name, keyword, groups, args.non_interactive)
                if chosen_jid:
                    jid_file = output_dir / f"{secret_name}.txt"
                    jid_file.write_text(chosen_jid, encoding="utf-8")
                    written_files.append(jid_file)
        else:
            print(groups_output)

    print(f"\nDone! Secret files written to: {output_dir}")
    for path in written_files:
        print(f"  - {path.name} ({path.stat().st_size} bytes)")

    if oversized_parts:
        print("\nWarning: the following parts exceed GitHub's 48 KB secret limit:", file=sys.stderr)
        for name, size in oversized_parts:
            print(f"  - {name}: {size} bytes", file=sys.stderr)
        print("Re-run with a larger --parts value and add those parts to the workflows.", file=sys.stderr)

    print("\nCopy the contents of each .txt file into GitHub -> Settings -> Secrets and variables -> Actions.")
    print("Do NOT commit the secrets/ folder.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
