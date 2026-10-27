#!/bin/sh
# rodin3d-mcp installer (Linux / macOS). Run with:
#   curl -fsSL https://github.com/sairaph/rodin3d-mcp/releases/latest/download/install.sh | sh
set -e
OWNER="sairaph"
REPO="rodin3d-mcp"
BIN="rodin3d-mcp"
# Extra flags for the configure step, e.g. CONFIGURE_ARGS="--yes --all" sh install.sh
CONFIGURE_ARGS="${CONFIGURE_ARGS:-}"

# --- detect OS / arch ------------------------------------------------------
OS="$(uname -s)"
ARCH="$(uname -m)"
case "$OS" in
  Linux*)  os=linux ;;
  Darwin*) os=darwin ;;
  *) printf '\n  Unsupported OS: %s\n' "$OS" >&2; exit 1 ;;
esac
case "$ARCH" in
  x86_64|amd64)  arch=amd64 ;;
  arm64|aarch64) arch=arm64 ;;
  *) printf '\n  Unsupported architecture: %s\n' "$ARCH" >&2; exit 1 ;;
esac

ASSET="${BIN}-${os}-${arch}"
URL="https://github.com/${OWNER}/${REPO}/releases/latest/download/${ASSET}"
INSTALL_DIR="$HOME/.${REPO}/bin"
TARGET="$INSTALL_DIR/$BIN"
mkdir -p "$INSTALL_DIR"

printf '\n  %s installer\n\n  Downloading %s...\n' "$BIN" "$ASSET"

# A running syncer holds the old binary open; ask it to stop first.
if [ -x "$TARGET" ]; then
  "$TARGET" daemon --stop >/dev/null 2>&1 || true
fi

# Download beside the target so a failure never leaves a half-written binary.
TEMP="${TARGET}.new"
trap 'rm -f "$TEMP" "$TEMP.err"' EXIT HUP INT TERM

download_failed() {
  printf '\n  Download failed. Please check your connection and try again.\n' >&2
  printf '  URL: %s\n' "$URL" >&2
  if [ -n "$1" ]; then
    printf '  Reason: %s\n' "$1" >&2
  fi
  exit 1
}

if command -v curl >/dev/null 2>&1; then
  if ! curl -fSL --progress-bar "$URL" -o "$TEMP" 2>"$TEMP.err"; then
    download_failed "$(tr '\n' ' ' < "$TEMP.err" 2>/dev/null)"
  fi
elif command -v wget >/dev/null 2>&1; then
  if ! wget -q --show-progress -O "$TEMP" "$URL" 2>"$TEMP.err"; then
    if ! wget -q -O "$TEMP" "$URL" 2>"$TEMP.err"; then
      download_failed "$(tr '\n' ' ' < "$TEMP.err" 2>/dev/null)"
    fi
  fi
else
  download_failed "neither curl nor wget is available"
fi
rm -f "$TEMP.err"

if [ ! -s "$TEMP" ]; then
  printf '  Download did not complete; nothing was installed.\n' >&2
  exit 1
fi

# Verify the download against the release's SHA256SUMS.txt.
if command -v sha256sum >/dev/null 2>&1; then
  SHA256_CMD="sha256sum"
elif command -v shasum >/dev/null 2>&1; then
  SHA256_CMD="shasum -a 256"
else
  SHA256_CMD=""
fi
if [ -n "$SHA256_CMD" ]; then
  CHECKSUM_URL="${URL%/*}/SHA256SUMS.txt"
  EXPECTED=""
  if command -v curl >/dev/null 2>&1; then
    EXPECTED=$(curl -fsSL "$CHECKSUM_URL" 2>/dev/null | grep " $ASSET\$" | awk '{print $1}') || EXPECTED=""
  elif command -v wget >/dev/null 2>&1; then
    EXPECTED=$(wget -q -O - "$CHECKSUM_URL" 2>/dev/null | grep " $ASSET\$" | awk '{print $1}') || EXPECTED=""
  fi
  if [ -n "$EXPECTED" ]; then
    ACTUAL=$($SHA256_CMD "$TEMP" | awk '{print $1}')
    if [ "$EXPECTED" != "$ACTUAL" ]; then
      printf '\n  SHA256 mismatch: the download is not the released file; nothing was installed.\n' >&2
      exit 1
    fi
  else
    printf '  Warning: could not fetch SHA256SUMS.txt; the download was not verified.\n' >&2
  fi
else
  printf '  Warning: no sha256 tool found; the download was not verified.\n' >&2
fi

chmod +x "$TEMP"
if ! mv -f "$TEMP" "$TARGET"; then
  printf '\n  Failed to install binary to %s\n' "$TARGET" >&2
  exit 1
fi
trap - EXIT HUP INT TERM

# Stop the syncer again: a tool call during the download may have started one
# from the old binary. The next tool call starts the new one.
stopped=$("$TARGET" daemon --stop 2>/dev/null) || stopped=""
case "$stopped" in
  *stopped*)
    printf '\n  %s\n' "$stopped"
    printf '  Restart your AI client so it picks up the new tool descriptions.\n'
    ;;
esac

# --- add to PATH if missing --------------------------------------------------
case ":$PATH:" in
  *":$INSTALL_DIR:"*) on_path=1 ;;
  *) on_path=0 ;;
esac

if [ "$on_path" -eq 0 ]; then
  line="export PATH=\"$INSTALL_DIR:\$PATH\""
  # The trailing `|| true` keeps `set -e` from ending the script when none of
  # the files exists (the loop's status is then the last failed test).
  for rc in "$HOME/.zshrc" "$HOME/.bashrc" "$HOME/.profile" "$HOME/.bash_profile"; do
    [ -f "$rc" ] || continue
    if ! grep -qF "$INSTALL_DIR" "$rc" 2>/dev/null; then
      printf '\n# added by %s installer\n%s\n' "$BIN" "$line" >> "$rc"
    fi
    on_path=2
    break
  done || true
fi

PATH="$INSTALL_DIR:$PATH"
export PATH

# Under `curl | sh` stdin is the script, so the setup reads the terminal from
# /dev/tty; opening it is the only reliable test that one exists.
if ( : </dev/tty ) 2>/dev/null; then
  # shellcheck disable=SC2086
  "$TARGET" configure $CONFIGURE_ARGS </dev/tty || {
    printf '  configure did not complete.\n'
    printf '  Re-run `%s configure` later to finish setup.\n' "$BIN"
  }
else
  printf '\n  Not running on a terminal. Finish setup with:\n    %s configure\n' "$BIN"
fi

if [ "$on_path" -eq 0 ]; then
  printf '\n  Add this to your shell profile:\n    export PATH="%s:$PATH"\n' "$INSTALL_DIR"
elif [ "$on_path" -eq 2 ]; then
  printf '\n  Open a new terminal so `%s` is on your PATH.\n' "$BIN"
fi
