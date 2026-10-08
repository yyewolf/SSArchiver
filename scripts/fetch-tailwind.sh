#!/bin/sh
# Downloads the pinned Tailwind CSS standalone CLI to ./bin/tailwindcss and verifies its checksum.
set -eu
VERSION="v4.3.3"
case "$(uname -s)-$(uname -m)" in
  Linux-x86_64)               ASSET=tailwindcss-linux-x64;   SUM=dc61b3ac6b8c9ca874c0cc4c57b2409791a64c5540404ca5f5367360babc313a ;;
  Linux-aarch64|Linux-arm64)  ASSET=tailwindcss-linux-arm64; SUM=55fd0b241214eff3de1e8ee4f22796662f2d2e7a49bcfca7477cfd0bac398195 ;;
  Darwin-arm64)               ASSET=tailwindcss-macos-arm64; SUM=cdf646702987a743464dff4d9c60fd4480d1c1e73dd819a9a67f1078815dce9d ;;
  Darwin-x86_64)              ASSET=tailwindcss-macos-x64;   SUM=7922e0953f2110c05976e3bf58f14e643d90427575e766b7d433f5f80cbee7e1 ;;
  *) echo "fetch-tailwind: unsupported platform $(uname -s)-$(uname -m)" >&2; exit 1 ;;
esac
mkdir -p bin
curl -fsSL -o bin/tailwindcss.tmp "https://github.com/tailwindlabs/tailwindcss/releases/download/${VERSION}/${ASSET}"
if command -v sha256sum >/dev/null 2>&1; then
  echo "${SUM}  bin/tailwindcss.tmp" | sha256sum -c - >/dev/null
else
  echo "${SUM}  bin/tailwindcss.tmp" | shasum -a 256 -c - >/dev/null
fi
chmod +x bin/tailwindcss.tmp
mv bin/tailwindcss.tmp bin/tailwindcss
echo "tailwindcss ${VERSION} ready"
