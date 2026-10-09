#!/usr/bin/env sh
set -eu

src=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
if ! command -v go >/dev/null 2>&1; then
    printf '%s\n' 'Go is required to build Ledger.' >&2
    exit 1
fi

default_dir="$HOME/.local/bin"
install_dir=${LEDGER_INSTALL_DIR:-"$default_dir"}
mkdir -p "$install_dir"
install_dir=$(CDPATH= cd -- "$install_dir" && pwd)
staged=$(mktemp "$install_dir/.ledger.XXXXXXXX")
trap 'rm -f "$staged"' EXIT HUP INT TERM

(cd "$src" && go build -o "$staged" .)
chmod 755 "$staged"
mv -f "$staged" "$install_dir/ledger"
"$install_dir/ledger" --help >/dev/null

case ":$PATH:" in
    *":$install_dir:"*) ;;
    *)
        case "${SHELL:-}" in
            */zsh) profile="$HOME/.zshrc" ;;
            */bash) profile="$HOME/.bashrc" ;;
            *) profile="$HOME/.profile" ;;
        esac
        if [ "$install_dir" = "$default_dir" ]; then
            marker='# Ledger CLI PATH'
            if [ ! -f "$profile" ] || ! grep -Fq "$marker" "$profile"; then
                printf '\n%s\n%s\n' "$marker" 'export PATH="$HOME/.local/bin:$PATH"' >> "$profile"
            fi
            printf 'Added %s to %s. Open a new shell to use ledger.\n' "$install_dir" "$profile"
        else
            printf 'Add %s to your PATH to use ledger everywhere.\n' "$install_dir"
        fi
        ;;
esac

printf 'Ledger installed at %s\n' "$install_dir/ledger"
