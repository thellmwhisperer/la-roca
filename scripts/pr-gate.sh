#!/usr/bin/env bash
# pr-gate - judge a PR's declared Risk Assessment (issue #457).
#
# Reads the PR body's "Risk Assessment" section and rules:
#   High   -> fail, label risk:high, request owner review. Degraded
#             enforcement (owner decision 2026-09-20): the PR is not
#             converted to draft; the failing check blocks the merge. The
#             owner's risk:accepted label lets a High PR pass.
#   Medium -> pass only with a pasted Aceptación/Acceptance section holding a
#             fenced block with at least one `$ roca` command line and one
#             output line; otherwise fail with the paste message.
#   Low    -> pass.
# risk:high is removed when the level drops. Fork PRs are judged without the
# metadata writes (no token to do them with).
# `--self-test` runs the parsers against embedded fixtures, offline.
set -euo pipefail
cd "$(dirname "$0")/.."

readonly RISK_LABEL=risk:high
readonly ACCEPT_LABEL=risk:accepted
readonly PASTE_MESSAGE='Medium risk: paste branch acceptance output - an Aceptación or Acceptance section with a fenced block showing at least one $ roca command and one output line - in the PR body; editing the body reruns this check.'

# --- parsers (pure, offline, self-tested) ---------------------------------

# Print the declared risk level (high|medium|low) of a PR body. A missing or
# unparseable Risk Assessment section declares High: the safe default.
parse_risk_level() {
  printf '%s\n' "$1" | awk '
    function lvl(s,   t) {
      t = tolower(s)
      if (t ~ /(^|[^a-z])high([^a-z]|$)/) return "high"
      if (t ~ /(^|[^a-z])medium([^a-z]|$)/) return "medium"
      if (t ~ /(^|[^a-z])low([^a-z]|$)/) return "low"
      return ""
    }
    function heading_level(s,   n) {
      if (match(s, /[^#]/)) n = RSTART - 1; else n = length(s)
      return n
    }
    BEGIN { insec = 0; done = 0 }
    {
      line = $0; t = tolower(line)
      if (!insec) {
        if (t ~ /^[ \t]*#{1,6}[ \t]*\**[ \t]*risk assessment/) {
          hdr = heading_level(line); insec = 1
          if (match(t, /risk assessment/)) {
            if (lvl(substr(t, RSTART + RLENGTH)) != "") {
              print lvl(substr(t, RSTART + RLENGTH)); done = 1; exit
            }
          }
        } else if (t ~ /^\**[ \t]*risk assessment\**[ \t]*(:|$)/) {
          hdr = 0; insec = 1
          if (match(t, /:/)) {
            if (lvl(substr(t, RSTART + RLENGTH)) != "") {
              print lvl(substr(t, RSTART + RLENGTH)); done = 1; exit
            }
          }
        }
        next
      }
      # Inside the section: a heading at or below the section heading ends it.
      if (line ~ /^[ \t]*#{1,6}[ \t]/ && heading_level(line) <= hdr) exit
      if (lvl(line) != "") { print lvl(line); done = 1; exit }
    }
    END { if (!done) print "high" }
  '
}

# True when the PR body carries an
# Aceptación/Acceptance section with a fenced block holding at least one
# `$ roca` command line and one output line.
has_acceptance_evidence() {
  printf '%s\n' "$1" | awk '
      {
        line = $0
        if (infence) {
          if (line ~ /^[ \t]*```/) {
            if (sawcmd && sawout) ok = 1
            infence = 0; sawcmd = 0; sawout = 0
          } else if (line ~ /^[ \t]*\$[ \t]+roca([ \t]|$)/) {
            sawcmd = 1
          } else if (sawcmd && line !~ /^[ \t]*\$[ \t]/ && line ~ /[^ \t]/) {
            sawout = 1
          }
          next
        }
        if (inacc && line ~ /^[ \t]*```/) { infence = 1; sawcmd = 0; sawout = 0; next }
        if (inacc && line ~ /^[ \t]*#{1,6}[ \t]/) { inacc = 0; next }
        if (!inacc) {
          t = tolower(line)
          if (t ~ /^[ \t]*#{1,6}[ \t]*\**[ \t]*(aceptaci|acceptanc)/ ||
              t ~ /^\**[ \t]*(aceptaci|acceptanc)e?[ \t]*\**[ \t]*:/) {
            inacc = 1
          }
        }
      }
      END { exit ok ? 0 : 1 }
    '
}

# --- offline evidence ------------------------------------------------------

self_test() {
  local failures=0
  check() { # check <expected> <actual> <name>
    if [[ $2 == "$1" ]]; then
      printf 'ok   %s\n' "$3"
    else
      printf 'FAIL %s: expected %s, got %s\n' "$3" "$1" "$2"
      failures=$((failures + 1))
    fi
  }
  expect_true() {
    if [[ $2 == 0 ]]; then printf 'ok   %s\n' "$1"
    else printf 'FAIL %s: expected pass\n' "$1"; failures=$((failures + 1)); fi
  }
  expect_false() {
    if [[ $2 != 0 ]]; then printf 'ok   %s\n' "$1"
    else printf 'FAIL %s: expected reject\n' "$1"; failures=$((failures + 1)); fi
  }

  local high_body medium_body low_body missing_body empty_section_body
  high_body='## Intent
Ship it.

## Risk Assessment

🚨 High: two criteria remain violated, do not merge.

## Testing
All Low risks were covered.'
  medium_body='## Risk Assessment

Medium: needs a local service, but nothing remote.'
  low_body='## Risk Assessment

Low.'
  missing_body='## Intent

Ship it.

## Testing

Ran the suite.'
  empty_section_body='## Risk Assessment

No level word here at all.

## Testing
Low.'

  check high "$(parse_risk_level "$high_body")" 'emoji High section'
  check medium "$(parse_risk_level "$medium_body")" 'plain Medium section'
  check low "$(parse_risk_level "$low_body")" 'plain Low section'
  check high "$(parse_risk_level "$missing_body")" 'missing section defaults High'
  check high "$(parse_risk_level "$empty_section_body")" 'section without a level defaults High'
  check low "$(parse_risk_level 'risk assessment: low')" 'inline label with level'
  check high "$(parse_risk_level '### Risk Assessment
🚨 High: deeper heading')" 'subsection heading'
  check medium "$(parse_risk_level '## Risk Assessment
Medium
## Risk Assessment
High')" 'first section wins'

  local evidence_full no_output no_roca outside_fence before_heading evidence_with_language
  evidence_full='## Aceptación

```
$ roca --version
roca version 1.74.0
```'
  no_output='## Acceptance

```
$ roca --version
```'
  no_roca='## Acceptance

```
$ make check
ok
```'
  outside_fence='## Acceptance

$ roca --version
roca version 1.74.0'
  before_heading='```
$ roca --version
roca version 1.74.0
```

## Acceptance'
  evidence_with_language='The branch acceptance:

## Acceptance evidence

```sh
$ roca --version
roca version 1.74.0
```'

  expect_true 'fenced command plus output passes' \
    "$(has_acceptance_evidence "$evidence_full"; echo $?)"
  expect_true 'fenced block with language passes' \
    "$(has_acceptance_evidence "$evidence_with_language"; echo $?)"
  expect_false 'fenced command without output rejects' \
    "$(has_acceptance_evidence "$no_output"; echo $?)"
  expect_false 'fence without a roca command rejects' \
    "$(has_acceptance_evidence "$no_roca"; echo $?)"
  expect_false 'unfenced command rejects' \
    "$(has_acceptance_evidence "$outside_fence"; echo $?)"
  expect_false 'fence before the heading rejects' \
    "$(has_acceptance_evidence "$before_heading"; echo $?)"
  expect_false 'no acceptance section rejects' \
    "$(has_acceptance_evidence "$missing_body"; echo $?)"

  if [[ $failures -eq 0 ]]; then
    printf 'self-test: all pass\n'
  else
    printf 'self-test: %s failure(s)\n' "$failures"
    exit 1
  fi
}

# --- gate (network) --------------------------------------------------------

summary() {
  if [[ -n ${GITHUB_STEP_SUMMARY:-} ]]; then
    printf '%s\n' "$*" >>"$GITHUB_STEP_SUMMARY"
  fi
}

pass_gate() {
  printf 'pr-gate: PASS - %s\n' "$*"
  summary "### pr-gate: PASS"
  summary "$*"
  exit 0
}

fail_gate() {
  printf 'pr-gate: FAIL - %s\n' "$*"
  summary "### pr-gate: FAIL"
  summary "$*"
  printf '::error::%s\n' "$*"
  exit 1
}

best_effort() { # best_effort <note> <command...>
  local note=$1 out
  shift
  printf 'pr-gate: %s\n' "$note"
  if ! out=$("$@" 2>&1); then
    printf 'pr-gate: warning: %s failed: %s\n' "$note" "$(printf '%s' "$out" | head -2)"
    summary "warning: $note failed: $(printf '%s' "$out" | head -2) (continuing; the verdict stands)"
  fi
}

gate() {
  local repo=${GITHUB_REPOSITORY:?GITHUB_REPOSITORY required}
  local n=${1:-${PR_NUMBER:?PR_NUMBER required}}
  local fork=${PR_GATE_FORK:-false}
  local action=${PR_GATE_ACTION:-}

  local body author labels owner
  body=$(gh pr view "$n" -R "$repo" --json body --jq '.body // ""')
  author=$(gh pr view "$n" -R "$repo" --json author --jq '.author.login')
  labels=$(gh pr view "$n" -R "$repo" --json labels --jq '.labels[].name' || true)
  owner=$(gh api "repos/$repo" --jq '.owner.login')

  has_label() {
    printf '%s\n' "$labels" | grep -Fxq "$1"
  }
  resolved_owner_candidates() {
    if [[ -n ${PR_GATE_REVIEWER:-} ]]; then
      printf '%s\n' "$PR_GATE_REVIEWER"
    elif [[ $(gh api "repos/$repo" --jq '.owner.type') == Organization ]]; then
      gh api "orgs/$owner/members?role=admin" --jq '.[].login' 2>/dev/null || true
    else
      printf '%s\n' "$owner"
    fi
  }
  owner_accepted_label() {
    local actor candidate candidates
    if [[ $action == labeled ]]; then
      [[ ${PR_GATE_LABEL_NAME:-} == "$ACCEPT_LABEL" ]] || return 1
      actor=${GITHUB_ACTOR:-}
    else
      actor=$(gh api --paginate --slurp "repos/$repo/issues/$n/events?per_page=100" \
        --jq 'add | sort_by(.created_at, .id) | reduce .[] as $event (null; if $event.event == "unlabeled" and $event.label.name == "risk:accepted" then null elif $event.event == "labeled" and $event.label.name == "risk:accepted" then $event.actor.login else . end) // empty' 2>/dev/null || true)
    fi
    [[ -n $actor ]] || return 1
    candidates=$(resolved_owner_candidates)
    while IFS= read -r candidate; do
      [[ -n $candidate && $candidate == "$actor" ]] && return 0
    done <<<"$candidates"
    return 1
  }
  if [[ $action == synchronize || $action == edited ]] && has_label "$ACCEPT_LABEL"; then
    if [[ $fork != true ]]; then
      best_effort "removing stale $ACCEPT_LABEL" \
        gh pr edit "$n" -R "$repo" --remove-label "$ACCEPT_LABEL"
    fi
    labels=$(printf '%s\n' "$labels" | grep -Fxv "$ACCEPT_LABEL" || true)
  fi
  # Degraded enforcement (owner decision 2026-09-20): the gate never touches
  # draft state; a GITHUB_TOKEN could not anyway (actions/toolkit#1165).
  # The repository owner is a user or an organization; ask the first resolved
  # owner candidate who is not the PR author. vars.PR_GATE_REVIEWER, when
  # set, names the reviewer explicitly.
  request_owner_review() {
    local candidates c
    candidates=$(resolved_owner_candidates)
    while IFS= read -r c; do
      [[ -n $c ]] || continue
      if [[ $c != "$author" ]]; then
        best_effort "request review from $c" \
          gh api --method POST "repos/$repo/pulls/$n/requested_reviewers" \
            -f "reviewers[]=$c"
        return 0
      fi
    done <<<"$candidates"
    printf 'pr-gate: note: the only owner candidate is the PR author; review request skipped\n'
    summary "note: review request skipped - the resolved owner candidate list ($candidates) is the PR author"
  }
  drop_high_label() {
    has_label "$RISK_LABEL" || return 0
    [[ $fork == true ]] && return 0
    best_effort "removing $RISK_LABEL" \
      gh pr edit "$n" -R "$repo" --remove-label "$RISK_LABEL"
  }

  local level
  level=$(parse_risk_level "$body")

  case $level in
    low)
      drop_high_label
      pass_gate "Risk Assessment is Low."
      ;;
    medium)
      drop_high_label
      if has_acceptance_evidence "$body"; then
        pass_gate "Risk Assessment is Medium with pasted branch acceptance."
      fi
      fail_gate "$PASTE_MESSAGE"
      ;;
    high)
      if has_label "$ACCEPT_LABEL" && owner_accepted_label; then
        pass_gate "Risk Assessment is High; $ACCEPT_LABEL is set by the owner."
      fi
      if [[ $fork != true ]]; then
        if ! has_label "$RISK_LABEL"; then
          if ! gh label list -R "$repo" --json name --jq '.[].name' 2>/dev/null | grep -Fxq "$RISK_LABEL"; then
            best_effort "creating label $RISK_LABEL" \
              gh label create "$RISK_LABEL" -R "$repo" --color D93F0B \
                --description "PR body declares High risk"
          fi
          best_effort "labelling $RISK_LABEL" \
            gh pr edit "$n" -R "$repo" --add-label "$RISK_LABEL"
        fi
        request_owner_review
      fi
      fail_gate "Risk Assessment is High: this PR cannot merge. Fix the findings so the risk drops to Low or Medium, or ask the owner to add $ACCEPT_LABEL."
      ;;
  esac
}

case ${1:-} in
  --self-test) self_test ;;
  "") gate ;;
  *) gate "$1" ;;
esac
