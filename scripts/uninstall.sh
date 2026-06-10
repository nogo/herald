#!/bin/sh
set -eu

# Herald uninstall script
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/nogo/herald/main/uninstall.sh | sudo sh
#   curl -fsSL .../uninstall.sh | sudo sh -s -- --purge        # also delete data + user
#   curl -fsSL .../uninstall.sh | sudo sh -s -- --purge --yes  # skip confirmation
#
# Default: removes the systemd service and binary. Data, secrets, and config under
# /etc/herald and deployed stacks under /opt/deploy are PRESERVED.
# --purge: also removes the herald user, /etc/herald (incl. the age key), and /opt/deploy.

INSTALL_DIR="/usr/local/bin"
DATA_DIR="/etc/herald"
DEPLOY_DIR="/opt/deploy"
USER="herald"
UNIT_PATH="/etc/systemd/system/herald.service"

info() { printf '  \033[36m%s\033[0m\n' "$*"; }
ok()   { printf '  \033[32m✓\033[0m %s\n' "$*"; }
warn() { printf '  \033[33m!\033[0m %s\n' "$*"; }
die()  { printf '  \033[31m✗\033[0m %s\n' "$*" >&2; exit 1; }

# --- args ---

PURGE=false
ASSUME_YES=false
for arg in "$@"; do
    case "$arg" in
        --purge)   PURGE=true ;;
        --yes|-y)  ASSUME_YES=true ;;
        *)         die "Unknown argument: $arg (valid: --purge, --yes)" ;;
    esac
done

if [ "$(id -u)" -ne 0 ]; then
    die "This script must be run as root. Try: sudo sh uninstall.sh"
fi

# --- confirm purge up front, before removing anything ---

if [ "$PURGE" = true ] && [ "$ASSUME_YES" != true ]; then
    printf '\n'
    warn "--purge will permanently DELETE:"
    printf '      %s\n' "$UNIT_PATH       (systemd unit)"
    printf '      %s\n' "$INSTALL_DIR/herald    (binary)"
    printf '      %s\n' "$DATA_DIR              (age key + secrets — UNRECOVERABLE, config, webhook state)"
    printf '      %s\n' "$DEPLOY_DIR            (all deployed stack directories)"
    id "$USER" >/dev/null 2>&1 && printf '      %s\n' "system user '$USER'"
    printf '\n'
    warn "The age key cannot be recovered — encrypted secrets become permanently unreadable."
    printf '  Continue? [y/N] '
    if [ -e /dev/tty ]; then
        read confirm < /dev/tty || confirm=""
    else
        die "No terminal available to confirm. Re-run with: --purge --yes"
    fi
    case "$confirm" in
        y|Y|yes|YES) ;;
        *) die "Aborted. Nothing was removed." ;;
    esac
fi

# --- stop and remove the service ---

if command -v systemctl >/dev/null 2>&1; then
    systemctl disable --now herald 2>/dev/null || true
    ok "Service stopped and disabled"
fi

if [ -f "$UNIT_PATH" ]; then
    rm -f "$UNIT_PATH"
    command -v systemctl >/dev/null 2>&1 && systemctl daemon-reload || true
    ok "Removed $UNIT_PATH"
fi

# --- remove the binary ---

if [ -x "$INSTALL_DIR/herald" ]; then
    rm -f "$INSTALL_DIR/herald"
    ok "Removed $INSTALL_DIR/herald"
fi

# --- purge data + user (only with --purge) ---

if [ "$PURGE" = true ]; then
    rm -rf "$DATA_DIR" "$DEPLOY_DIR"
    ok "Removed $DATA_DIR and $DEPLOY_DIR"
    if id "$USER" >/dev/null 2>&1; then
        userdel "$USER" 2>/dev/null || true
        ok "Removed user '$USER'"
    fi
else
    info "Preserved: $DATA_DIR (age key, secrets, config) and $DEPLOY_DIR (stacks)"
    info "To remove everything: sudo sh uninstall.sh --purge"
fi

# --- Docker leftovers (this script never touches Docker) ---

if command -v docker >/dev/null 2>&1; then
    printf '\n'
    info "Docker containers/volumes for herald stacks are NOT removed by this script."
    info "List them:    docker compose ls --all | grep '^herald-'"
    info "Remove one:   docker compose -p herald-<name> down --volumes"
fi

cat <<EOF

  ──────────────────────────────────────
  Herald uninstalled.
  ──────────────────────────────────────
EOF
