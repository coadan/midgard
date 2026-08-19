#!/bin/sh
set -eu

if [ "$#" -ne 1 ]; then
  echo "usage: $0 OUTPUT_DIRECTORY" >&2
  exit 2
fi

output=$1

mkdir -p "$output/libexec"
go build -o "$output/midgard" ./cmd/midgard

build_companion() {
  name=$1
  module=$2
  version=$3
  download=$(go mod download -json "$module@$version")
  source_dir=$(printf '%s\n' "$download" | sed -n 's/^[[:space:]]*"Dir":[[:space:]]*"\(.*\)",$/\1/p')
  module_sum=$(printf '%s\n' "$download" | sed -n 's/^[[:space:]]*"Sum":[[:space:]]*"\(.*\)",$/\1/p')
  if [ -z "$source_dir" ] || [ ! -d "$source_dir" ]; then
    echo "could not resolve $module@$version" >&2
    exit 1
  fi
  go build -C "$source_dir" -o "$output/libexec/$name" "./cmd/$name"
  if command -v shasum >/dev/null 2>&1; then
    binary_sum=$(shasum -a 256 "$output/libexec/$name" | awk '{print $1}')
  else
    binary_sum=$(sha256sum "$output/libexec/$name" | awk '{print $1}')
  fi
  printf '{"schema":"midgard.companion/v1","name":"%s","module":"%s","version":"%s","sum":"%s","binary_sha256":"sha256:%s"}\n' \
    "$name" "$module" "$version" "$module_sum" "$binary_sum" > "$output/libexec/$name.manifest.json"
}

build_companion ygg github.com/coadan/yggdrasil v0.3.0
build_companion heimdal github.com/coadan/heimdal v0.0.0-20260803075142-786747acf5c4
