#!/usr/bin/env python3
"""Generate the committed third-party license manifest (THIRD_PARTY_LICENSES.md)
and the LICENSES/ text directory for the ainsel monorepo.

Covered distribution surfaces:
  * Node.js pnpm workspace (frontend, docs-site) - via `pnpm licenses list --json --prod`
  * pi runtime extensions (pi/pi-extensions/*)   - via their committed package-lock.json
  * Go modules (every module in go.work)         - via `go list -deps` + the module cache

Why not google/go-licenses: v1.6.0 (the newest release) cannot load modules whose
go.mod declares go >= 1.25 (it errors on every std package; see
https://github.com/google/go-licenses/issues/128), and this repo's modules require
go 1.26.1. The Go section therefore uses `go list -deps` plus a small local
license-text classifier instead of an external binary.

Output is deterministic: sorted by name@version, no timestamps, license texts
copied verbatim. CI (.github/workflows/ci-licenses.yml) re-runs this script and
fails on any diff, so the committed manifest never drifts from the lockfiles.

Regenerate locally:
    pnpm install --frozen-lockfile
    python3 scripts/generate-third-party-licenses.py

Exit code 0 on success. Files are rewritten in place; run `git diff` afterwards.
"""

from __future__ import annotations

import argparse
import json
import re
import shutil
import subprocess
import sys
import tempfile
from dataclasses import dataclass
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent

# Own module prefix - excluded from the Go listing (our code, covered by NOTICE).
OWN_GO_PREFIX = "github.com/ainsel-project/ainsel"

# License-family key reported by `pnpm licenses list` for dual-licensed packages,
# mapped to the license this project elects (recorded in NOTICE).
LICENSE_ELECTIONS = {
    "dompurify": "Apache-2.0",
}

# Matches license/notice text files shipped inside packages and modules.
# Deliberately excludes README* (too noisy); includes LICENSE-MPL, licence.txt, etc.
LICENSE_FILE_RE = re.compile(r"(?i)^((UN)?LICEN[SC]E|COPYING|NOTICE)([.-].*)?$")

# Ordered license-text classifier: first distinctive match wins. Each entry is
# (SPDX id, regex applied to whitespace-normalized text).
CLASSIFY_PATTERNS: list[tuple[str, str]] = [
    ("Apache-2.0", r"Apache License,? Version 2\.0"),
    ("MPL-2.0", r"Mozilla Public License[, ]+Version 2\.0"),
    ("MPL-1.1", r"Mozilla Public License,? version 1\.1"),
    ("EPL-2.0", r"Eclipse Public License\s*[-–—]?\s*(v|version)?\s*2\.0"),
    ("ISC", r"Permission to use, copy, modify, and/or distribute this software for any purpose with or without fee is hereby granted"),
    ("Unlicense", r"free and unencumbered software released into the public domain"),
    ("Zlib", r"originally written by (Jean-loup Gailly|Mark Adler)|zlib\.libpng"),
    ("LGPL-2.1", r"GNU LESSER GENERAL PUBLIC LICENSE\s+Version 2\.1"),
    ("LGPL-3.0", r"GNU LESSER GENERAL PUBLIC LICENSE\s+Version 3"),
    ("GPL-2.0", r"GNU GENERAL PUBLIC LICENSE\s+Version 2"),
    ("GPL-3.0", r"GNU GENERAL PUBLIC LICENSE\s+Version 3"),
    ("AGPL-3.0", r"GNU AFFERO GENERAL PUBLIC LICENSE"),
    ("MIT", r"Permission is hereby granted, free of charge"),
    ("BSD-3-Clause", r"Neither the name"),
    ("BSD-2-Clause", r"Redistributions of source code"),
]


def classify_license_text(text: str) -> str:
    """Best-effort SPDX id for a license text. Returns 'Unknown' if no pattern hits."""
    normalized = re.sub(r"\s+", " ", text)
    for spdx, pattern in CLASSIFY_PATTERNS:
        if re.search(pattern, normalized, re.IGNORECASE):
            return spdx
    return "Unknown"


def sanitize_dir_name(name: str) -> str:
    """Make a package/module name safe for a LICENSES/ directory name.

    Mirrors the pnpm store convention: path separators become '+'.
    """
    return name.replace("/", "+")


def dep_dir(name: str, version: str) -> str:
    return f"{sanitize_dir_name(name)}@{version}"


@dataclass(frozen=True)
class LicenseText:
    """One license/notice text file copied verbatim into LICENSES/<dep_dir>/.

    The text is held in memory (not as a path) because sources may live in
    temp directories that are gone by the time write_outputs() runs.
    """

    filename: str
    data: bytes
    spdx: str | None  # classification for license files; None for NOTICE files
    shared: bool = False  # True: lives at LICENSES/<filename> (canonical family text)


@dataclass(frozen=True)
class Dep:
    ecosystem: str  # section key: "node", "pi", "go"
    name: str
    version: str
    license: str  # SPDX expression as declared, or classified, or "Unknown"
    dir_name: str  # LICENSES/<dir_name>
    texts: tuple[LicenseText, ...]

    @property
    def sort_key(self) -> tuple[str, str]:
        return (self.name.lower(), self.version)


def find_license_files(pkg_dir: Path) -> list[Path]:
    """License/notice files at the top level of a package or module directory."""
    if not pkg_dir.is_dir():
        return []
    return sorted(
        f for f in pkg_dir.iterdir()
        if f.is_file() and LICENSE_FILE_RE.match(f.name)
    )


def license_texts_for(pkg_dir: Path) -> tuple[LicenseText, ...]:
    texts = []
    for f in find_license_files(pkg_dir):
        try:
            data = f.read_bytes()
        except OSError:
            continue
        spdx = None
        if not f.name.upper().startswith("NOTICE"):
            spdx = classify_license_text(data.decode("utf-8", errors="replace"))
        texts.append(LicenseText(filename=f.name, data=data, spdx=spdx))
    return tuple(texts)


def declared_license_to_spdx(license_field: object) -> str | None:
    """Normalize a package.json 'license' field (string | {type} | list) to an SPDX string."""
    if license_field is None:
        return None
    if isinstance(license_field, dict):
        license_field = license_field.get("type")
    if isinstance(license_field, list):
        parts = []
        for item in license_field:
            part = declared_license_to_spdx(item)
            if part:
                parts.append(part)
        return " OR ".join(parts) if parts else None
    value = str(license_field).strip()
    return value or None


# ---------------------------------------------------------------------------
# Stage 1: pnpm workspace (frontend, docs-site)
# ---------------------------------------------------------------------------

def parse_pnpm_licenses_json(raw: str) -> list[dict]:
    """Parse concatenated JSON documents emitted by `pnpm licenses list --json`."""
    decoder = json.JSONDecoder()
    docs: list[dict] = []
    idx = 0
    while idx < len(raw):
        while idx < len(raw) and raw[idx] in " \r\n\t":
            idx += 1
        if idx >= len(raw):
            break
        obj, idx = decoder.raw_decode(raw, idx)
        docs.append(obj)
    return docs


def collect_pnpm_workspace(repo_root: Path) -> list[Dep]:
    """Deps of every pnpm workspace project (frontend, docs-site), prod only."""
    proc = subprocess.run(
        ["pnpm", "-r", "licenses", "list", "--json", "--prod"],
        cwd=repo_root, capture_output=True, text=True, check=True,
    )
    deps: list[Dep] = []
    for doc in parse_pnpm_licenses_json(proc.stdout):
        for family, packages in doc.items():
            for pkg in packages:
                name = pkg["name"]
                version = pkg["versions"][0]
                pkg_dir = Path(pkg["paths"][0]) if pkg.get("paths") else None
                texts = license_texts_for(pkg_dir) if pkg_dir else ()
                license_str = family
                if family == "Unknown" and texts:
                    classified = [t.spdx for t in texts if t.spdx and t.spdx != "Unknown"]
                    if classified:
                        license_str = " + ".join(sorted(set(classified)))
                if name in LICENSE_ELECTIONS:
                    license_str = f"{license_str} — {LICENSE_ELECTIONS[name]} elected (see NOTICE)"
                deps.append(Dep("node", name, version, license_str, dep_dir(name, version), texts))
    return deps


# ---------------------------------------------------------------------------
# Stage 2: pi runtime extensions (pi/pi-extensions/*)
# ---------------------------------------------------------------------------

# Canonical texts for declared licenses of packages that ship no license file
# (e.g. @types/* stubs, esbuild platform binaries). Keyed by SPDX id.
CANONICAL_LICENSE_TEXTS: dict[str, bytes] = {
    "MIT": (
        b"MIT License\n"
        b"\n"
        b"Copyright (c) the respective authors of the packages listed in\n"
        b"THIRD_PARTY_LICENSES.md as using this license.\n"
        b"\n"
        b'Permission is hereby granted, free of charge, to any person obtaining a copy\n'
        b"of this software and associated documentation files (the \"Software\"), to deal\n"
        b"in the Software without restriction, including without limitation the rights\n"
        b"to use, copy, modify, merge, publish, distribute, sublicense, and/or sell\n"
        b"copies of the Software, and to permit persons to whom the Software is\n"
        b"furnished to do so, subject to the following conditions:\n"
        b"\n"
        b"The above copyright notice and this permission notice shall be included in all\n"
        b"copies or substantial portions of the Software.\n"
        b"\n"
        b'THE SOFTWARE IS PROVIDED \"AS IS\", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR\n'
        b"IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,\n"
        b"FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE\n"
        b"AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER\n"
        b"LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,\n"
        b"OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE\n"
        b"SOFTWARE.\n"
    ),
}


def canonical_license_text(spdx: str, repo_root: Path) -> bytes | None:
    """Canonical text for a license family, or None if not available."""
    if spdx == "Apache-2.0":
        # The repo's own LICENSE is the canonical Apache-2.0 text.
        try:
            return (repo_root / "LICENSE").read_bytes()
        except OSError:
            return None
    return CANONICAL_LICENSE_TEXTS.get(spdx)


def apply_canonical_fallbacks(deps: list[Dep], repo_root: Path) -> list[Dep]:
    """Point deps that ship no license file at the canonical family text.

    The declared license (npm lockfile / pnpm metadata) is authoritative for
    the family; the canonical text makes the license of every shipped package
    available under LICENSES/.
    """
    out: list[Dep] = []
    for dep in deps:
        if not dep.texts and dep.license in ("MIT", "Apache-2.0"):
            data = canonical_license_text(dep.license, repo_root)
            if data is not None:
                dep = Dep(
                    dep.ecosystem, dep.name, dep.version, dep.license, dep.dir_name,
                    (LicenseText(f"{dep.license}.txt", data, dep.license, shared=True),),
                )
        out.append(dep)
    return out


def parse_npm_lockfile(lock: dict) -> list[dict]:
    """Extract {name, version, license} rows from an npm lockfileVersion 3 lock.

    Skips the root package entry ('') - the extension itself - and dev-only
    entries, which are not installed by `npm ci --omit=dev` and do not ship in
    the pi image.
    """
    rows = []
    for path, entry in lock.get("packages", {}).items():
        if path == "":
            continue
        if entry.get("dev") is True:
            continue
        name = entry.get("name") or path.split("node_modules/")[-1]
        version = entry.get("version")
        if not version:
            continue
        rows.append({"name": name, "version": version, "license": entry.get("license")})
    return rows


def collect_pi_extensions(repo_root: Path, *, package_dirs: list[Path] | None = None,
                          ecosystem: str = "pi") -> list[Dep]:
    """Collect locked runtime packages (Pi extensions by default).

    The lockfile pins name/version/license deterministically; the actual license
    texts are obtained from an `npm ci --omit=dev` install into a temp dir.
    """
    deps: list[Dep] = []
    extensions_root = repo_root / "pi" / "pi-extensions"
    if package_dirs is None:
        package_dirs = sorted(p for p in extensions_root.iterdir() if p.is_dir())
    for ext_dir in package_dirs:
        lock_path = ext_dir / "package-lock.json"
        pkg_path = ext_dir / "package.json"
        if not lock_path.is_file() or not pkg_path.is_file():
            raise SystemExit(
                f"error: {ext_dir.name} has no committed package-lock.json - "
                f"run `npm install --package-lock-only` in {ext_dir}"
            )
        lock = json.loads(lock_path.read_text(encoding="utf-8"))
        rows = parse_npm_lockfile(lock)
        with tempfile.TemporaryDirectory(prefix="ainsel-licenses-") as tmp:
            work = Path(tmp) / ext_dir.name
            work.mkdir()
            shutil.copy2(pkg_path, work / "package.json")
            shutil.copy2(lock_path, work / "package-lock.json")
            # Per-extension .npmrc (e.g. legacy-peer-deps in pi-auto-compact)
            # decides whether peers are installed - mirror the pi image's
            # `npm install --omit=dev` behavior exactly.
            npmrc = ext_dir / ".npmrc"
            if npmrc.is_file():
                shutil.copy2(npmrc, work / ".npmrc")
            subprocess.run(
                ["npm", "ci", "--omit=dev", "--ignore-scripts",
                 "--no-audit", "--no-fund", "--loglevel=error"],
                cwd=work, check=True,
            )
            installed = collect_npm_tree(work / "node_modules")
            for row in rows:
                key = (row["name"], row["version"])
                pkg_dir = installed.get(key)
                texts = license_texts_for(pkg_dir) if pkg_dir else ()
                # Platform packages ship the same Claude license as the wrapper.
                # Use it on every host so cross-platform regeneration is stable.
                if ecosystem == "subscription" and row["name"].startswith("@anthropic-ai/claude-code-"):
                    wrapper = installed.get(("@anthropic-ai/claude-code", row["version"]))
                    if wrapper:
                        texts = license_texts_for(wrapper)
                license_str = declared_license_to_spdx(row["license"]) or "Unknown"
                if license_str == "Unknown" and texts:
                    classified = [t.spdx for t in texts if t.spdx and t.spdx != "Unknown"]
                    if classified:
                        license_str = " + ".join(sorted(set(classified)))
                deps.append(Dep(ecosystem, row["name"], row["version"], license_str,
                                dep_dir(row["name"], row["version"]), texts))
    return deps


def collect_npm_tree(node_modules: Path) -> dict[tuple[str, str], Path]:
    """Map (name, version) -> package dir for a whole npm tree, incl. nested copies."""
    found: dict[tuple[str, str], Path] = {}

    def walk(root: Path) -> None:
        if not root.is_dir():
            return
        for entry in root.iterdir():
            if entry.name.startswith("@"):
                walk_scoped(entry)
            elif (entry / "package.json").is_file():
                register(entry)

    def walk_scoped(scope_dir: Path) -> None:
        for entry in scope_dir.iterdir():
            if (entry / "package.json").is_file():
                register(entry)

    def register(pkg_dir: Path) -> None:
        try:
            meta = json.loads((pkg_dir / "package.json").read_text(encoding="utf-8"))
        except (OSError, json.JSONDecodeError):
            return
        name, version = meta.get("name"), meta.get("version")
        if name and version:
            found.setdefault((name, version), pkg_dir)
        nested = pkg_dir / "node_modules"
        if nested.is_dir():
            walk(nested)

    walk(node_modules)
    return found


# ---------------------------------------------------------------------------
# Stage 3: Go modules (go.work)
# ---------------------------------------------------------------------------

def parse_go_work_use(text: str) -> list[str]:
    """Module dirs from a go.work `use (...)` block."""
    dirs: list[str] = []
    block = re.search(r"use\s*\(([^)]*)\)", text)
    if block:
        for line in block.group(1).splitlines():
            line = line.strip()
            if not line or line.startswith("//"):
                continue
            dirs.append(_normalize_use_dir(line))
    for m in re.finditer(r"use\s+([^\s(]+)", text):
        dirs.append(_normalize_use_dir(m.group(1)))
    return sorted(set(dirs))


def _normalize_use_dir(line: str) -> str:
    line = line.strip().strip("/")
    return line.removeprefix("./")


def parse_go_list_modules(output: str) -> list[tuple[str, str]]:
    """(module, version) pairs from `go list -deps -f '{{if .Module}}...'` output."""
    mods = set()
    for line in output.splitlines():
        line = line.strip()
        if not line:
            continue
        parts = line.split()
        if len(parts) != 2:
            continue  # std, or local module without version
        path, version = parts
        if path == "std" or path.startswith(OWN_GO_PREFIX):
            continue
        mods.add((path, version))
    return sorted(mods)


def go_module_cache_dir(gomodcache: Path, module: str, version: str) -> Path:
    """Module cache layout: uppercase letters become '!lower'."""
    escaped = re.sub(r"([A-Z])", lambda m: "!" + m.group(1).lower(), module)
    return gomodcache / f"{escaped}@{version}"


def collect_go_modules(repo_root: Path) -> list[Dep]:
    """Deps of every module in go.work, resolved via `go list -deps` + module cache."""
    go_work = repo_root / "go.work"
    module_dirs = parse_go_work_use(go_work.read_text(encoding="utf-8"))
    gomodcache = Path(subprocess.run(
        ["go", "env", "GOMODCACHE"], cwd=repo_root,
        capture_output=True, text=True, check=True,
    ).stdout.strip())

    pairs: set[tuple[str, str]] = set()
    for mod in module_dirs:
        mod_dir = repo_root / mod
        subprocess.run(["go", "mod", "download"], cwd=mod_dir, check=True)
        proc = subprocess.run(
            ["go", "list", "-deps", "-f",
             "{{if .Module}}{{.Module.Path}} {{.Module.Version}}{{end}}", "./..."],
            cwd=mod_dir, capture_output=True, text=True, check=True,
        )
        pairs.update(parse_go_list_modules(proc.stdout))

    deps: list[Dep] = []
    for module, version in sorted(pairs):
        mod_dir = go_module_cache_dir(gomodcache, module, version)
        texts = license_texts_for(mod_dir)
        classified = sorted({t.spdx for t in texts if t.spdx})
        if classified:
            license_str = " + ".join(classified)
        else:
            license_str = "Unknown"
        deps.append(Dep("go", module, version, license_str, dep_dir(module, version), texts))
    return deps


# ---------------------------------------------------------------------------
# Output
# ---------------------------------------------------------------------------

SECTIONS = [
    ("node", "Node.js - frontend and docs-site (pnpm workspace)"),
    ("pi", "Node.js - pi runtime extensions (pi/pi-extensions)"),
    ("subscription", "Native subscription runtime (runtimes/subscription)"),
    ("go", "Go modules (services, operators, shared)"),
]

BANNER = """# Third-Party Licenses

<!--
GENERATED FILE - DO NOT EDIT.
Generated by: python3 scripts/generate-third-party-licenses.py
Regenerate:   pnpm install --frozen-lockfile && python3 scripts/generate-third-party-licenses.py
CI check:     .github/workflows/ci-licenses.yml regenerates and fails on any diff.
-->

This file lists every third-party dependency statically bundled into ainsel's
distributed artifacts (frontend SPA bundle, Go service/operator images, pi
and subscription runtime images). Each row links to the license text as
shipped in `LICENSES/`.
Project-level attributions and license elections (including the DOMPurify
Apache-2.0 election) are recorded in [NOTICE](NOTICE).
"""


def render_markdown(deps: list[Dep]) -> str:
    lines = [BANNER]
    by_eco: dict[str, list[Dep]] = {}
    for dep in deps:
        by_eco.setdefault(dep.ecosystem, []).append(dep)
    for eco, title in SECTIONS:
        rows = sorted(by_eco.get(eco, []), key=lambda d: d.sort_key)
        lines.append(f"\n## {title}\n")
        lines.append(f"{len(rows)} packages.\n")
        lines.append("| Package | Version | License | License texts |")
        lines.append("| --- | --- | --- | --- |")
        for dep in rows:
            links = ", ".join(
                f"[{t.filename}](LICENSES/{t.filename})" if t.shared
                else f"[{t.filename}](LICENSES/{dep.dir_name}/{t.filename})"
                for t in dep.texts
            ) or "-"
            license_cell = dep.license.replace("|", "\\|")
            lines.append(f"| `{dep.name}` | {dep.version} | {license_cell} | {links} |")
    lines.append("")
    return "\n".join(lines)


def write_outputs(repo_root: Path, deps: list[Dep]) -> None:
    licenses_dir = repo_root / "LICENSES"
    licenses_dir.mkdir(exist_ok=True)

    keep = set()
    for dep in deps:
        keep.add(dep.dir_name)
        for text in dep.texts:
            if text.shared:
                (licenses_dir / text.filename).write_bytes(text.data)
                keep.add(text.filename)
        target = licenses_dir / dep.dir_name
        if target.exists():
            shutil.rmtree(target)
        if dep.texts:
            target.mkdir(parents=True)
            for text in dep.texts:
                (target / text.filename).write_bytes(text.data)
        elif target.is_dir():
            target.rmdir()

    # Prune stale entries so the committed tree matches exactly what was generated.
    for entry in licenses_dir.iterdir():
        if entry.name not in keep:
            if entry.is_dir():
                shutil.rmtree(entry)
            else:
                entry.unlink()

    (repo_root / "THIRD_PARTY_LICENSES.md").write_text(render_markdown(deps), encoding="utf-8")


def dedupe_within_ecosystem(deps: list[Dep]) -> list[Dep]:
    """Drop duplicate (ecosystem, name, version) rows - e.g. the same package
    pulled in as a peer by several pi extensions. Keeps one row per package."""
    seen: set[tuple[str, str, str]] = set()
    out: list[Dep] = []
    for dep in sorted(deps, key=lambda d: (d.ecosystem, d.sort_key)):
        key = (dep.ecosystem, dep.name, dep.version)
        if key in seen:
            continue
        seen.add(key)
        out.append(dep)
    return out


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--repo-root", type=Path, default=REPO_ROOT)
    args = parser.parse_args(argv)
    repo_root = args.repo_root.resolve()

    deps: list[Dep] = []
    deps.extend(collect_pnpm_workspace(repo_root))
    deps.extend(collect_pi_extensions(repo_root))
    deps.extend(collect_pi_extensions(
        repo_root, package_dirs=[repo_root / "runtimes" / "subscription"],
        ecosystem="subscription"))
    deps.extend(collect_go_modules(repo_root))
    deps = dedupe_within_ecosystem(deps)
    deps = apply_canonical_fallbacks(deps, repo_root)

    write_outputs(repo_root, deps)
    counts = {}
    for dep in deps:
        counts[dep.ecosystem] = counts.get(dep.ecosystem, 0) + 1
    print(f"THIRD_PARTY_LICENSES.md: " + ", ".join(
        f"{counts.get(eco, 0)} {eco}" for eco, _ in SECTIONS) + f" ({len(deps)} total)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
