`repo-person` maintains Git repositories across development machines: it synchronizes clean checkouts and uses Codex to finish coherent changes that have been idle long enough to be forgotten.

# What It Does

- **Synchronizes safely:** fetches the current upstream, fast-forwards or rebases nonconflicting local commits, and pushes inspected commits.
- **Finishes quiet work:** asks Codex to inspect changes, run checks, make one bounded repair attempt if necessary, and commit.
- **Keeps failures visible:** writes per-attempt events, results, stderr, and an unresolved `LOG.md`.
- **Works across repositories:** registers local checkouts and limits concurrent agent jobs to three per user on a machine.

# How It Works

- Create `repo-person.toml` in a repository and review its settings.
- Run maintenance directly, or register the checkout and schedule `repo-person run --all` through `cron-person`.
- Each cycle synchronizes clean work, runs an optional repository command, attempts eligible changes, and synchronizes again.
- A failed agent attempt is reported without suppressing the final synchronization or stopping other repositories.

# How it Really Works

- Changes become eligible after fifteen minutes of filesystem inactivity by default. Modification and inode-change times cover writes, creation, deletion, and renaming. Running agent processes are not an eligibility signal.
- Codex runs with `gpt-6.1-sol`, low reasoning, a ten-minute timeout, and full filesystem access without approval prompts. Its authenticated account bears the usage cost. The prompt limits work to this repository and its logs; filesystem access is not sandbox-enforced.
- The agent runs required checks, makes at most one bounded repair attempt, and creates exactly one `<type>(sweep): ...` commit with a `Sweep-Run:` footer. Git hooks stay enabled. The runner validates the commit and runs Gitleaks before publication.
- Dirty checkouts are not stashed for synchronization. Staged changes, active Git operations, and detached checkouts are skipped. Nonconflicting divergence is rebased; conflicts abort the rebase and preserve the original checkout. Upstream rewinds require manual reconciliation. Pushes never force-update the remote.
- A forced termination, including an agent timeout, leaves `incomplete.json` and blocks further runs and publication, including `retry`. The block appears in status and cycle logs. Stop any surviving agent, review its recorded baseline, changes, and logs, then remove the marker to resume.
- An unvalidated agent commit writes `blocked-head` and is withheld from subsequent publication. Review `LOG.md`, resolve the commit, and remove that marker only after verifying the result.
- State defaults to `$XDG_STATE_HOME/repo-person` or `~/.local/state/repo-person`, under a hash of the checkout path. `status` prints the exact directory. Each `runs/<id>/` contains `events.jsonl`, `stderr.log`, `result.json` when produced, and `outcome.json`. Old run directories expire after fourteen days, except the latest unresolved agent report. The latest cycle has `latest.log` and `latest.json`.
- Git reads have a thirty-second timeout; fetch, push, and mutating operations have five minutes, bounded by their phase budget. Cancellation terminates process groups, escalating after one second. Git failures include stderr.
- Codex must pass a startup version check before an attempt is recorded. Missing executables, startup exit codes 126/127, and parent interruption do not impose a failure cooldown.
- Cycle output identifies each repository. A successful cycle clears resolved failure logs while retaining unresolved agent reports.
- The same failed diff waits twenty-four hours before another agent attempt. Changed content or metadata resets that cooldown. `retry` bypasses the cooldown while retaining the quiet window.
- Cycles and registered batch runs have a twenty-minute work budget plus up to two minutes for final synchronization. Agent work is deferred unless its configured timeout and final-sync allowance fit the remaining budget. Parent cancellation does not impose an agent failure cooldown; exhausting the agent’s own timeout does. Repository locks cover linked worktrees; three shared agent slots also cover separate invocations. Registration is local to each machine. Normal Git push rejection handles another machine advancing the remote.
- Commands return exit 1 for failures and exit 0 for completion or safe skips. `AGENT=1` uses compact help and error output. Dates use local `yyyy-mm-dd hh:mm`; displayed home paths use `~`.

# Installation

Release artifacts are not published yet. The release configuration produces versioned Linux and macOS archives for amd64 and arm64, with checksums, under [GitHub Releases](https://github.com/alexgorbatchev/repo-person-cli/releases).

# Setup

- Install [Git](https://git-scm.com/) and configure the checkout's remote tracking branch and noninteractive credentials for synchronization.
- Install and authenticate [Codex CLI](https://developers.openai.com/codex/cli/) for agent attempts.
- Install [Gitleaks](https://github.com/gitleaks/gitleaks) for the default outgoing scanner, or configure a shared scanner command. Missing scanners fail closed when there are outgoing commits.
- Use [cron-person](https://github.com/alexgorbatchev/cron-person-cli) when scheduled execution is desired. It owns schedule authorization and cron installation.

# Quick Start

```sh
cd /path/to/repository
repo-person init
# Review and commit repo-person.toml as part of your repository setup.
repo-person enable
repo-person run
repo-person status
repo-person logs
```

Sample Output:

```text
[~/projects/example] Repository: ~/projects/example
[~/projects/example] Started: 2026-10-29 13:00
[~/projects/example] clean
[~/projects/example] Output: ~/.local/state/repo-person/<checkout-hash>
```

For all locally registered checkouts:

```sh
repo-person run --all
```

Add this to a scheduler-owned `.cronrc`, then register and authorize that schedule through `cron-person`:

```cron
*/30 * * * * repo-person run --all
```

# Options & Flags

| Flag | Short | Default | Description |
| :--- | :--- | :--- | :--- |
| `--repo <path>` | `-C` | `.` | Repository directory |
| `--help` | `-h` | `false` | Display command help |
| `--version` | `-v` | `false` | Print the raw version |

`repo-person run`:

| Flag | Short | Default | Description |
| :--- | :--- | :--- | :--- |
| `--all` | — | `false` | Maintain locally registered repositories |

# Configuration

`repo-person.toml` is read from the selected Git root. Unknown keys fail validation. Commands are argv arrays executed from the checkout without shell interpolation.

```toml
sync = true
sweep = true
checks = [["just", "check"], ["just", "test"]]
# after_sync = ["just", "setup"]
# state_dir = ".tmp/repo-person" # Must be Git-ignored inside the checkout.
# scanner = ["bash", "/path/to/shared/gitleaks/check.sh", "outgoing"]

[agent]
model = "gpt-6.1-sol"
effort = "low"
quiet = "15m"
cooldown = "24h"
timeout = "10m"
# prompt = "prompts/maintenance.md"
```

`after_sync` runs once between initial sync and sweep, even when nothing was fetched, with a ten-minute timeout. Configured checks supplement the repository's required checks in the agent prompt. A custom scanner receives repository path, base revision, and head revision after its configured arguments. Without one, the runner invokes native `gitleaks git --redact` over the outgoing range. Configure your shared scanner when policy also covers commit messages or destination-specific rules.

`disable` removes a checkout from the machine registry; it preserves config and logs and does not prevent an explicit `run`. `retry` runs this repository again without waiting for an existing failure cooldown. `fingerprint` supports the agent's preflight observation. `skill` prints the agent usage-skill installation command and URL.

# Name Rationale

`repo-maintenance` became `repo-man`, short for repository maintenance. Following `cron-person`'s commitment to making Unix naming marginally less embarrassing, it became `repo-person`.

# License

[MIT](LICENSE) © Alex Gorbatchev
