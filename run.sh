#!/usr/bin/env bash
set -euo pipefail

BINARY="whatsapp-tui"

usage() {
    echo "Usage: $0 [--install <distro>] [--run] [--logout]"
    echo ""
    echo "Options:"
    echo "  --install <distro>  Install dependencies (arch, debian, fedora, opensuse)"
    echo "  --run               Run the app (builds automatically if needed)"
    echo "  --logout            Remove session data and log out"
    echo ""
    echo "Examples:"
    echo "  $0 --install arch"
    echo "  $0 --install debian"
    echo "  $0 --run"
    echo "  $0 --logout"
    echo "  $0 --install arch --run"
    exit 1
}

install_deps() {
    local distro="$1"
    case "$distro" in
        arch)
            echo ":: Installing dependencies for Arch..."
            sudo pacman -S --needed go gcc chafa sqlite
            ;;
        debian|ubuntu)
            echo ":: Installing dependencies for Debian/Ubuntu..."
            sudo apt update
            sudo apt install -y golang gcc chafa libsqlite3-dev
            ;;
        fedora)
            echo ":: Installing dependencies for Fedora..."
            sudo dnf install -y golang gcc chafa sqlite-devel
            ;;
        opensuse|suse)
            echo ":: Installing dependencies for openSUSE..."
            sudo zypper install -y go gcc chafa sqlite3-devel
            ;;
        *)
            echo "Error: Unknown distro '$distro'"
            echo "Supported: arch, debian, ubuntu, fedora, opensuse"
            exit 1
            ;;
    esac
    echo ":: Dependencies installed."
}

build() {
    echo ":: Building $BINARY..."
    go build -o "$BINARY" ./cmd/whatsapp-tui
    echo ":: Build complete."
}

run() {
    if [ ! -f "$BINARY" ]; then
        build
    fi
    exec ./"$BINARY"
}

logout() {
    echo ":: Removing session data..."
    rm -f whatsapp.db whatsapp.db-wal whatsapp.db-shm messages.db messages.db-wal messages.db-shm
    echo ":: Logged out. Run the app again to pair with a new QR code."
}

if [ $# -eq 0 ]; then
    run
fi

while [ $# -gt 0 ]; do
    case "$1" in
        --install)
            [ $# -lt 2 ] && { echo "Error: --install requires a distro name"; exit 1; }
            install_deps "$2"
            shift 2
            ;;
        --run)
            run
            shift
            ;;
        --logout)
            logout
            shift
            ;;
        --help|-h)
            usage
            ;;
        *)
            echo "Error: Unknown option '$1'"
            usage
            ;;
    esac
done
