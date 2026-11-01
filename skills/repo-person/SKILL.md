---
name: repo-person
description: "Use when operating the repo-person CLI."
author: "Alex Gorbatchev"
metadata:
  created_on: 2026-10-29 12:51
  last_modified: 2026-10-29 23:11
  status: current
---

## Execution rules

Read this entire skill before running operational commands. Set `AGENT=1`
on every invocation, or export it for the session. Select commands and
options from this reference. Read exit status as well as stdout; failures
exit nonzero and print `ERR:` diagnostics to stderr in agent mode.

Set `AGENT=0` or leave it unset for human output. Values `1`, `true`, and
`yes` enable agent output, with case and surrounding whitespace ignored.
Operational commands use the arguments and options documented below.

## `repo-person`

Invoke without a subcommand to print root help. All commands accept
`--help` / `-h`; the root also accepts `--version` / `-v`.
Agent help starts with an alert to read this skill. Root and operational
commands accept no positional arguments. Maintenance requires Git remote credentials and Codex authentication. Metadata commands work offline.

| Flag | Short | Type | Default | Behavior |
| --- | --- | --- | --- | --- |
| `--help` | `-h` | `bool` | `false` | Print help for the selected command. |
| `--repo` | `-C` | `string` | `.` | Repository directory; inherited by commands. |
| `--version` | `-v` | `bool` | `false` | Print only the raw version and a newline. |

## `repo-person skill`

Print the Skills CLI install command and skill URL, identically in both modes. Accept no positional arguments. No network or repository access. Output-write failures return an error.

## `repo-person init`

Create `repo-person.toml` in the Git root. Fail if it exists. No positional arguments or command-specific flags.

## `repo-person enable`

Validate configuration and register the canonical repository path in the machine's XDG state directory. Idempotent. Scheduling remains external.

## `repo-person disable`

Unregister the repository locally. Preserve its configuration and logs. Explicit `run` still works.

## `repo-person run`

Load configuration, acquire a repository lock, synchronize clean work, run the optional command, sweep eligible changes, and synchronize again. Dirty, staged, detached, or busy checkouts are preserved. A failed agent does not prevent the final sync but the command reports failure. Logs contain details; failures also write `LOG.md` outside the checkout by default.

| Flag | Short | Type | Default | Behavior |
| --- | --- | --- | --- | --- |
| `--all` | — | `bool` | `false` | Run enabled repositories with up to three workers. |

## `repo-person retry`

Run this repository without its previous failure cooldown. Preserve the filesystem quiet window and safety checks.

## `repo-person status`

Read current filesystem eligibility and prior attempt time without changing observation state. Print repository, running/idle activity, state, latest event file, latest attempt in local `yyyy-mm-dd hh:mm`, and output directory.

## `repo-person logs`

Print the latest maintenance log. Fail if no run log exists. Agent output is in `runs/<id>/events.jsonl`; errors in `stderr.log`, structured results in `result.json` and `outcome.json`.

## `repo-person fingerprint`

Print the content/metadata fingerprint of pending changes, or an empty line for a clean checkout. Used by the agent before starting work.

## `repo-person help [command]`

Print root help with no argument, or supply a space-separated command path,
such as `repo-person help status`. Accept no command-specific flags.

## `repo-person completion`

Print completion group help with no verb. Use one of the four shell commands
below to print a completion script to stdout. Accept no positional arguments
or command-specific flags on the group. Redirect a script to a file to save it.

## `repo-person completion bash`

Print a Bash completion script. Accept no positional arguments. Load with
`source <(repo-person completion bash)` in Bash with `bash-completion` installed.

| Flag | Short | Type | Default | Behavior |
| --- | --- | --- | --- | --- |
| `--no-descriptions` | — | `bool` | `false` | Omit completion descriptions. |

## `repo-person completion zsh`

Print a Zsh completion script. Accept no positional arguments. Enable
completion with `autoload -U compinit; compinit`, then load with
`source <(repo-person completion zsh)`.

| Flag | Short | Type | Default | Behavior |
| --- | --- | --- | --- | --- |
| `--no-descriptions` | — | `bool` | `false` | Omit completion descriptions. |

## `repo-person completion fish`

Print a Fish completion script. Accept no positional arguments. Load with
`repo-person completion fish | source`.

| Flag | Short | Type | Default | Behavior |
| --- | --- | --- | --- | --- |
| `--no-descriptions` | — | `bool` | `false` | Omit completion descriptions. |

## `repo-person completion powershell`

Print a PowerShell completion script. Accept no positional arguments. Load
with `repo-person completion powershell | Out-String | Invoke-Expression`.

| Flag | Short | Type | Default | Behavior |
| --- | --- | --- | --- | --- |
| `--no-descriptions` | — | `bool` | `false` | Omit completion descriptions. |

## Configuration and environment

Read `repo-person.toml` only at the selected repository root. Unknown keys fail. Defaults: `sync=true`, `sweep=true`, empty `after_sync`, `checks`, `scanner`, and `state_dir`. Command values are argv arrays (`checks` is an array of argv arrays), without shell interpolation. `after_sync` runs once between initial sync and sweep, with a ten-minute timeout. An explicit scanner command receives repository, base SHA, and head SHA appended; otherwise Gitleaks runs natively with `--redact` over outgoing commits. Git hooks remain enabled.

The `[agent]` table defaults to `model="gpt-6.1-sol"`, `effort="low"`, `quiet="15m"`, `cooldown="24h"`, `timeout="10m"`, `prompt=""`. Durations must be positive. Effort accepts minimal, low, medium, high, xhigh. An optional prompt path is relative to the repository; otherwise use the bundled prompt. The agent runs Codex exec with no approval prompts, full filesystem access, ephemeral session, JSON events and schema-constrained result. It creates one commit with `<type>(sweep): ...` and `Sweep-Run: <id>`; the runner validates it before publication. An unvalidated agent commit writes `blocked-head`; review `LOG.md`, resolve the commit, and remove that marker only after verification to allow publication. Checks are supplied to the agent in the prompt. Costs depend on the configured Codex model and account.

`XDG_STATE_HOME` must be absolute when set. Default state root is `~/.local/state/repo-person`, with per-repository path hashes. `state_dir` overrides per-repository logs/state; an in-repository path must be Git-ignored. Registry and global three-agent slot locks remain in XDG state. Cycles and batch runs have a twenty-minute work budget plus up to two minutes for final synchronization, including after cancellation. Agent work is deferred unless its full timeout and final-sync allowance fit. Parent interruption avoids failure cooldown; the agent’s own timeout remains a failure. Repository locks use the Git common directory, covering linked worktrees. Upstream rewinds require manual reconciliation. Logs older than fourteen days are pruned during runs; the latest unresolved agent report is retained. New file content/metadata changes bypass an old failure cooldown; `retry` also bypasses it. Codex passes a version startup check before an attempt is recorded. Missing executables and startup exit codes 126/127 avoid cooldown. Git failures include stderr. Cycle output prefixes each repository; successful cycles clear resolved failure logs and retain unresolved agent reports. Forced terminations, including agent timeouts, leave `incomplete.json` and block runs and publication including `retry`; status and cycle logs show the block: stop surviving agents, review the recorded baseline and changes, then remove that marker to resume.

Exit 0 means completed or safely skipped. Exit 1 means configuration, lock, Git, command, agent, or I/O failure. Independent registered repositories continue after another fails. A signal cancels work and terminates agent process groups. `AGENT=1`, `true`, or `yes` selects compact help/errors; result lines are plain text in both modes. `PATH` resolves Git, Gitleaks, Codex, and configured commands. All displayed home paths use `~`.

## Workflow

```sh
export AGENT=1
repo-person init
repo-person enable
repo-person run
repo-person status
repo-person logs
```

Schedule `repo-person run --all` using cron-person after reviewing and authorizing its `.cronrc`. Enable repositories on each machine separately.
