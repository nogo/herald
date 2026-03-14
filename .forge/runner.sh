#!/usr/bin/env bash
# Runner: picks up work units from .forge/wu/, executes them in order, moves done to .forge/done/
# Polls for new WUs when queue is empty. Ctrl-C to stop gracefully.
# Usage: ./runner.sh [model] [poll_interval]
# Example: ./runner.sh
#          ./runner.sh opus 10
set -euo pipefail

# Append-log all output (stdout + stderr) while still printing to terminal
FORGE_DIR="$(cd "$(dirname "$0")" && pwd)"
LOG_FILE="$FORGE_DIR/runner.log"
exec > >(tee -a "$LOG_FILE") 2>&1

command -v jq >/dev/null 2>&1 || { echo "error: jq is required"; exit 1; }

WU_DIR="$FORGE_DIR/wu"
DONE_DIR="$FORGE_DIR/done"
PROJECT_DIR="$(cd "$FORGE_DIR/.." && pwd)"
MODEL="${1:-sonnet}"
POLL_INTERVAL="${2:-5}"

RUNNING=true
trap 'RUNNING=false; echo ""; echo "  ⏹ Ctrl-C received, finishing current work..."; ' INT

mkdir -p "$DONE_DIR"

echo "forge runner"
echo "project: ${PROJECT_DIR}"
echo "queue:   ${WU_DIR}"
echo "model:   ${MODEL}"
echo "poll:    ${POLL_INTERVAL}s"
echo "date:    $(date '+%Y-%m-%d %H:%M:%S')"
echo ""
echo "  Ctrl-C to stop gracefully"

run_wu() {
    local WU_FILE="$1"
    local WU_NAME
    WU_NAME=$(basename "$WU_FILE" .md)
    local WU_LOG_FILE="$WU_DIR/${WU_NAME}.log"
    local RAW_FILE="$WU_DIR/${WU_NAME}.jsonl"
    local TITLE
    TITLE=$(head -1 "$WU_FILE" | sed 's/^# //')

    echo ""
    echo "${WU_NAME}: ${TITLE}"
    echo "model: ${MODEL}"
    echo ""
    echo "  Prompt:  $(wc -l < "$WU_FILE") lines  ($(wc -c < "$WU_FILE" | tr -d ' ') bytes)"
    echo "  Log:     $WU_LOG_FILE"
    echo ""

    local START_TS
    START_TS=$(date +%s)
    echo "  Started: $(date '+%Y-%m-%d %H:%M:%S')"
    echo ""

    # Run claude
    local EXIT_CODE=0
    env -u CLAUDECODE claude --print \
        --model "$MODEL" \
        --output-format stream-json \
        --verbose \
        --dangerously-skip-permissions \
        -p "$(cat "$WU_FILE")" \
        2>"$WU_DIR/${WU_NAME}.stderr.log" \
        | tee "$RAW_FILE" \
        | while IFS= read -r line; do
            TYPE=$(echo "$line" | jq -r '.type // empty' 2>/dev/null) || continue
            NOW=$(date '+%H:%M:%S')

            case "$TYPE" in
                assistant)
                    TOOLS=$(echo "$line" | jq -r '
                        .message.content[]? |
                        select(.type=="tool_use") |
                        .name + " " + (.input | tostring | .[0:120])
                    ' 2>/dev/null)
                    if [[ -n "$TOOLS" ]]; then
                        while IFS= read -r tool_line; do
                            TOOL_NAME="${tool_line%% *}"
                            TOOL_ARGS="${tool_line#* }"
                            if [[ ${#TOOL_ARGS} -gt 80 ]]; then
                                TOOL_ARGS="${TOOL_ARGS:0:80}…"
                            fi
                            echo "  [$NOW]  ⚙  $TOOL_NAME  $TOOL_ARGS"
                        done <<< "$TOOLS"
                    fi
                    TEXT=$(echo "$line" | jq -r '
                        [.message.content[]? | select(.type=="text") | .text] | join("") | .[0:200]
                    ' 2>/dev/null)
                    if [[ -n "$TEXT" && "$TEXT" != "null" ]]; then
                        echo "  [$NOW]  💬 ${TEXT:0:120}"
                    fi
                    ;;
                result)
                    COST_IN=$(echo "$line" | jq -r '.usage.input_tokens // "?"' 2>/dev/null)
                    COST_OUT=$(echo "$line" | jq -r '.usage.output_tokens // "?"' 2>/dev/null)
                    TOTAL_COST=$(echo "$line" | jq -r '.total_cost_usd // "?"' 2>/dev/null)
                    echo ""
                    echo "  [$NOW]  ✅ Done — tokens: ${COST_IN} in / ${COST_OUT} out | cost: \$${TOTAL_COST}"
                    echo "$line" | jq -r '.result // empty' 2>/dev/null > "$WU_LOG_FILE"
                    ;;
            esac
        done || EXIT_CODE=$?

    # Build readable log from raw JSONL if log is empty
    if [[ ! -s "$WU_LOG_FILE" ]] && [[ -s "$RAW_FILE" ]]; then
        jq -r 'select(.type=="result") | .result // empty' "$RAW_FILE" > "$WU_LOG_FILE" 2>/dev/null || \
        cp "$RAW_FILE" "$WU_LOG_FILE"
    fi

    local END_TS
    END_TS=$(date +%s)
    local ELAPSED
    ELAPSED=$(( END_TS - START_TS ))
    local MINS=$(( ELAPSED / 60 ))
    local SECS=$(( ELAPSED % 60 ))

    echo ""
    echo "  ─────────────────────────────────────────────────────"
    echo "  Finished: $(date '+%Y-%m-%d %H:%M:%S')  (${MINS}m ${SECS}s)"

    if [[ $EXIT_CODE -ne 0 ]]; then
        echo "  Status:   ✗ FAILED (exit $EXIT_CODE)"
        echo "  ─────────────────────────────────────────────────────"
        echo ""
        return $EXIT_CODE
    fi

    echo "  Status:   ✓ OK"

    # Commit changes
    (cd "$PROJECT_DIR" && \
        if [[ -n "$(git status --porcelain 2>/dev/null)" ]]; then
            git add -A
            DIFF_STAT=$(git diff --cached --stat | tail -1)
            git commit -q -m "${WU_NAME}: ${TITLE}

${DIFF_STAT}
Model: ${MODEL} | Duration: ${MINS}m ${SECS}s"
            COMMIT_HASH=$(git rev-parse --short HEAD)
            echo "  Commit:   ✓ ${COMMIT_HASH}  ${WU_NAME}: ${TITLE}"
            echo "  Changes:  ${DIFF_STAT}"
        else
            echo "  Commit:   – no changes to commit"
        fi
    )

    # Move WU + artifacts to done
    local STDERR_FILE="$WU_DIR/${WU_NAME}.stderr.log"
    mv "$WU_FILE" "$DONE_DIR/"
    [[ -f "$WU_LOG_FILE" ]] && mv "$WU_LOG_FILE" "$DONE_DIR/"
    [[ -f "$RAW_FILE" ]] && mv "$RAW_FILE" "$DONE_DIR/"
    [[ -f "$STDERR_FILE" ]] && mv "$STDERR_FILE" "$DONE_DIR/"
    echo "  Moved:    → .forge/done/${WU_NAME}.md"

    echo "  ─────────────────────────────────────────────────────"
    echo ""
}

# Main loop: pick up WU files, poll when empty
PROCESSED=0
FAILED=0

while $RUNNING; do
    # Find next WU file (sorted by name)
    NEXT=$(find "$WU_DIR" -maxdepth 1 -name 'wu-*.md' -type f | sort -V | head -1)

    if [[ -z "$NEXT" ]]; then
        # No work — poll
        if [[ -t 1 ]]; then
            printf "\r  ⏳ Waiting for work units... ($(date '+%H:%M:%S'))"
        else
            echo "  ⏳ Waiting for work units... ($(date '+%H:%M:%S'))"
        fi
        sleep "$POLL_INTERVAL"
        continue
    fi

    # Clear the waiting line (terminal only)
    [[ -t 1 ]] && printf "\r%-60s\r" ""

    if run_wu "$NEXT"; then
        PROCESSED=$((PROCESSED + 1))
    else
        FAILED=$((FAILED + 1))
        echo "  ⚠ ${NEXT} failed — pausing. Fix and restart, or Ctrl-C to stop."
        # Wait for Ctrl-C or manual resolution (remove the failed WU file to resume)
        while $RUNNING; do
            sleep "$POLL_INTERVAL"
            if [[ ! -f "$NEXT" ]]; then
                echo "  ↻ Failed WU removed, resuming..."
                break
            fi
        done
    fi
done

echo ""
echo "  ═══════════════════════════════════════════════════════"
echo "  Runner stopped: ${PROCESSED} done, ${FAILED} failed, $(find "$WU_DIR" -maxdepth 1 -name 'wu-*.md' -type f 2>/dev/null | wc -l) remaining"
echo "  ═══════════════════════════════════════════════════════"
echo ""
