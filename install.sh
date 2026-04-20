#!/usr/bin/env bash
set -e

BINARY="alosgarble"
MODULE="github.com/guno1928/alosgarble"

# Check Go is installed
if ! command -v go &>/dev/null; then
    echo "Error: Go is not installed. Install it from https://go.dev/dl/"
    exit 1
fi

echo "Installing $BINARY..."
go install "$MODULE@latest"

# Find where go install put the binary
GOBIN="$(go env GOBIN)"
if [ -z "$GOBIN" ]; then
    GOBIN="$(go env GOPATH)/bin"
fi

if [ ! -f "$GOBIN/$BINARY" ]; then
    echo "Error: binary not found at $GOBIN/$BINARY"
    exit 1
fi

echo "Binary installed to $GOBIN/$BINARY"

# Auto-add GOBIN to PATH if not already there
if ! echo "$PATH" | tr ':' '\n' | grep -qx "$GOBIN"; then
    SHELL_NAME="$(basename "$SHELL")"
    case "$SHELL_NAME" in
        zsh)  RC="$HOME/.zshrc" ;;
        fish) RC="$HOME/.config/fish/config.fish" ;;
        *)    RC="$HOME/.bashrc" ;;
    esac

    EXPORT_LINE="export PATH=\"$GOBIN:\$PATH\""
    if [ "$SHELL_NAME" = "fish" ]; then
        EXPORT_LINE="set -gx PATH $GOBIN \$PATH"
    fi

    # Only add if not already in the rc file
    if ! grep -qF "$GOBIN" "$RC" 2>/dev/null; then
        echo "" >> "$RC"
        echo "# Added by alosgarble installer" >> "$RC"
        echo "$EXPORT_LINE" >> "$RC"
        echo "Added $GOBIN to PATH in $RC"
    fi

    # Make it available right now too
    export PATH="$GOBIN:$PATH"
fi

echo 'export PATH="$(go env GOPATH)/bin:$PATH"' >> ~/.bashrc && source ~/.bashrc

# Verify it works
if command -v "$BINARY" &>/dev/null; then
    echo ""
    echo "Done! Run '$BINARY' to get started."
    echo "If it says 'command not found', restart your terminal or run: source $RC"
else
    echo ""
    echo "Done! Restart your terminal or run: source $RC"
fi
