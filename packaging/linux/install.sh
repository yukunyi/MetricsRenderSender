#!/usr/bin/env bash

set -euo pipefail

package_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
app_dir="${HOME}/.local/lib/metrics_render_sender"
data_dir="${XDG_DATA_HOME:-${HOME}/.local/share}"
case "$data_dir" in
    /*) ;;
    *) data_dir="${HOME}/.local/share" ;;
esac
applications_dir="$data_dir/applications"
icon_dir="$data_dir/icons/hicolor/scalable/apps"

# Desktop entries decode string escapes before parsing the quoted Exec argument.
desktop_exec_path() {
    local value="$1"
    value="${value//\\/\\\\}"
    value="${value//\"/\\\"}"
    value="${value//\$/\\\$}"
    value="${value//\`/\\\`}"
    value="${value//%/%%}"
    value="${value//\\/\\\\}"
    value="${value//$'\n'/\\n}"
    value="${value//$'\r'/\\r}"
    value="${value//$'\t'/\\t}"
    printf '"%s"' "$value"
}

for file in metrics_render_sender metrics_render_sender.desktop metrics_render_sender.svg; do
    if [ ! -f "$package_dir/$file" ]; then
        printf 'Missing package file: %s\n' "$package_dir/$file" >&2
        exit 1
    fi
done

mkdir -p "$app_dir" "$applications_dir" "$icon_dir"
binary_temp="$(mktemp "$app_dir/.metrics_render_sender.XXXXXX")"
desktop_temp=""
trap 'rm -f -- "$binary_temp" "${desktop_temp:-}"' EXIT

# Rename allows reinstalling while the previous executable is still running.
install -m 755 "$package_dir/metrics_render_sender" "$binary_temp"
mv -f -- "$binary_temp" "$app_dir/metrics_render_sender"
install -m 644 "$package_dir/metrics_render_sender.svg" "$icon_dir/metrics_render_sender.svg"

desktop_temp="$(mktemp "$applications_dir/.metrics_render_sender.XXXXXX")"
while IFS= read -r line || [ -n "$line" ]; do
    # The shell command is expanded by the launcher, not by this installer.
    # shellcheck disable=SC2016
    case "$line" in
        # Pass the path as an argument: some launchers check the executable's
        # existence before expanding literal percent escapes in the Exec key.
        Exec=*) printf 'Exec=/bin/sh -c %s metrics_render_sender %s\n' \
            "$(desktop_exec_path 'exec "$1"')" \
            "$(desktop_exec_path "$app_dir/metrics_render_sender")" ;;
        *) printf '%s\n' "$line" ;;
    esac
done < "$package_dir/metrics_render_sender.desktop" > "$desktop_temp"
chmod 644 "$desktop_temp"
mv -f -- "$desktop_temp" "$applications_dir/metrics_render_sender.desktop"

if command -v update-desktop-database >/dev/null 2>&1; then
    update-desktop-database "$applications_dir" || true
fi
if command -v gtk-update-icon-cache >/dev/null 2>&1; then
    gtk-update-icon-cache -f -t "$data_dir/icons/hicolor" >/dev/null 2>&1 || true
fi

printf 'Installed: %s\n' "$app_dir/metrics_render_sender"
printf 'Launch MetricsRenderSender from your application menu.\n'
