#!/bin/sh
set -eu

# Build, archive, and publish a GitHub release for the current tag.
# Usage: scripts/release.sh
# Requires: go, tar, shasum/sha256sum, gh

MODULE="github.com/nogo/herald"
DIST="dist"
TARGETS="linux/amd64 linux/arm64 darwin/amd64 darwin/arm64"

TAG=$(git describe --tags --exact-match 2>/dev/null) || {
	echo "ERROR: HEAD is not tagged" >&2
	exit 1
}
COMMIT=$(git rev-parse --short HEAD)
DATE=$(git log -1 --format=%cI)

LDFLAGS="-s -w -X ${MODULE}/cmd.commit=${COMMIT} -X ${MODULE}/cmd.tag=${TAG} -X ${MODULE}/cmd.date=${DATE}"

rm -rf "$DIST"

# Cross-compile
for target in $TARGETS; do
	os="${target%/*}"
	arch="${target#*/}"
	name="herald_${os}_${arch}"
	dir="${DIST}/${name}"

	echo "Building ${os}/${arch}..."
	mkdir -p "$dir"
	CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" \
		go build -trimpath -ldflags "$LDFLAGS" -o "${dir}/herald" .
	tar -czf "${DIST}/${name}.tar.gz" -C "$DIST" "$name"
	rm -rf "$dir"
done

# Checksums
cd "$DIST"
if command -v sha256sum >/dev/null 2>&1; then
	sha256sum ./*.tar.gz >checksums.txt
else
	shasum -a 256 ./*.tar.gz >checksums.txt
fi
cd ..

# Publish
gh release create "$TAG" "${DIST}"/*.tar.gz "${DIST}/checksums.txt" \
	--generate-notes \
	--verify-tag
