#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
set -euo pipefail

root="$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)"
fixture="$(mktemp)"
trap 'rm -f "$fixture"' EXIT

jq -n '[
  range(0; 22) as $release
  | (("000000000000" + ($release | tostring))[-12:]) as $tag
  | range(0; 3) as $kind
  | {
      id: ($release * 10 + $kind),
      created_at: ("2026-07-" + (("00" + (($release + 1) | tostring))[-2:]) + "T00:00:00Z"),
      metadata: {container: {tags: [
        if $kind == 0 then $tag
        elif $kind == 1 and $release == 21 then empty
        elif $kind == 1 then ($tag + "-amd64")
        else ($tag + "-arm64") end
      ]}}
    }
] + [
  {id: 999, created_at: "2026-08-01T00:00:00Z", metadata: {container: {tags: ["main"]}}},
  {id: 1000, created_at: "2026-08-01T00:00:00Z", metadata: {container: {tags: ["sha-0123456789ab"]}}},
  {id: 1001, created_at: "2026-08-01T00:00:00Z", metadata: {container: {tags: ["1.2.3"]}}},
  {id: 1002, created_at: "2026-08-01T00:00:00Z", metadata: {container: {tags: []}}},
  {id: 1003, created_at: "2026-08-01T00:00:00Z", metadata: {container: {tags: ["000000000021-amd64", "main"]}}}
]' >"$fixture"

selected="$(jq -r --argjson keep 20 -f "$root/scripts/select-obsolete-container-versions.jq" "$fixture" | sort -n | paste -sd, -)"
if [[ "$selected" != '0,1,2,10,11,12,211,999,1000,1001,1002,1003' ]]; then
	echo "retention selector chose unexpected package versions: $selected" >&2
	exit 1
fi

echo 'GitHub Container Registry retention selection passed'
