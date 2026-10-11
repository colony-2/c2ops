#!/usr/bin/env python3
"""Fast-forward published to a verified main commit; skip obsolete candidates."""

import argparse
import subprocess


def git(*args, check=True):
    return subprocess.run(["git", *args], check=check, text=True, capture_output=True)


def ancestor(older, newer):
    result = git("merge-base", "--is-ancestor", older, newer, check=False)
    if result.returncode not in (0, 1):
        raise RuntimeError(result.stderr)
    return result.returncode == 0


def published():
    result = git("ls-remote", "--exit-code", "origin", "refs/heads/published", check=False)
    if result.returncode == 2:
        return None
    if result.returncode:
        raise RuntimeError(result.stderr)
    git("fetch", "origin", "refs/heads/published")
    return git("rev-parse", "FETCH_HEAD").stdout.strip()


def promote(candidate):
    candidate = git("rev-parse", "--verify", f"{candidate}^{{commit}}").stdout.strip()
    git("fetch", "origin", "refs/heads/main:refs/remotes/origin/main")
    if not ancestor(candidate, "refs/remotes/origin/main"):
        raise ValueError("candidate is not in main's history")
    current = published()
    if current and ancestor(candidate, current):
        print("published already contains this candidate; nothing to promote")
        return
    if current and not ancestor(current, candidate):
        raise ValueError("published and candidate have diverged; refusing to force an update")
    # Git enforces the fast-forward condition on the server, including races.
    result = git("push", "origin", f"{candidate}:refs/heads/published", check=False)
    if result.returncode:
        current = published()
        if current and ancestor(candidate, current):
            print("a newer candidate won promotion; leaving published unchanged")
            return
        raise RuntimeError(result.stderr)
    print(f"published now points to {candidate}")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("candidate")
    promote(parser.parse_args().candidate)
