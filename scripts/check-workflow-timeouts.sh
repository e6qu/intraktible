#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
set -euo pipefail

failed=0
for workflow in .github/workflows/*.yml; do
	if ! awk -v workflow="$workflow" '
		function check_job() {
			if (job != "" && timeout == "") {
				printf "%s: job %s has no timeout-minutes\n", workflow, job > "/dev/stderr"
				failed = 1
			}
		}
		/^jobs:[[:space:]]*$/ { in_jobs = 1; next }
		in_jobs && /^[^[:space:]]/ { check_job(); in_jobs = 0; job = ""; next }
		in_jobs && /^  [A-Za-z0-9_-]+:[[:space:]]*$/ {
			check_job()
			job = $1
			sub(/:$/, "", job)
			timeout = ""
			next
		}
		in_jobs && job != "" && /^    timeout-minutes:[[:space:]]*[0-9]+[[:space:]]*$/ {
			timeout = $2 + 0
			if (timeout < 1 || timeout > 15) {
				printf "%s: job %s timeout-minutes is %d, want 1..15\n", workflow, job, timeout > "/dev/stderr"
				failed = 1
			}
		}
		END { check_job(); exit failed }
	' "$workflow"; then
		failed=1
	fi
done

exit "$failed"
