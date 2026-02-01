#!/bin/bash
# Claude Code PostToolUse hook for roborev integration
# Triggers after git commit commands to remind the agent to check reviews

set -euo pipefail
INPUT=$(cat)

# Only trigger on git commit (match command field specifically, not full JSON)
echo "$INPUT" | jq -r '.tool_input.command // ""' | grep -q "git commit" || exit 0

# Check prerequisites
command -v roborev &>/dev/null || exit 0
git rev-parse --git-dir &>/dev/null || exit 0
roborev status &>/dev/null || exit 0

COMMIT_SHA=$(git rev-parse HEAD)
COMMIT_SHORT=$(git rev-parse --short HEAD)

# stderr + exit 2 = Claude sees this message
echo "**ROBOREV** — Review queued for ${COMMIT_SHORT}. Run \`sleep 25 && roborev show ${COMMIT_SHA}\`, note job ID, then \`roborev address <id>\`." >&2
exit 2
