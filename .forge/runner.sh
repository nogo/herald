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

VERIFY="$FORGE_DIR/verify"
CONTEXT_FILE="$FORGE_DIR/context.md"

mkdir -p "$DONE_DIR"

if [[ ! -x "$VERIFY" ]]; then
    echo "error: $VERIFY not found or not executable"
    echo "create a .forge/verify script that exits 0 when the project is green"
    exit 1
fi

echo "forge runner"
echo "project: ${PROJECT_DIR}"
echo "queue:   ${WU_DIR}"
echo "model:   ${MODEL}"
echo "verify:  ${VERIFY}"
echo "context: ${CONTEXT_FILE}"
echo "poll:    ${POLL_INTERVAL}s"
echo "date:    $(date '+%Y-%m-%d %H:%M:%S')"
echo ""

# Pre-flight: verify baseline is green before processing any WUs
echo "  Pre-flight verify..."
PREFLIGHT_OUTPUT=$("$VERIFY" 2>&1) || {
    echo "  Pre-flight: ✗ FAILED — project is not green before any WU ran"
    echo ""
    echo "$PREFLIGHT_OUTPUT" | tail -20
    echo ""
    echo "  Fix the baseline, then restart the runner."
    exit 1
}
echo "  Pre-flight: ✓ baseline green"
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

    # Build prompt: context prefix + WU body
    local PROMPT
    if [[ -f "$CONTEXT_FILE" ]]; then
        PROMPT="$(cat "$CONTEXT_FILE")

---

$(cat "$WU_FILE")"
    else
        PROMPT="$(cat "$WU_FILE")"
    fi

    # Run claude
    local EXIT_CODE=0
    env -u CLAUDECODE claude --print \
        --model "$MODEL" \
        --output-format stream-json \
        --verbose \
        --dangerously-skip-permissions \
        -p "$PROMPT" \
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

    # Run full test suite to catch cross-WU regressions
    echo ""
    echo "  Running full test suite..."
    local TEST_OUTPUT
    TEST_OUTPUT=$("$VERIFY" 2>&1) || {
        echo "  Tests:    ✗ FAILED"
        echo "$TEST_OUTPUT" | tail -20
        echo ""
        return 1
    }
    echo "  Tests:    ✓ all green"

    # Commit changes (exclude binaries and sensitive files)
    (cd "$PROJECT_DIR" && \
        if [[ -n "$(git status --porcelain 2>/dev/null)" ]]; then
            # Stage tracked file changes first (safe — only files already in git)
            git add --update
            # Stage new files from Creates list, but warn about unexpected untracked files
            UNTRACKED=$(git ls-files --others --exclude-standard 2>/dev/null || true)
            if [[ -n "$UNTRACKED" ]]; then
                # Filter out binaries and large files before adding
                while IFS= read -r f; do
                    case "$f" in
                        *.so|*.dylib|*.dll|*.exe|*.db|*.sqlite|*.env|*.key|*.pem)
                            echo "  ⚠ Skipping untracked file: $f"
                            ;;
                        *)
                            if [[ $(stat -c%s "$f" 2>/dev/null || stat -f%z "$f" 2>/dev/null || echo 0) -lt 1048576 ]]; then
                                git add "$f"
                            else
                                echo "  ⚠ Skipping large file (>1MB): $f"
                            fi
                            ;;
                    esac
                done <<< "$UNTRACKED"
            fi
            if [[ -n "$(git diff --cached --name-only 2>/dev/null)" ]]; then
                DIFF_STAT=$(git diff --cached --stat | tail -1)
                FILES_CHANGED=$(git diff --cached --name-only | head -20)
                git commit -q -m "$(cat <<EOF
${WU_NAME}: ${TITLE}

${DIFF_STAT}

Files: ${FILES_CHANGED}
Model: ${MODEL} | Duration: ${MINS}m ${SECS}s
EOF
)"
                COMMIT_HASH=$(git rev-parse --short HEAD)
                echo "  Commit:   ✓ ${COMMIT_HASH}  ${WU_NAME}: ${TITLE}"
                echo "  Changes:  ${DIFF_STAT}"
            else
                echo "  Commit:   – no changes to commit"
            fi
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

# Gate WU repair loop: on failure, generate a fix WU and retry (max 3 attempts)
run_gate() {
    local WU_FILE="$1"
    local WU_NAME
    WU_NAME=$(basename "$WU_FILE" .md)
    local MAX_RETRIES=3
    local ATTEMPT=0

    if run_wu "$WU_FILE"; then
        return 0
    fi

    echo "  ⚠ Gate ${WU_NAME} failed — entering repair loop"

    while [[ $ATTEMPT -lt $MAX_RETRIES ]] && $RUNNING; do
        ATTEMPT=$((ATTEMPT + 1))
        echo ""
        echo "  ──── Gate repair attempt ${ATTEMPT}/${MAX_RETRIES} ────"

        # Capture test failure output
        local TEST_FAILURE
        TEST_FAILURE=$("$VERIFY" 2>&1 || true)

        # Read original gate WU for context
        local GATE_CONTENT
        if [[ -f "$WU_FILE" ]]; then
            GATE_CONTENT=$(cat "$WU_FILE")
        else
            # Gate WU was moved to done dir on partial success
            GATE_CONTENT=$(cat "$DONE_DIR/${WU_NAME}.md" 2>/dev/null || echo "(gate WU not found)")
        fi

        local FIX_NAME="${WU_NAME}-fix-${ATTEMPT}"
        local FIX_LOG="$WU_DIR/${FIX_NAME}.log"
        local FIX_RAW="$WU_DIR/${FIX_NAME}.jsonl"
        local FIX_STDERR="$WU_DIR/${FIX_NAME}.stderr.log"

        echo "  Generating fix: ${FIX_NAME}"

        # Build repair prompt with architectural context
        local REPAIR_CONTEXT=""
        if [[ -f "$CONTEXT_FILE" ]]; then
            REPAIR_CONTEXT="$(cat "$CONTEXT_FILE")

---

"
        fi

        # Run claude to fix the failures
        local FIX_EXIT=0
        env -u CLAUDECODE claude --print \
            --model "$MODEL" \
            --output-format stream-json \
            --verbose \
            --dangerously-skip-permissions \
            -p "${REPAIR_CONTEXT}A gate work unit ran and produced test failures. Fix the source code so all tests pass. Do NOT modify the test expectations unless the tests themselves are clearly wrong — fix the implementation.

## Architectural constraints for repairs
- Do not change import relationships between packages.
- Do not modify files in \`core/\` unless the gate WU explicitly targets core.
- Do not weaken pass/fail criteria or delete assertions to make tests pass.
- Fix only the implementation, not the contracts.

## Original gate WU
${GATE_CONTENT}

## Test failure output
\`\`\`
${TEST_FAILURE}
\`\`\`

Fix the code so that \`.forge/verify\` passes (go build, go vet, go test -race, gofmt, boundary check). Do not add features. Do not refactor. Only fix what's broken." \
            2>"$FIX_STDERR" \
            | tee "$FIX_RAW" \
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
                                [[ ${#TOOL_ARGS} -gt 80 ]] && TOOL_ARGS="${TOOL_ARGS:0:80}…"
                                echo "  [$NOW]  ⚙  $TOOL_NAME  $TOOL_ARGS"
                            done <<< "$TOOLS"
                        fi
                        ;;
                    result)
                        COST_IN=$(echo "$line" | jq -r '.usage.input_tokens // "?"' 2>/dev/null)
                        COST_OUT=$(echo "$line" | jq -r '.usage.output_tokens // "?"' 2>/dev/null)
                        echo "  [$NOW]  ✅ Fix done — tokens: ${COST_IN} in / ${COST_OUT} out"
                        ;;
                esac
            done || FIX_EXIT=$?

        if [[ $FIX_EXIT -ne 0 ]]; then
            echo "  Fix ${FIX_NAME}: ✗ claude failed (exit $FIX_EXIT)"
            continue
        fi

        # Re-run tests
        echo "  Re-running tests..."
        local RETEST_OUTPUT
        RETEST_OUTPUT=$("$VERIFY" 2>&1) || {
            echo "  Tests:    ✗ still failing (attempt ${ATTEMPT}/${MAX_RETRIES})"
            echo "$RETEST_OUTPUT" | tail -10
            # Move fix artifacts to done
            [[ -f "$FIX_RAW" ]] && mv "$FIX_RAW" "$DONE_DIR/"
            [[ -f "$FIX_STDERR" ]] && mv "$FIX_STDERR" "$DONE_DIR/"
            [[ -f "$FIX_LOG" ]] && mv "$FIX_LOG" "$DONE_DIR/"
            continue
        }

        echo "  Tests:    ✓ all green after fix"

        # Commit the fix (tracked files only — repair should not create new files)
        (cd "$PROJECT_DIR" && \
            if [[ -n "$(git status --porcelain 2>/dev/null)" ]]; then
                git add --update
                if [[ -n "$(git diff --cached --name-only 2>/dev/null)" ]]; then
                    DIFF_STAT=$(git diff --cached --stat | tail -1)
                    FILES_CHANGED=$(git diff --cached --name-only | head -20)
                    git commit -q -m "$(cat <<EOF
${FIX_NAME}: Fix gate failures

${DIFF_STAT}

Files: ${FILES_CHANGED}
Model: ${MODEL}
EOF
)"
                    COMMIT_HASH=$(git rev-parse --short HEAD)
                    echo "  Commit:   ✓ ${COMMIT_HASH}  ${FIX_NAME}"
                fi
            fi
        )

        # Move fix artifacts to done
        [[ -f "$FIX_RAW" ]] && mv "$FIX_RAW" "$DONE_DIR/"
        [[ -f "$FIX_STDERR" ]] && mv "$FIX_STDERR" "$DONE_DIR/"
        [[ -f "$FIX_LOG" ]] && mv "$FIX_LOG" "$DONE_DIR/"

        echo "  ──── Gate repair succeeded ────"
        return 0
    done

    echo "  ✗ Gate repair exhausted (${MAX_RETRIES} attempts). Manual intervention needed."
    return 1
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

    # Detect gate WUs by title prefix [gate]
    IS_GATE=false
    if head -1 "$NEXT" | grep -qi '^# *\[gate\]'; then
        IS_GATE=true
    fi

    if $IS_GATE; then
        if run_gate "$NEXT"; then
            PROCESSED=$((PROCESSED + 1))
        else
            FAILED=$((FAILED + 1))
            echo "  ⚠ Gate ${NEXT} exhausted repair loop — pausing."
            while $RUNNING; do
                sleep "$POLL_INTERVAL"
                if [[ ! -f "$NEXT" ]]; then
                    echo "  ↻ Failed WU removed, resuming..."
                    break
                fi
            done
        fi
    elif run_wu "$NEXT"; then
        PROCESSED=$((PROCESSED + 1))
    else
        FAILED=$((FAILED + 1))
        echo "  ⚠ ${NEXT} failed — pausing. Fix and restart, or Ctrl-C to stop."
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
