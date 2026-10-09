#!/usr/bin/env bash
set -euo pipefail

# Build release archives only. Cross-compilation is not native execution;
# the release workflow smoke-tests extracted binaries on matching runners.
if [[ $# -ne 2 ]]; then
    echo "Usage: $0 <tag-or-version> <output-directory>" >&2
    exit 2
fi
version=$1
case "$version" in
    ''|[!A-Za-z0-9]*|*[!A-Za-z0-9._+-]*)
        echo "Version must start with a letter or digit and contain only letters, digits, ., _, +, or -." >&2
        exit 2
        ;;
esac

mkdir -p -- "$2"
output_dir=$(cd -- "$2" && pwd)
repo_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
work_dir=$(mktemp -d)
trap 'rm -rf -- "$work_dir"' EXIT

if command -v sha256sum >/dev/null 2>&1; then
    checksum=(sha256sum)
elif command -v shasum >/dev/null 2>&1; then
    checksum=(shasum -a 256)
else
    echo "A SHA-256 tool is required: sha256sum (Linux) or shasum (macOS)." >&2
    exit 1
fi

cd -- "$repo_dir"
cp LICENSE README.md "$work_dir/"
cp -R docs "$work_dir/"
mkdir -p "$work_dir/scripts"
cp scripts/install-voxtype.py scripts/voxtype_config.py "$work_dir/scripts/"
archives=()
for os in linux darwin; do
    for arch in amd64 arm64; do
        archive="codex-transcribe_${version}_${os}_${arch}.tar.gz"
        echo "Building $archive"
        CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build \
            -trimpath -ldflags "-s -w -X main.version=$version" \
            -o "$work_dir/codex-transcribe" .
        tar -czf "$output_dir/$archive" -C "$work_dir" codex-transcribe LICENSE README.md docs scripts
        archives+=("$archive")
    done
done

# Hash only this invocation's four archives, even if the directory has old assets.
(
    cd -- "$output_dir"
    : > SHA256SUMS
    for archive in "${archives[@]}"; do
        "${checksum[@]}" "$archive" > "$archive.sha256"
        cat "$archive.sha256" >> SHA256SUMS
    done
)
echo "Release archives, individual .sha256 files and SHA256SUMS written to $output_dir"
