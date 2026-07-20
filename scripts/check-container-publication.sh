#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
set -euo pipefail

root="$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)"
workflow="$root/.github/workflows/release.yml"
dockerfile="$root/Dockerfile"
gha='$'
continuation="\\"
git_sha_ldflag="releaseRevision=${gha}{GIT_SHA}"
build_time_ldflag="releaseBuiltAt=${gha}{BUILD_TIME}"

expect_count() {
	local expected="$1" literal="$2" actual
	actual="$(grep -Fxc -- "$literal" "$workflow" || true)"
	if [[ "$actual" != "$expected" ]]; then
		echo "publication workflow expected $expected exact occurrence(s), found $actual: $literal" >&2
		exit 1
	fi
}

expect_count 1 '    branches: [main]'
expect_count 1 '  IMAGE: ghcr.io/e6qu/intraktible'
expect_count 1 '          - arch: amd64'
expect_count 1 '            runner: ubuntu-24.04'
expect_count 1 '          - arch: arm64'
expect_count 1 '            runner: ubuntu-24.04-arm'
expect_count 1 '          provenance: false'
expect_count 1 '          sbom: false'
expect_count 1 "          tags: ${gha}{{ env.IMAGE }}:${gha}{{ needs.metadata.outputs.tag }}-${gha}{{ matrix.arch }}"
expect_count 1 "            --tag \"${gha}{IMAGE}:${gha}{{ needs.metadata.outputs.tag }}\" ${continuation}"
expect_count 1 "            \"${gha}{IMAGE}:${gha}{{ needs.metadata.outputs.tag }}-amd64\" ${continuation}"
expect_count 1 "            \"${gha}{IMAGE}:${gha}{{ needs.metadata.outputs.tag }}-arm64\""
expect_count 1 '    name: Retain 20 release groups'
expect_count 1 "        run: ./scripts/prune-ghcr-images.sh \"${gha}{{ github.repository_owner }}\" \"${gha}{{ github.event.repository.name }}\" \"${gha}{{ needs.metadata.outputs.tag }}\" 20"

if grep -E '(tags:|--tag)[^#]*:(latest|main|sha-)([^[:alnum:]_-]|$)' "$workflow"; then
	echo 'publication workflow must not publish latest, main, or sha-prefixed image tags' >&2
	exit 1
fi
if grep -Eq '(tags:|--tag)[^#]*:[0-9]+\.[0-9]+' "$workflow"; then
	echo 'publication workflow must not publish semantic-version image tags' >&2
	exit 1
fi
if grep -Fq 'paths-ignore:' "$workflow" || grep -Eq '^    tags:' "$workflow"; then
	echo 'every main commit must publish exactly one immutable release group' >&2
	exit 1
fi
if grep -Fq 'mathieudutour/github-tag-action' "$workflow"; then
	echo 'publication must not create an independent semantic release stream' >&2
	exit 1
fi

grep -Fq 'ARG GIT_SHA=dev' "$dockerfile"
grep -Fq 'ARG BUILD_TIME=' "$dockerfile"
grep -Fq "$git_sha_ldflag" "$dockerfile"
grep -Fq "$build_time_ldflag" "$dockerfile"

"$root/scripts/test-container-retention.sh"
echo 'container publication contract passed'
