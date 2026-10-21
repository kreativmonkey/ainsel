#!/usr/bin/env python3
"""Golden-file tests for controller-conditional ingress rendering (issue #95).

Renders the chart with helm for an ingress-nginx class and for a non-nginx
class, extracts the Ingress objects, and compares them against checked-in
golden files. Also asserts the security-relevant invariants directly:

* nginx renders keep the regex paths and nginx.ingress.kubernetes.io/*
  rewrite annotations (backwards compatibility).
* non-nginx renders contain no nginx annotations, no regex paths, and the
  hub backend publishes only <hub.ingress.path>/v1 with pathType Prefix so
  /api/internal/* (authenticated by the cluster-wide internal token) stays
  unreachable from outside the cluster.

Run with --update after intentional template changes to regenerate the
goldens, then review the diff.
"""

import argparse
import os
import re
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
CHART = ROOT / "chart"
GOLDEN_DIR = CHART / "tests" / "golden"
HELM = os.environ.get("HELM", "helm")

# mcp is disabled by default; enable it so all five ingress templates render.
BASE_SETS = [
    "mcp.enabled=true",
    "mcp.ingress.host=mcp.example.com",
    "auth.oidcIssuer=https://id.example.com",
    "auth.oidcClientId=ainsel-test",
]
NON_NGINX_SETS = BASE_SETS + [
    "hub.ingress.className=traefik",
    "ui.ingress.className=traefik",
    "mcp.ingress.className=traefik",
]

SCENARIOS = {
    "ingress-nginx.yaml": BASE_SETS,
    "ingress-nonnginx.yaml": NON_NGINX_SETS,
}


def render(value_sets):
    cmd = [HELM, "template", "ainsel", str(CHART)]
    for value_set in value_sets:
        cmd += ["--set", value_set]
    result = subprocess.run(cmd, capture_output=True, text=True)
    if result.returncode != 0:
        sys.exit(f"helm template failed for {value_sets}:\n{result.stderr}")
    return result.stdout


def extract_ingresses(manifests):
    """Return all Ingress documents, source-comment stripped, sorted by name."""
    docs = []
    for doc in re.split(r"(?m)^---$", manifests):
        if not re.search(r"(?m)^kind: Ingress$", doc):
            continue
        lines = [
            line
            for line in doc.splitlines()
            # Drop source headers and the version-dependent chart label so
            # the goldens stay stable across chart version bumps.
            if not line.lstrip().startswith(("#", "helm.sh/chart:"))
        ]
        body = "\n".join(lines).strip()
        if body:
            docs.append(body)
    docs.sort()
    return "\n---\n".join(docs) + "\n"


def ingress_paths(manifests):
    """Return (path, pathType) pairs of every rendered ingress rule path."""
    pairs = []
    for doc in re.split(r"(?m)^---$", manifests):
        if not re.search(r"(?m)^kind: Ingress$", doc):
            continue
        current = None
        for line in doc.splitlines():
            match = re.match(r"\s*-\s+path:\s*(\S+)", line)
            if match:
                current = [match.group(1), None]
                pairs.append(current)
            match = re.match(r"\s*pathType:\s*(\S+)", line)
            if match and current is not None:
                current[1] = match.group(1)
                current = None
    return pairs


def ingress_named(manifests, name):
    for doc in re.split(r"(?m)^---$", manifests):
        if re.search(r"(?m)^kind: Ingress$", doc) and re.search(
            rf"(?m)^  name: {re.escape(name)}$", doc
        ):
            return doc
    return ""


def check_invariants(name, manifests):
    paths = ingress_paths(manifests)
    all_paths = [p for p, _ in paths]

    if name == "ingress-nginx.yaml":
        assert "nginx.ingress.kubernetes.io/rewrite-target" in manifests, (
            "nginx render lost its rewrite-target annotations"
        )
        assert "nginx.ingress.kubernetes.io/use-regex" in manifests, (
            "nginx render lost its use-regex annotations"
        )
        assert any("(/|$)" in p for p in all_paths), (
            "nginx render lost its regex paths"
        )
    else:
        assert "nginx.ingress.kubernetes.io/" not in manifests, (
            "non-nginx render still carries nginx annotations"
        )
        assert not any("(/|$)" in p for p in all_paths), (
            "non-nginx render still contains nginx regex paths"
        )
        assert not any("internal" in p for p in all_paths), (
            "no ingress path may publish /api/internal/*"
        )
        hub = ingress_named(manifests, "hub-backend")
        hub_paths = [tuple(pair) for pair in ingress_paths(hub)]
        assert hub_paths == [("/ainsel/api/v1", "Prefix")], (
            "non-nginx hub-backend must publish exactly /ainsel/api/v1 with "
            f"pathType Prefix, got {hub_paths}"
        )
        for _, path_type in paths:
            assert path_type in ("Prefix", "Exact"), (
                f"non-nginx render uses pathType {path_type!r}; "
                "ImplementationSpecific implies controller-specific semantics"
            )


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument(
        "--update",
        action="store_true",
        help="regenerate golden files instead of comparing",
    )
    args = parser.parse_args()

    GOLDEN_DIR.mkdir(parents=True, exist_ok=True)
    failures = []
    for name, value_sets in SCENARIOS.items():
        rendered = extract_ingresses(render(value_sets))
        check_invariants(name, rendered)
        golden = GOLDEN_DIR / name
        if args.update:
            golden.write_text(rendered)
            print(f"updated {golden.relative_to(ROOT)}")
        elif not golden.exists():
            failures.append(f"{name}: golden file missing (run with --update)")
        elif golden.read_text() != rendered:
            failures.append(
                f"{name}: rendered ingresses differ from golden file "
                f"(if intentional, run scripts/test-chart-ingress.py --update "
                f"and review the diff)"
            )
        else:
            print(f"ok {name}")

    if failures:
        for failure in failures:
            print(f"FAIL {failure}", file=sys.stderr)
        sys.exit(1)


if __name__ == "__main__":
    main()
