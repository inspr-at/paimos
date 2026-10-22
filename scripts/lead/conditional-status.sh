# SPDX-License-Identifier: AGPL-3.0-only
# LEAD cancellation guards (AEON-1345). Source with S set to the harness root.
# Installed only by the coordinator, together with the dispatch/worker patch.

# 0 = readable and not cancelled; 1 = cancelled; 3 = unreadable (defer).
ticket_startable() {
  local st
  st=$(set -o pipefail
    perl -e 'alarm 30; exec @ARGV' "$S/pm" issue get "$1" --json 2>/dev/null </dev/null | python3 -c '
import json,sys
raw = sys.stdin.read(2 * 1024 * 1024 + 1)
try:
    if len(raw) > 2 * 1024 * 1024: raise ValueError()
    st = json.loads(raw)["status"]
    if not isinstance(st, str) or not st.strip() or len(st) > 128 or any(c in st for c in "\r\n\t"): raise ValueError()
    print(st)
except (ValueError, KeyError, TypeError):
    sys.exit(1)
' 2>/dev/null) || { echo unknown; return 3; }
  echo "$st"
  [ "$st" != cancelled ]
}

# claim_ticket KEY MARKER first|round. A first claim is ONE conditional write.
# Rounds never write a status. No retry can accidentally consume another claim.
claim_ticket() {
  local key=$1 marker=$2 mode=$3 st rc
  if st=$(ticket_startable "$key"); then :; else
    rc=$?
    [ "$rc" = 3 ] && { echo "ticket status unreadable: $key; defer"; return 3; }
    echo "$key is cancelled; no claim"; return 1
  fi
  case "$mode" in
    first)
      case "$st" in open|qa) ;; *) echo "$key is $st; first claim requires open or qa"; return 1;; esac
      perl -e 'alarm 30; exec @ARGV' "$S/pm" issue update "$key" --status in_progress --if-status open,qa >/dev/null 2>&1 </dev/null \
        || { echo "ticket claim deferred: $key (conditional transition refused or unreadable)"; return 3; }
      ;;
    round) ;;
    *) echo "invalid claim mode"; return 1;;
  esac
  perl -e 'alarm 30; exec @ARGV' "$S/pm" issue comment "$key" --body-file "$marker" >/dev/null 2>&1 </dev/null \
    || { echo "ticket claim deferred: $key (worker marker failed)"; return 3; }
}

record_stopped_run() {
  mkdir -p "$S/qa" && printf '%s\t%s\t%s\t%s\n' "$1" "$2" \
    "$(perl -MTime::HiRes=time -e 'printf "%.6f", time')" "$3" >> "$S/qa/stopped-runs.txt"
}

# Uses the launcher's existing registered identity and lease. Never logs CLI
# output, which may include private diagnostics. Three bounded attempts.
mark_worker_stopped() {
  local try
  for try in 1 2 3; do
    if perl -e 'alarm 30; exec @ARGV' "$A" harness mark-stopped --project "${PROJ:-AEON}" --session "$SESSION" \
      --agent "${AGENT:-aeon-coordinator}" --worker-lease-file "$ST/lease.key" --reason stopped >/dev/null 2>&1 </dev/null; then
      echo "$(date -u +%FT%TZ) marked stopped $SESSION" >> "$LOG"
      return 0
    fi
    [ "$try" = 3 ] || sleep "$((try*10))"
  done
  echo "$(date -u +%FT%TZ) ESCALATE worker-run $SLUG: mark-stopped failed 3x for $SESSION" >> "$S/escalate.log"
  echo "$(date -u +%FT%TZ) mark-stopped failed 3x; escalated" >> "$LOG"
  return 1
}

# Called immediately before the builder case, after registration and markers.
# Unknown status also refuses; a failed stop record or cleanup never starts work.
worker_start_allowed() {
  local st
  if st=$(ticket_startable "$TICKET"); then return 0; fi
  record_stopped_run "$SLUG" "$(basename "$BRIEF")" "$st" \
    || echo "$(date -u +%FT%TZ) ESCALATE worker-run $SLUG: could not persist stop record" >> "$S/escalate.log"
  mark_worker_stopped || :
  echo "$(date -u +%FT%TZ) $TICKET is $st; builder not started" >> "$LOG"
  echo "$(date -u +%FT%TZ) ESCALATE worker-run $SLUG: $TICKET is $st; builder not started" >> "$S/escalate.log"
  return 1
}
