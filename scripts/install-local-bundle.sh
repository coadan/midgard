#!/bin/sh
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
stage=$(mktemp -d "${TMPDIR:-/tmp}/midgard-bundle.XXXXXX")
trap 'rm -rf "$stage"' EXIT HUP INT TERM

sh "$root/scripts/build-bundle.sh" "$stage"
bin_dir=$(go env GOBIN)
if [ -z "$bin_dir" ]; then
  bin_dir=$(go env GOPATH)/bin
fi
mkdir -p "$bin_dir/libexec"
install -m 755 "$stage/midgard" "$bin_dir/midgard"
install -m 755 "$stage/libexec/ygg" "$bin_dir/libexec/ygg"
install -m 644 "$stage/libexec/ygg.manifest.json" "$bin_dir/libexec/ygg.manifest.json"
install -m 755 "$stage/libexec/heimdal" "$bin_dir/libexec/heimdal"
install -m 644 "$stage/libexec/heimdal.manifest.json" "$bin_dir/libexec/heimdal.manifest.json"

echo "Installed Midgard and bundled Yggdrasil to $bin_dir"
