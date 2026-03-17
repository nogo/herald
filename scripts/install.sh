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

# --- helpers ---

info() { printf '  \033[36m%s\033[0m\n' "$*"; }
ok()   { printf '  \033[32m✓\033[0m %s\n' "$*"; }
warn() { printf '  \033[33m!\033[0m %s\n' "$*"; }
err()  { printf '  \033[31m✗\033[0m %s\n' "$*" >&2; }
die()  { err "$*"; exit 1; }

need_cmd() {
    command -v "$1" >/dev/null 2>&1 || die "Required command not found: $1"
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

RELEASE_URL="https://github.com/${REPO}/releases/latest/download/herald_${OS}_${ARCH}.tar.gz"
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

$DL "$TMP/herald.tar.gz" "$RELEASE_URL" || die "Download failed. Check https://github.com/${REPO}/releases"
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

cat <<EOF

  ──────────────────────────────────────
  Herald installed: $NEW_VERSION

  Binary:   $INSTALL_DIR/herald
  User:     $USER
  Data:     $DATA_DIR
  Deploy:   $DEPLOY_DIR

  Next steps (as $USER):

    su - $USER
    herald auth login --client-id <your-oauth-client-id>
    herald init <your-org/server-repo>
    herald secret set herald/webhook_secret
    herald deploy --all
    exit

  Then install as a service (as root):

    herald install --user $USER

  ──────────────────────────────────────
EOF
