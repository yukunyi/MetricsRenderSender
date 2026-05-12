#!/bin/bash

set -e

VERSION="${VERSION:-1.0.0}"
LINUX_PACKAGE="metrics_render_sender-linux-amd64-v${VERSION}"
DIST_DIR="dist"
WINDOWS_PACKAGE="metrics_render_sender-windows-amd64-v${VERSION}"
WINDOWS_DIST_DIR="$DIST_DIR/windows/$WINDOWS_PACKAGE"
WINDOWS_ZIP_PATH="$DIST_DIR/windows/$WINDOWS_PACKAGE.zip"
FRONTEND_DIR="frontend"
EMBED_DIST_DIR="src/metrics_render_sender/webassets/webdist"
BUILD_TARGETS="${BUILD_TARGETS:-linux windows}"
SKIP_FRONTEND_BUILD="${SKIP_FRONTEND_BUILD:-0}"
WINDOWS_CC="${WINDOWS_CC:-x86_64-w64-mingw32-gcc}"
WINDOWS_CXX="${WINDOWS_CXX:-x86_64-w64-mingw32-g++}"
WINDOWS_PKG_CONFIG="${WINDOWS_PKG_CONFIG:-x86_64-w64-mingw32-pkg-config}"
WINDOWS_OBJDUMP="${WINDOWS_OBJDUMP:-x86_64-w64-mingw32-objdump}"
MINGW_ROOT="${MINGW_ROOT:-/usr/x86_64-w64-mingw32}"

has_build_target() {
    local needle="$1"
    local target
    for target in $BUILD_TARGETS; do
        if [ "$target" = "$needle" ]; then
            return 0
        fi
    done
    return 1
}

echo "MetricsRenderSender - Package Script"
echo "===================================="
echo "Version: $VERSION"
echo "Linux Package: $LINUX_PACKAGE"
echo "Windows Package Dir: $WINDOWS_DIST_DIR"
echo ""

is_windows_system_dll() {
    local name
    name="$(echo "$1" | tr '[:upper:]' '[:lower:]')"
    case "$name" in
        kernel32.dll|user32.dll|gdi32.dll|advapi32.dll|shell32.dll|ws2_32.dll|ole32.dll|oleaut32.dll|\
        comdlg32.dll|comctl32.dll|secur32.dll|crypt32.dll|ntdll.dll|msvcrt.dll|ucrtbase.dll|\
        api-ms-win-*.dll)
            return 0
            ;;
        *)
            return 1
            ;;
    esac
}

resolve_windows_dll() {
    local dll="$1"
    local search_dirs=()
    local gcc_dll_dir
    gcc_dll_dir="$(dirname "$("$WINDOWS_CC" -print-file-name=libgcc_s_seh-1.dll 2>/dev/null)")"
    search_dirs+=("$MINGW_ROOT/bin")
    search_dirs+=("$MINGW_ROOT/lib")
    search_dirs+=("/usr/x86_64-w64-mingw32/bin")
    search_dirs+=("/usr/x86_64-w64-mingw32/lib")
    search_dirs+=("/mingw64/bin")
    search_dirs+=("/mingw64/lib")
    if [ -n "$gcc_dll_dir" ] && [ "$gcc_dll_dir" != "." ]; then
        search_dirs+=("$gcc_dll_dir")
    fi
    local dir
    for dir in "${search_dirs[@]}"; do
        if [ -f "$dir/$dll" ]; then
            echo "$dir/$dll"
            return 0
        fi
    done
    return 1
}

copy_windows_runtime_deps() {
    local exe_path="$1"
    local target_dir="$2"
    local queue=()
    local seen=()
    local queue_len=0
    local head=0

    queue+=("$exe_path")
    queue_len=1

    while [ "$head" -lt "$queue_len" ]; do
        local current="${queue[$head]}"
        head=$((head + 1))

        while IFS= read -r dll; do
            [ -z "$dll" ] && continue
            if is_windows_system_dll "$dll"; then
                continue
            fi
            local key
            key="$(echo "$dll" | tr '[:upper:]' '[:lower:]')"
            if [[ " ${seen[*]} " == *" $key "* ]]; then
                continue
            fi
            seen+=("$key")

            local dll_path
            if ! dll_path="$(resolve_windows_dll "$dll")"; then
                echo "Error: cannot resolve runtime dependency DLL: $dll"
                exit 1
            fi

            cp -f "$dll_path" "$target_dir/$dll"
            queue+=("$dll_path")
            queue_len=$((queue_len + 1))
        done < <("$WINDOWS_OBJDUMP" -p "$current" | awk '/DLL Name:/{print $3}')
    done
}

if ! command -v go &> /dev/null; then
    echo "Error: Go compiler not found, please install Go first"
    exit 1
fi

echo "Go version: $(go version)"

if [ "$SKIP_FRONTEND_BUILD" != "1" ]; then
    if ! command -v npm &> /dev/null; then
        echo "Error: npm not found, cannot build web frontend"
        exit 1
    fi

    echo "Building Vite frontend..."
    pushd "$FRONTEND_DIR" > /dev/null
    if [ -f package-lock.json ]; then
        npm ci --include=dev
    else
        npm install --include=dev
    fi
    npm run build
    popd > /dev/null

    if [ ! -f "$FRONTEND_DIR/dist/index.html" ]; then
        echo "Error: frontend build output missing: $FRONTEND_DIR/dist/index.html"
        exit 1
    fi

    echo "Syncing frontend dist to $EMBED_DIST_DIR ..."
    mkdir -p "$EMBED_DIST_DIR"
    rm -rf "$EMBED_DIST_DIR"/*
    cp -r "$FRONTEND_DIR"/dist/* "$EMBED_DIST_DIR"/
else
    if [ ! -f "$EMBED_DIST_DIR/index.html" ]; then
        echo "Error: embedded frontend output missing: $EMBED_DIST_DIR/index.html"
        exit 1
    fi
    echo "Using prebuilt frontend assets in $EMBED_DIST_DIR ..."
fi

mkdir -p dist

echo "Cleaning previous build files..."
rm -rf dist/*

cd src/metrics_render_sender

if [ ! -f go.mod ]; then
    echo "Initializing Go module..."
    go mod init metrics_render_sender
fi

echo "Downloading dependencies..."
go mod tidy

cd ../../src/metrics_render_sender

if has_build_target "linux"; then
    echo "Compiling Linux version..."
    GOOS=linux GOARCH=amd64 go build \
        -ldflags "-s -w -X main.Version=$VERSION -X main.BuildTime=$(date -u '+%Y-%m-%dT%H:%M:%SZ')" \
        -trimpath \
        -buildmode=exe \
        -o ../../dist/metrics_render_sender-linux-amd64 .
fi

if has_build_target "windows"; then
    echo "Compiling Windows version..."
    if ! command -v "$WINDOWS_CC" &> /dev/null; then
        echo "Error: $WINDOWS_CC not found, cannot compile windows with cgo"
        exit 1
    fi
    if ! command -v "$WINDOWS_CXX" &> /dev/null; then
        echo "Error: $WINDOWS_CXX not found, cannot compile windows with cgo"
        exit 1
    fi
    CC="$WINDOWS_CC" \
    CXX="$WINDOWS_CXX" \
    PKG_CONFIG="$WINDOWS_PKG_CONFIG" \
    GOOS=windows GOARCH=amd64 CGO_ENABLED=1 go build \
        -ldflags "-s -w -H=windowsgui -X main.Version=$VERSION -X main.BuildTime=$(date -u '+%Y-%m-%dT%H:%M:%SZ')" \
        -trimpath \
        -o ../../dist/metrics_render_sender-windows-amd64.exe .
fi

cd ../..

if has_build_target "linux"; then
    chmod +x dist/metrics_render_sender-linux-amd64
fi

if has_build_target "linux"; then
    echo "Creating Linux package directory..."
    rm -rf "$LINUX_PACKAGE"
    mkdir -p "$LINUX_PACKAGE"

    echo "Copying files to Linux package directory..."
    cp dist/metrics_render_sender-linux-amd64 "$LINUX_PACKAGE/metrics_render_sender"
    if [ -f README.md ]; then
        cp README.md "$LINUX_PACKAGE/"
    fi

    chmod +x "$LINUX_PACKAGE/metrics_render_sender"
fi

if has_build_target "windows"; then
    echo "Creating Windows package directory..."
    mkdir -p "$WINDOWS_DIST_DIR"

    echo "Copying files to Windows package directory..."
    cp dist/metrics_render_sender-windows-amd64.exe "$WINDOWS_DIST_DIR/metrics_render_sender.exe"
    if [ -f README.md ]; then
        cp README.md "$WINDOWS_DIST_DIR/"
    fi
    if [ -f docs/LIBRE_HARDWARE_MONITOR.md ]; then
        cp docs/LIBRE_HARDWARE_MONITOR.md "$WINDOWS_DIST_DIR/"
    fi
    copy_windows_runtime_deps "$WINDOWS_DIST_DIR/metrics_render_sender.exe" "$WINDOWS_DIST_DIR"
fi

if has_build_target "linux"; then
    echo "Verifying Linux package contents..."
    echo "Linux package directory contents:"
    ls -la "$LINUX_PACKAGE/"
fi

if has_build_target "windows"; then
    echo "Verifying Windows package contents..."
    echo "Windows package directory contents:"
    ls -la "$WINDOWS_DIST_DIR/"
fi

if has_build_target "linux"; then
    echo "Creating Linux tar archive..."
    tar -czf "dist/$LINUX_PACKAGE.tar.gz" "$LINUX_PACKAGE"
fi

if has_build_target "windows"; then
    echo "Creating Windows zip archive..."
    if command -v zip &> /dev/null; then
        pushd "$DIST_DIR/windows" > /dev/null
        zip -r "$WINDOWS_PACKAGE.zip" "$WINDOWS_PACKAGE"
        popd > /dev/null
    else
        echo "Warning: zip command not found, creating tar archive instead"
        tar -czf "$DIST_DIR/windows/$WINDOWS_PACKAGE.tar.gz" -C "$DIST_DIR/windows" "$WINDOWS_PACKAGE"
    fi
fi

echo "Cleaning up temporary package directories..."
if has_build_target "linux"; then
    rm -rf "$LINUX_PACKAGE"
fi

echo ""
echo "Packages created successfully!"
echo "Output files:"
if has_build_target "linux"; then
    ls -la "$DIST_DIR/$LINUX_PACKAGE.tar.gz"
fi
if has_build_target "windows"; then
    if [ -f "$WINDOWS_ZIP_PATH" ]; then
        ls -la "$WINDOWS_ZIP_PATH"
    elif [ -f "$DIST_DIR/windows/$WINDOWS_PACKAGE.tar.gz" ]; then
        ls -la "$DIST_DIR/windows/$WINDOWS_PACKAGE.tar.gz"
    fi
fi

echo ""
echo "Installation Instructions:"
echo ""
if has_build_target "linux"; then
    echo "Linux:"
    echo "1. Extract: tar -xzf dist/$LINUX_PACKAGE.tar.gz"
    echo "2. Enter directory: cd $LINUX_PACKAGE"
    echo "3. Run in foreground: ./metrics_render_sender"
    echo ""
fi
if has_build_target "windows"; then
    echo "Windows:"
    echo "1. Use directory: $WINDOWS_DIST_DIR"
    echo "2. Or extract zip: $WINDOWS_ZIP_PATH"
    echo "3. Run metrics_render_sender.exe"
    echo "4. Use Web UI if needed: set METRICS_RENDER_SENDER_WEB=1 && metrics_render_sender.exe --port 18086"
fi

echo ""
echo "Note: Configure the required output targets before production use"
