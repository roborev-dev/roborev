#!/bin/bash
# Generate a changelog since the last release using codex
# Usage: ./scripts/changelog.sh [version]
# If version is not provided, uses "NEXT" as placeholder

set -e

VERSION="${1:-NEXT}"

# Find the previous tag
PREV_TAG=$(git describe --tags --abbrev=0 2>/dev/null || echo "")
if [ -z "$PREV_TAG" ]; then
    # No previous tag, use first commit
    FIRST_COMMIT=$(git rev-list --max-parents=0 HEAD)
    RANGE="$FIRST_COMMIT..HEAD"
    echo "No previous release found. Generating changelog for all commits..." >&2
else
    RANGE="$PREV_TAG..HEAD"
    echo "Generating changelog from $PREV_TAG to HEAD..." >&2
fi

# Get commit log for changelog generation
COMMITS=$(git log $RANGE --pretty=format:"- %s (%h)" --no-merges)
DIFF_STAT=$(git diff --stat $RANGE)

if [ -z "$COMMITS" ]; then
    echo "No commits since $PREV_TAG" >&2
    exit 0
fi

# Use codex to generate the changelog
echo "Using codex to generate changelog..." >&2

TMPFILE=$(mktemp)
trap "rm -f $TMPFILE" EXIT

codex exec --skip-git-repo-check -o "$TMPFILE" - >/dev/null <<EOF
You are generating a changelog for roborev version $VERSION.

Here are the commits since the last release:
$COMMITS

Here's the diff summary:
$DIFF_STAT

Please generate a concise, user-focused changelog. Group changes into sections like:
- New Features
- Improvements
- Bug Fixes

Focus on user-visible changes. Skip internal refactoring unless it affects users.
Keep descriptions brief (one line each). Use present tense.
Do NOT mention bugs that were introduced and fixed within this same release cycle.
Output ONLY the changelog content, no preamble.
EOF

cat "$TMPFILE"
