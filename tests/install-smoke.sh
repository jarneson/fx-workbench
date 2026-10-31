#!/usr/bin/env bash
set -euo pipefail

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
tmp=$(mktemp -d "${TMPDIR:-/tmp}/fx-workbench-smoke.XXXXXX")
trap 'rm -rf "$tmp"' EXIT
export GOMODCACHE=${GOMODCACHE:-$(go env GOMODCACHE)}
export GOCACHE=${GOCACHE:-$(go env GOCACHE)}

run_install() {
  local case_root=$1
  HOME="$case_root/home" \
  GOBIN="$case_root/bin" \
  FX_WORKBENCH_CONFIG_HOME="$case_root/config" \
  FX_WORKBENCH_FX_ROOT="$case_root/fx" \
    "$root/install"
}

clean="$tmp/clean"
mkdir -p "$clean/home"
run_install "$clean" >"$tmp/first.log"
for file in \
  "$clean/bin/fx-workbench" \
  "$clean/bin/fx-principle" \
  "$clean/bin/fx-obs" \
  "$clean/bin/fx-tmux" \
  "$clean/bin/fx-tmux-statusd" \
  "$clean/bin/fx-workbench-cleanup" \
  "$clean/bin/fx-workbench-new-workstream" \
  "$clean/bin/fx-workbench-planner" \
  "$clean/bin/fx-workbench-projects" \
  "$clean/bin/fx-workbench-review-pane" \
  "$clean/config/fx-workbench/tmux.conf" \
  "$clean/config/tuicr/config.toml" \
  "$clean/fx/.fx-workbench-manifest.json" \
  "$clean/fx/agent-profiles/coding.md" \
  "$clean/fx/agent-profiles/planner.md" \
  "$clean/fx/skills/obsidian-planner/SKILL.md" \
  "$clean/fx/skills/worktree-task-launcher/SKILL.md" \
  "$clean/fx/principles/prove-it-works.md"; do
  [[ -f $file ]] || { echo "missing installed file: $file" >&2; exit 1; }
done
[[ $(find "$clean/fx/principles" -type f | wc -l | tr -d ' ') == 13 ]]

chmod 0644 "$clean/bin/fx-tmux"
run_install "$clean" >"$tmp/repeat.log"
[[ -x $clean/bin/fx-tmux ]]
grep -F "Fixed executable mode $clean/bin/fx-tmux" "$tmp/repeat.log" >/dev/null

printf '# local change\n' >> "$clean/config/fx-workbench/tmux.conf"
if run_install "$clean" >"$tmp/config-conflict.log" 2>&1; then
  echo 'installer accepted a differing config file' >&2
  exit 1
fi
grep -F 'refusing to overwrite existing file' "$tmp/config-conflict.log" >/dev/null

after_conflict="$tmp/fx-conflict"
mkdir -p "$after_conflict/home" "$after_conflict/fx/skills/obsidian-planner"
printf 'unmanaged\n' > "$after_conflict/fx/skills/obsidian-planner/SKILL.md"
if run_install "$after_conflict" >"$tmp/fx-conflict.log" 2>&1; then
  echo 'installer accepted a conflicting unmanaged fx asset' >&2
  exit 1
fi
grep -F 'refusing to overwrite unmanaged fx file' "$tmp/fx-conflict.log" >/dev/null
[[ ! -e $after_conflict/bin ]]
[[ ! -e $after_conflict/config ]]

printf 'installer_clean_install=passed\n'
printf 'installer_mode_repair=passed\n'
printf 'installer_config_refusal=passed\n'
printf 'installer_atomic_fx_refusal=passed\n'
