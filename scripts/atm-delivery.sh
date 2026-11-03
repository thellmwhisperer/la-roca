#!/bin/sh
# ATM delivery for this repository: launch no-mistakes once per attempt, bound by a nonce and its launch receipt,
# and follow that one run by status until it ends. Pull requests target integration and close the run's issue.
# ATM sets ATM_REPORT, ATM_INTENT and ATM_ISSUE. Tested by scripts/atm-delivery-test.py.
no-mistakes init || exit
# One attempt keeps its nonce, generation and receipt's run beside report.json, for a recovery to replay them.
a=${ATM_REPORT%/*}/delivery g=${ATM_REPORT%/*}
g=${g##*/}
[ -s "$a.nonce" ] || od -An -N16 -tx1 /dev/urandom | tr -d ' \n' >"$a.nonce" || exit
n=$(cat "$a.nonce")
# f DOC KEY FIELD prints FIELD of DOC's top-level TOON object KEY, unquoted.
f() { printf '%s\n' "$1" | sed -n "/^$2:/,/^[^ ]/s/^  $3: //p" | tr -d '"'; }
# r DOC prints the run of DOC's launch_receipt when it carries this attempt's nonce and generation.
r() {
  [ "$(f "$1" launch_receipt launch_nonce)" = "$n" ] &&
    [ "$(f "$1" launch_receipt validation_generation)" = "$g" ] && f "$1" launch_receipt run_id
}
# The bound run, or the one whose receipt a launch of this attempt saved before it was interrupted, kept before a replay rewrites that output.
[ -s "$a.run" ] || r "$(cat "$a.out" 2>/dev/null)" >"$a.run"
b=$(cat "$a.run")
# A fresh nonce cancels the branch's active run: launch only past no run, an ended one or this attempt's own.
s=$(no-mistakes axi status) || { printf '%s\n' "$s"; exit 1; }
case $(f "$s" run status) in
completed | failed | cancelled) ;;
"") [ -z "$(f "$s" run id)" ] && printf '%s\n' "$s" | grep -qx 'runs_on_current_branch: 0' ;;
*) [ -n "$b" ] && [ "$(f "$s" run id)" = "$b" ] ;;
esac || { printf '%s\n%s\n' "$s" "delivery: no-mistakes shows a run this attempt cannot prove its own; left untouched"; exit 1; }
# Only the receipt this invocation prints binds the run; stderr stays live.
no-mistakes axi run --launch-nonce "$n" --validation-generation "$g" --intent "$ATM_INTENT" --closes "$ATM_ISSUE" --base-branch integration | tee "$a.out"
id=$(r "$(cat "$a.out")")
[ -n "$id" ] && [ "${b:-$id}" = "$id" ] && echo "$id" >"$a.run" ||
  { echo "delivery: no launch receipt of this attempt binds a run"; exit 1; }
# Only that run decides: its completion with a passing outcome passes; a failure, query error or other answer does not.
# A decision keeps it running while its operator answers: each new gate is shown once.
while s=$(no-mistakes axi status --run "$id") && [ "$(f "$s" run id)" = "$id" ]; do
  case $(f "$s" run status) in
  completed)
    printf '%s\n' "$s" | grep -qxE 'outcome: passed(-with-override|-with-skips)?' || break
    printf '%s\n' "$s"; exit 0 ;;
  pending | running)
    v=$(printf '%s\n' "$s" | sed -n '/^gate:/,/^[^ ]/p')
    [ -z "$v" ] || [ "$v" = "$w" ] || printf '%s\n' "$s"
    w=$v
    sleep 30 ;;
  *) break ;;
  esac
done
printf '%s\n' "$s"
exit 1
