#!/bin/sh
set -eu

# Herald install/update script
# Usage: curl -fsSL https://raw.githubusercontent.com/nogo/herald/main/install.sh | sh
#
# Fresh install: creates user, directories, downloads binary
# Update:        downloads new binary, restarts service if running

REPO="nogo/herald"
INSTALL_DIR="/usr/local/bin"
DATA_DIR="/etc/herald"
DEPLOY_DIR="/opt/deploy"
USER="herald"
UNIT_PATH="/etc/systemd/system/herald.service"

# --- helpers ---

info() { printf '  \033[36m%s\033[0m\n' "$*"; }
ok()   { printf '  \033[32m✓\033[0m %s\n' "$*"; }
warn() { printf '  \033[33m!\033[0m %s\n' "$*"; }
err()  { printf '  \033[31m✗\033[0m %s\n' "$*" >&2; }
die()  { err "$*"; exit 1; }

need_cmd() {
    command -v "$1" >/dev/null 2>&1 || die "Required command not found: $1"
}

# write_service writes the systemd unit and a placeholder environment file, then
# reloads systemd. Idempotent — also called on update so hardening changes
# propagate. ExecStart omits --config so the daemon auto-detects
# $DATA_DIR/repo/config.yml after `herald init` runs. A custom services_dir
# requires editing ReadWritePaths below by hand.
write_service() {
    cat > "$UNIT_PATH" <<EOF
[Unit]
Description=Herald - deployment daemon
Documentation=https://github.com/${REPO}
After=network-online.target docker.service
Wants=network-online.target docker.service
Requires=docker.service

[Service]
Type=simple
User=${USER}
Group=${USER}
ExecStart=${INSTALL_DIR}/herald serve --data-dir ${DATA_DIR}
Restart=on-failure
RestartSec=10
TimeoutStartSec=30
TimeoutStopSec=30

# Environment — secrets go in the environment file, never in the unit file
EnvironmentFile=-${DATA_DIR}/environment

# Security hardening
NoNewPrivileges=true
ProtectSystem=strict
ReadWritePaths=${DATA_DIR} ${DEPLOY_DIR} /var/run/docker.sock /home/${USER}
PrivateTmp=true
ProtectHome=read-only
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectKernelLogs=true
ProtectControlGroups=true
ProtectHostname=true
ProtectClock=true
RestrictNamespaces=true
RestrictRealtime=true
RestrictSUIDSGID=true
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6
LockPersonality=true
SystemCallArchitectures=native
# Note: docker socket access is root-equivalent; these directives harden the
# herald process surface but do not contain a compromised docker socket.

# Logging
StandardOutput=journal
StandardError=journal
SyslogIdentifier=herald

[Install]
WantedBy=multi-user.target
EOF

    # Placeholder environment file (loaded by the unit; never holds secrets at rest).
    if [ ! -f "$DATA_DIR/environment" ]; then
        cat > "$DATA_DIR/environment" <<'ENVEOF'
# Herald environment variables
# Add environment variables here. They will be loaded by the systemd service.
# Example:
# GITHUB_TOKEN=ghp_xxxxxxxxxxxxxxxxxxxx
ENVEOF
        chown "$USER:$USER" "$DATA_DIR/environment"
        chmod 600 "$DATA_DIR/environment"
    fi

    systemctl daemon-reload
    ok "systemd unit written to $UNIT_PATH"
}

# --- checks ---

if [ "$(id -u)" -ne 0 ]; then
    die "This script must be run as root. Try: sudo sh install.sh"
fi

need_cmd docker
need_cmd git

# Detect downloader
if command -v curl >/dev/null 2>&1; then
    DL="curl -fsSL -o"
elif command -v wget >/dev/null 2>&1; then
    DL="wget -qO"
else
    die "curl or wget required"
fi

# --- detect mode ---

IS_UPDATE=false
OLD_VERSION=""
if [ -x "$INSTALL_DIR/herald" ]; then
    IS_UPDATE=true
    OLD_VERSION=$("$INSTALL_DIR/herald" version 2>/dev/null || echo "unknown")
    info "Existing installation found: $OLD_VERSION"
fi

# --- detect arch ---

ARCH=$(uname -m)
case "$ARCH" in
    x86_64|amd64)  ARCH="amd64" ;;
    aarch64|arm64) ARCH="arm64" ;;
    *)             die "Unsupported architecture: $ARCH" ;;
esac

OS=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$OS" in
    linux)  ;;
    darwin) ;;
    *)      die "Unsupported OS: $OS" ;;
esac

# --- download ---

info "Downloading herald for ${OS}/${ARCH}..."

ARCHIVE="herald_${OS}_${ARCH}.tar.gz"
RELEASE_URL="https://github.com/${REPO}/releases/latest/download/${ARCHIVE}"
CHECKSUMS_URL="https://github.com/${REPO}/releases/latest/download/checksums.txt"
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

$DL "$TMP/herald.tar.gz" "$RELEASE_URL" || die "Download failed. Check https://github.com/${REPO}/releases"

# Verify the download against the published checksums before trusting it.
if command -v sha256sum >/dev/null 2>&1; then
    SHA_CMD="sha256sum"
elif command -v shasum >/dev/null 2>&1; then
    SHA_CMD="shasum -a 256"
else
    die "sha256sum or shasum is required to verify the download"
fi

$DL "$TMP/checksums.txt" "$CHECKSUMS_URL" || die "Could not download checksums.txt for verification"
EXPECTED=$(grep -F "$ARCHIVE" "$TMP/checksums.txt" | head -1 | awk '{print $1}')
[ -n "$EXPECTED" ] || die "No checksum found for $ARCHIVE in checksums.txt"
ACTUAL=$($SHA_CMD "$TMP/herald.tar.gz" | awk '{print $1}')
if [ "$EXPECTED" != "$ACTUAL" ]; then
    die "Checksum mismatch for $ARCHIVE (expected $EXPECTED, got $ACTUAL) — refusing to install"
fi
ok "Checksum verified"

tar -xzf "$TMP/herald.tar.gz" -C "$TMP"

# Find the binary (GoReleaser puts it inside the archive)
BINARY=$(find "$TMP" -name herald -type f | head -1)
if [ -z "$BINARY" ]; then
    die "herald binary not found in archive"
fi

chmod +x "$BINARY"
NEW_VERSION=$($BINARY version 2>/dev/null || echo "herald")
ok "Downloaded $NEW_VERSION"

# --- update path ---

if [ "$IS_UPDATE" = true ]; then
    if [ "$OLD_VERSION" = "$NEW_VERSION" ]; then
        ok "Already up to date ($NEW_VERSION)"
        exit 0
    fi

    info "Updating: $OLD_VERSION -> $NEW_VERSION"
    install -m 755 "$BINARY" "$INSTALL_DIR/herald"
    ok "Binary updated"

    # Refresh the unit so hardening/path changes in this release propagate.
    if command -v systemctl >/dev/null 2>&1; then
        write_service
    fi

    # Restart service if running
    if command -v systemctl >/dev/null 2>&1 && systemctl is-active --quiet herald 2>/dev/null; then
        info "Restarting herald service..."
        systemctl restart herald
        ok "Service restarted"
    else
        warn "Service not running. Start with: systemctl start herald"
    fi

    cat <<EOF

  ──────────────────────────────────────
  Herald updated: $OLD_VERSION -> $NEW_VERSION
  ──────────────────────────────────────
EOF
    exit 0
fi

# --- fresh install ---

# Create user
if id "$USER" >/dev/null 2>&1; then
    ok "User '$USER' already exists"
else
    info "Creating user '$USER'..."
    useradd -r -m -d "/home/$USER" -s /bin/bash -G docker "$USER"
    ok "User '$USER' created (docker group)"
fi

# Ensure user is in docker group
if ! id -nG "$USER" | grep -qw docker; then
    usermod -aG docker "$USER"
    ok "Added '$USER' to docker group"
fi

# Directories
install -d -o "$USER" -g "$USER" -m 700 "$DATA_DIR"
install -d -o "$USER" -g "$USER" -m 755 "$DEPLOY_DIR"
ok "Directories ready"

# Install binary
install -m 755 "$BINARY" "$INSTALL_DIR/herald"
ok "Installed to $INSTALL_DIR/herald"

# systemd unit (written disabled/stopped — there is no config until `herald init`)
if command -v systemctl >/dev/null 2>&1; then
    write_service
else
    warn "systemd not found — skipping service install"
fi

cat <<EOF

  ──────────────────────────────────────
  Herald installed: $NEW_VERSION

  Binary:   $INSTALL_DIR/herald
  User:     $USER
  Data:     $DATA_DIR
  Deploy:   $DEPLOY_DIR
  Service:  $UNIT_PATH (installed, not started)

  Next steps:

    # 1. Bootstrap from your server repo (handles auth, secrets, webhooks):
    sudo -iu $USER herald init <your-org/server-repo>

    # 2. Start the daemon (wires Caddy + webhooks on first start):
    sudo systemctl enable --now herald

  Set any required app secrets with:
    sudo -iu $USER herald secret set <stack>/<key>

  Uninstall later with: sudo sh uninstall.sh

  ──────────────────────────────────────
EOF
