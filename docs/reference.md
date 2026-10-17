# Reference

Installation details, command line, service process, data layout, environment variables and diagnostic reports. Short versions are in the [README](../README.md).

## Install and configure (full)

Prerequisites: adb (Android SDK platform-tools). Windows needs **Windows 10 1803 / Windows Server 2019 or later**. Go is not needed.

There are two ways to run it. Both use the same service process and data.

| Your OS | Recommended | Why |
|---|---|---|
| macOS, Linux | **A. npx** (B works too) | One line, no install step; `npx` starts `node` and then the binary, which is cheap here |
| Windows | **B. native binary** | Avoids `npx.cmd` → `cmd.exe` → `node` → binary for every MCP session; A still works |
| No Node.js | **B**, from a GitHub Release asset | No npm at all |

adb is looked up in this order: `ANDROID_LOG_MCP_ADB` → `PATH` → `platform-tools` under `$ANDROID_HOME` / `$ANDROID_SDK_ROOT` → the usual SDK locations (macOS `~/Library/Android/sdk`, Linux `~/Android/Sdk`, Windows `%LOCALAPPDATA%\Android\Sdk`). Set `ANDROID_LOG_MCP_ADB` in the MCP host's `env` for android-log-mcp if adb is elsewhere.

Whichever way the MCP host starts the MCP process, the service process always runs from its own copy under `<root>/bin/`, started directly (never through node, npx or cmd.exe) and without a console window.

### A. Using npx

Needs Node.js ≥ 16 (only to download and launch). The npm package is a tiny Node launcher; the program is a prebuilt Go binary in the optional dependency `android-log-mcp-<os>-<cpu>` (linux/darwin/win32 × x64/arm64), which the launcher starts directly (no shell) with stdio inherited and `windowsHide`. If the platform package is missing (e.g. installed with `--omit=optional`) it says so; `ANDROID_LOG_MCP_BINARY` can point at a binary you built yourself.

```bash
npx -y android-log-mcp --version                 # check that it runs
npx -y android-log-mcp call logcat_status '{}'   # debugging: call one tool and print the result
```

**macOS / Linux**

Codex (`~/.codex/config.toml`; the Codex CLI and IDE extension share it):

```toml
[mcp_servers.android-log]
command = "npx"
args = ["-y", "android-log-mcp"]
# env = { ANDROID_LOG_MCP_ADB = "/path/to/adb" }
```

Cursor (`~/.cursor/mcp.json` or the project's `.cursor/mcp.json`) and Claude Code (the project's `.mcp.json`):

```json
{
  "mcpServers": {
    "android-log": { "command": "npx", "args": ["-y", "android-log-mcp"] }
  }
}
```

or `claude mcp add android-log -- npx -y android-log-mcp`.

**Windows**

The same configuration works, with `npx` resolving to `npx.cmd`. If the MCP host cannot start `npx` (some MCP hosts do not run `.cmd` files), go through `cmd`:

```toml
[mcp_servers.android-log]
command = "cmd"
args = ["/c", "npx", "-y", "android-log-mcp"]
```

```json
{ "mcpServers": { "android-log": { "command": "cmd", "args": ["/c", "npx", "-y", "android-log-mcp"] } } }
```

Trade-offs on Windows: every MCP session runs a chain of `npx.cmd` → `cmd.exe` → `node` → the binary, i.e. three extra processes between the MCP host and the MCP process and a slower start (npx resolves the package first). Whether `cmd.exe` gets a console window depends on how the MCP host starts it. Path B starts the binary directly.

**Upgrading:** `npx -y android-log-mcp` uses the newest version npx has cached and may not check for a new one every time. To upgrade, pin the version in `args` (`"android-log-mcp@0.9.1"`) and change it on upgrades, or use `android-log-mcp@latest`; then restart the MCP host. The new MCP process replaces an older running service process by itself.

### B. Without npx: the native binary (recommended on Windows)

**Get the binary.** If you have Node.js, let android-log-mcp copy itself to a fixed path:

```bash
npx -y android-log-mcp@latest install
```

It prints the path: `<user cache dir>/android-log-mcp/exe/android-log-mcp[.exe]`, i.e.

| OS | Path printed by `install` |
|---|---|
| Windows | `C:\Users\<you>\AppData\Local\android-log-mcp\exe\android-log-mcp.exe` (`%LOCALAPPDATA%\android-log-mcp\exe\android-log-mcp.exe`) |
| macOS | `/Users/<you>/Library/Caches/android-log-mcp/exe/android-log-mcp` |
| Linux | `/home/<you>/.cache/android-log-mcp/exe/android-log-mcp` (`$XDG_CACHE_HOME` if set) |

`android-log-mcp root --json` also shows it as `exe`. Without Node.js, download the platform tarball from the [GitHub Releases](https://github.com/s1crab/android-log-mcp/releases) (`android-log-mcp-win32-x64-<version>.tgz`, `-darwin-arm64-`, `-linux-x64-`, …) and extract `package/bin/android-log-mcp[.exe]` (`tar -xzf <file>.tgz`, also built into Windows 10 1803+); run `<extracted binary> install` to put it at the fixed path, or keep it wherever you like. Building from source (Go 1.24+): `cd server && go build -o android-log-mcp ./cmd/android-log-mcp`.

**Configure the MCP host** with the absolute path and no args.

Windows, Codex (`%USERPROFILE%\.codex\config.toml`; a TOML literal string `'…'` needs no escaping):

```toml
[mcp_servers.android-log]
command = 'C:\Users\<you>\AppData\Local\android-log-mcp\exe\android-log-mcp.exe'
args = []
```

Windows, Cursor (`.cursor\mcp.json`) and Claude Code (`.mcp.json`); JSON needs doubled backslashes:

```json
{
  "mcpServers": {
    "android-log": { "command": "C:\\Users\\<you>\\AppData\\Local\\android-log-mcp\\exe\\android-log-mcp.exe", "args": [] }
  }
}
```

or, in cmd.exe, `claude mcp add android-log -- "%LOCALAPPDATA%\android-log-mcp\exe\android-log-mcp.exe"`.

macOS / Linux, Codex:

```toml
[mcp_servers.android-log]
command = "/Users/<you>/Library/Caches/android-log-mcp/exe/android-log-mcp"   # Linux: /home/<you>/.cache/android-log-mcp/exe/android-log-mcp
args = []
```

macOS / Linux, Cursor and Claude Code:

```json
{
  "mcpServers": {
    "android-log": { "command": "/Users/<you>/Library/Caches/android-log-mcp/exe/android-log-mcp", "args": [] }
  }
}
```

or `claude mcp add android-log -- ~/Library/Caches/android-log-mcp/exe/android-log-mcp` (Linux: `~/.cache/…`).

**Upgrading:** run `npx -y android-log-mcp@latest install` again (or `<new binary> install`), then restart the MCP host. The fixed path stays the same, so the configuration does not change. A copy that MCP processes are running is replaced safely (on Windows the running file is renamed aside and removed by a later `install` once it has stopped), and the new MCP process replaces the older running service process. Check with `android-log-mcp --version` and `android-log-mcp service status`.

The `exe/` directory holds one binary (about 11 MB), plus renamed copies only while an MCP process still runs them. It lives in the fixed control directory, so `set-root` never moves or deletes it, and it is not counted in the data cap. Delete it to uninstall.

## Command line

| Command | What it does |
|---|---|
| `android-log-mcp` | The MCP process (MCP stdio server; records nothing and forwards every call to the service process, which it starts when needed) |
| `android-log-mcp call TOOL [JSON]` | Call one tool through the service and print the result (like an MCP call, it starts recording the device and waits up to 10 s for the backfill) |
| `android-log-mcp record [--device X]... --for 2h` / `--stop` / (no flag) | Keep recording until now + the duration without tool calls or connected MCP / CLI processes (a pin), stop now, or show what is recorded (see [below](#recording-pins-record-and-record_for)); `--json` for scripts |
| `android-log-mcp export [flags]` | Write the lines a selection matches to a text file (see [Export files](#export-files-logcat_export)); **unredacted unless `--redact`**; `export -h` lists the flags |
| `android-log-mcp report [flags]` | Diagnostic zip (see [below](#diagnostic-reports-and-privacy)); `report -h` lists the flags |
| `android-log-mcp service [status [--json] [--verbose] \| stop [--force]]` | Show or stop the service process; without arguments, run it in the foreground. `stop --force` also ends a service process that holds the lock but does not answer: it is signalled (SIGTERM, then SIGKILL after 10 s; Windows: a stop request, then TerminateProcess) only when it is verified to be the process recorded in `run/service.json` (same executable and start time); unflushed lines can be lost, and the command says so. Exit 1 when the service holds the lock but does not answer |
| `android-log-mcp install [--json]` | Copy this binary to a fixed path (`<user cache dir>/android-log-mcp/exe/`) and print it, for MCP hosts that run the binary directly (recommended on Windows) |
| `android-log-mcp root [--json]` | Print the data root (and the control dir, config file, socket and running service) |
| `android-log-mcp set-root DIR --keep-old\|--delete-old` | Move the data root for every MCP and CLI process (see [below](#data-root-and-set-root)) |
| `android-log-mcp compat [--serial S] [--json\|--raw]` | Read-only probe of a device for the logcat / ps / pm / dumpsys features this tool needs, evaluated with the recorder's own parsers. Exit 0 = supported (possibly degraded), 1 = a required feature is missing, 2 = usage or adb error |
| `android-log-mcp --offline DIR [call …]` | Open a dataset read-only, e.g. an unzipped report's `store/`: no adb, no service, never records, prunes or creates files |

## Output format (`fmt=1`)

Every tool response and every CLI command prints the same grammar, built for agents (parse first, then read; see [GOALS.md](GOALS.md)). The version is `fmt=1` in the header; it changes only with a breaking format change. Adding optional fields/keys does not change `fmt`; only breaking changes (removing/renaming/changing the meaning of an existing field or token) bump `fmt`. Readers must ignore unknown keys.

- **Line 1** is `ok`, `empty` or `error`, then `key=value` fields (values with a space are quoted). Times are ISO-8601 with offset (device time; `host_now` and `tz` only with `verbose=true`), numbers are plain integers, durations end in `_s` / `_ms`, sizes in `_bytes`. `order=arrival|newest_first` says how a list is sorted; `clock=observed|estimated|unknown` is the reading of device now (`dev_now`, `offset_s`).
- **Rows** of `logcat_search`: `time level pid/tid process tag: message`. No column before the tag contains a space (`process` is `system_server` or `uid:system`; `?` = unknown). A continuation line of a multi-line message starts with `| `; a repeated line is `^ rep=N`; a date change is `--- 2026-10-15 ---`.
- **Truncation:** one line `truncated <what>=N exact=0|1 ... next: param=value [| param=value]` at the place where output was cut; `exact=0` means N is a lower bound. `next:` is always executable (a parameter of the same tool or a command line), never a placeholder. **One command per `next:`:** a CLI `next:` line holds exactly one command, never `a | b`, `;` or `&&`; when there are several alternatives (for example one `record --stop --device X` per pinned device) each is its own line starting with `next:`, so pasting the output into a shell can never run them all. A destructive option such as `set-root --delete-old` is never suggested in a `next:` line. In a tool response, `a=1 | a=2` separates alternative parameters of a read-only tool (choose one).
- **Empty:** `empty ... matched=0 scanned=N exact=1 range=<first>/<last>` plus `hint code=<code> next: ...`: `scanned` and `range` tell "no data in range" from "the filter matched nothing".
- **Summary head** of a wide `logcat_search` (more matches than shown): one line `summary sampled=N top3_share=P% tags=a:12%,b:6% msgs=515x"tag: message with #"` of at most 300 characters. **`sampled` is not `matched`:** it is how many matches the digest was computed from (the first 2000), so it is at most `matched`; the percentages are shares of `sampled`, and carry a `~` (`~12%`) when `sampled` is smaller than `matched` (an approximation of the whole result). `top3_share` is the combined share of the three most frequent tags. `msgs` are the most repeated message templates of the same sample (digits are `#`; at most 256 distinct templates are tracked, so a template that first appears after that is not counted). There is no separate `msgs_sampled`: tags and messages are always computed from the same `sampled` matches. `matched` and `exact` in the header remain the count of the whole range.
- **`view="summary"`** prints `levels`, `tags` (with `more=N` hidden tags), `procs`, `msgs` lines with counts, `crashes=N anrs=N next: logcat_crashes ...`, and up to 5 `latest_errors` rows; all counts are over the whole range (`matched`), not a sample.
- **Quoting (one rule).** A string value is printed bare unless it is empty or contains a space, a control character, `"`, `=` or `\`. Every other value is a JSON string (`"..."` with `\\`, `\"`, `\n` escapes): any JSON parser reads it back. So a Windows path is always quoted and escaped, with or without spaces (`root="C:\\Users\\me\\alm"`), and a POSIX path without special characters is bare. Parameters inside `next:` are always JSON strings. In `next:` command lines the paths are quoted for the host shell (single quotes on Linux/macOS, double quotes on Windows when needed); several paths are never joined by commas, one line each.
- **Times** in `proc`, `window` and `stored` lines are device-local to the second without offset (`2026-10-10T03:07:04`; `tz` and `tz_offset` are on the `device` line); times in headers and rows keep their millisecond and offset form.
- **Error:** `error code=<code> ... next: ...`. Codes are a closed set: `bad_param`, `missing_param`, `bad_type`, `bad_json`, `bad_filter`, `bad_time`, `bad_cursor`, `cursor_device`, `host_time`, `empty_window`, `unknown_param`, `unknown_tool` (lists the tools), `unknown_device`, `unknown_crash`, `several_devices`, `no_device`, `device_offline`, `device_unauthorized`, `device_state`, `device_identifying`, `record_for_unavailable`, `disk_low`, `export_failed` and `exists` (`logcat_export`; `exists` only for the CLI's `--out`), `service_unreachable` (a tool that is not retried after a lost connection, `logcat_export`). A cursor that points before the oldest stored line (`after=` older than the retention) is answered from the oldest line, with `note code=cursor_pruned boot=<boot> first=<seq> using=oldest`: `first` is the oldest stored seq, so the agent can tell how much was dropped. `note code=...` lines carry facts that explain the result (for example `shares_uid`, `lag`, `backfill`, `no_callback_durations`, `no_displayed_hot`).
- **Verbose:** `verbose=true` adds what rarely changes the next call: `host_now`, `tz`, the full clock-jump list, the shared-uid package list, linger and lifetime.
- **`logcat_crashes` detail** shows the 20 lines before a crash by default; `before_lines` (1-100) shows more.

The CLI uses the same lines: `android-log-mcp call` writes exactly the tool response to stdout and nothing to stderr (except `service state=started pid=... version=...` when it started the service); usage mistakes are one `error code=usage ... next: <command>` line (exit 2), the full usage only for `-h`; `record`, `service status` and `report` print `key=value` lines (`service status --verbose` for the full text); `--json` outputs and `root` are unchanged apart from added numeric fields.

## Service process

```
MCP host ─stdio─ android-log-mcp (MCP process) ─┐
MCP host ─stdio─ android-log-mcp (MCP process) ─┼─ AF_UNIX socket ─ android-log-mcp service (one per user)
android-log-mcp call / report / record / set-root ┘                    records devices in use, answers every tool call
```

- **One service process per user.** It holds `run/service.lock` in the control directory `<user cache dir>/android-log-mcp`, records devices into the data root and answers all tool calls. MCP and CLI processes never open data files (except `--offline`), so there is one cache, one writer and one sweep however many MCP processes are running.
- **Started on demand.** An MCP or CLI process that finds no service process copies its own binary to `<root>/bin/<version>-<hash>/` (so the npx cache can change while the service runs; the running and at most one other version are kept) and starts it detached. If that is impossible, the MCP process runs the service **in its own process** and serves the same socket for the other MCP and CLI processes; when it exits, they start a new one. After 3 service process exits within a minute an MCP process stops respawning and runs it in-process.
- **Recording starts on use.** An idle service runs no adb at all, not even `adb track-devices`. A device starts being recorded on the first log tool call that uses it (`logcat_status`, `logcat_search`, `logcat_crashes`, `logcat_app_launches`, also through `call`), on `report` and on `record --for`. A call without `device` uses the single connected device; with several connected it starts none and lists them; more than `ANDROID_LOG_MCP_MAX_DEVICES` recorded devices is an error naming the variable.
- **Backfill.** A newly started device is first read from its log buffer: the whole buffer when nothing of this boot is stored, else from the last stored line (`logcat -T`) with deduplication. Before starting, the service reads the buffer's last line (`logcat -d -t 50`); the backfill has caught up once that line is stored. Every call waits for that at most 10 s; if it has not caught up, the answer starts with "backfill in progress (1,423 of ~27,000 lines; earliest …)" (the total is estimated from the number of entries logd holds, `logcat -S`; after `-T` only the lines so far are shown) and `logcat_status` returns no cursor and asks to call again. While backfilling, the service runs no other adb command on that device (`ps`, `pm`, `dumpsys dropbox`); crash details from DropBox are fetched once it has caught up. If the device clock went back since the last stored line (e.g. an emulator snapshot), or a clock jump lies among the stored lines that may still be in the device buffer, the whole buffer is read without `-T`, already stored lines are skipped (compared with counts, so a line logged twice is kept twice), and `logcat_status` says so.
- **Per-device idle stop (linger).** A device stops being recorded when it has had no log tool call for `max(10 min, 2 × the device's log buffer span)`, at most 1 h (10 min until the span is measured). A call that waits (`wait_ms`) counts as use while it waits. Starting or stopping MCP processes plays no role. `ANDROID_LOG_MCP_LINGER` overrides it (`30m`; `0` = stop after each call; `forever`). The service process reads it when it starts, so set it in the MCP host's `env` for android-log-mcp. When the device is used again, recording resumes with `logcat -T` and deduplication; anything the buffer dropped meanwhile is marked as a gap.
- **Service lifetime.** The service process never exits while MCP or CLI processes are connected (MCP processes and running `call` / `report` / `record --for` CLI processes; idle, it runs no adb). It exits 30 s after none is connected, there is no active pin and no recorded device; short queries such as `service status`, `root` or `record` (state) do not keep it running. A call that finds no device to record stops `adb track-devices` again 30 s later.
- **Crash recovery.** If the service process dies, every MCP and CLI process reconnects on its next call, one of them starts a new service process and the call is retried (all tools are read-only). With an active pin, a connected MCP process restarts it immediately. Devices started by tool calls are not carried over a crash or a version handoff (the next call starts them again); pinned devices are. Recording resumes with `logcat -T` and deduplication; anything the device buffer dropped meanwhile is marked as a gap.
- **Versions.** An MCP or CLI process replaces the service process only if the service process is **older**: it asks it to hand off, waits for it to stop and starts its own version. A newer service process is used as is. Both sides also carry a protocol version: an older MCP or CLI process that the newer service process still supports works normally (`logcat_status` says so); an incompatible one fails with an error asking you to upgrade and never replaces the newer service process.
- **Visibility.** The MCP process's stderr names the service process it uses (pid, version, protocol, root, recording). `logcat_status` starts with `service: … pid N, version V, protocol P` followed by `root:`, `linger:`, pin and `service log:` lines (on later calls of the same MCP process, unchanged `root:`, `linger:`, `lifetime:`, `service log:`, `adb:` and `store:` lines are replaced by one `unchanged since shown earlier in this session: …` line; `verbose=true` shows them); `service status` and `record` show each device as recording or idle, its backfill progress, idle deadline and pin.
- **Local only.** The socket is `<control dir>/run/service.sock` (directory 0700) and is 0600 on Linux/macOS; the service also checks that the peer runs as the same user (`SO_PEERCRED` / `LOCAL_PEERCRED`). On Windows the run directory and socket get a protected ACL for the current user and SYSTEM only. No TCP and no network listener. When that path would be longer than 100 bytes (a very long home or cache path) the socket is `/tmp/alm-<uid>/<hash>.sock` instead (`%TEMP%\alm-<hash>\s.sock` on Windows); it never moves into the cache dir.
- **Service log:** `<root>/log/service.log` (1 MiB plus one rotated copy) and `log/crash.log` for Go runtime crashes; both count towards the total size cap.
- **adb:** the service process runs `adb start-server` outside its own process group and never kills the adb server; its adb children (track-devices, logcat, shell) end with it, and with the last recorded device.

### Recording pins: `record` and `record_for`

To keep a device recorded without tool calls (e.g. overnight, or while reproducing a bug by hand), pin it:

```bash
android-log-mcp record --for 2h              # the single connected device; or --device X (repeatable)
android-log-mcp record                       # what is recorded: recording/idle, backfill, idle deadline, pin
android-log-mcp record --stop                # stop now and remove the pin
```

Agents do the same with `logcat_status(record_for="30m")` (`"0"` stops).

- The deadline is `max(current deadline, now + duration)`: a shorter `--for` never shortens it. Durations need a unit (`30m`, `2h`, `1h30m`); at most `24h`, longer is an error. The usual size and disk limits apply.
- `record --for` starts the service process detached when none runs (never inside the CLI process) and returns without waiting for the backfill. `--stop` stops recording at once.
- Pins are stored in `<control dir>/run/record.json` (format `"v": 1`, written atomically), so they survive a service process crash, a version handoff, `set-root` and a machine restart, and they keep the service process running until the deadline (it is started again by the next MCP process, CLI process or `record`). Deadlines more than 24 h ahead are cut to 24 h with a warning; a file with an unknown format version is ignored, never overwritten, and `record --for` refuses until it is removed.
- Devices that log tool calls started are listed in `<control dir>/run/wanted.json` (the writing service process's pid and start time, the write time, and per device the time since its last log tool call and its linger; written a second after a change, and at most once a minute for a call that only refreshes the time). After a crash, a kill or an automatic replacement, the next service process resumes the devices that were still within their linger (and within one hour of the write), each for the rest of its linger; `ANDROID_LOG_MCP_LINGER=0` disables this. An idle exit, `service stop` and `service stop --force` remove the file, so a cold start records nothing until a call. The file is optional and additive: unknown keys and a garbage file are ignored.
- **Crash protection.** If the service crashes 3 times within a minute while a pin is active, the pin is suspended (`suspended: crash-loop`). Independently, a pin allows at most 10 automatic restarts per service version (`suspended: crash-budget`). Both are stored, shown with the last error by `record`, `service status` and `logcat_status`, and cleared by a new `record --for`; a newer version taking over gets a fresh budget.

## Data root and `set-root`

The data root is `<user cache dir>/android-log-mcp` by default (Linux `~/.cache/android-log-mcp`, macOS `~/Library/Caches/android-log-mcp`, Windows `%LocalAppData%\android-log-mcp`). There is no per-MCP-host setting: every MCP process, CLI process and the service process use the same root.

```bash
android-log-mcp root [--json]                     # where the data is
android-log-mcp set-root /data/alm --keep-old     # move; leave the old root as it is
android-log-mcp set-root /data/alm --delete-old   # move; remove our directories from the old root
```

- Exactly one of `--keep-old` / `--delete-old` is required (no prompts). `DIR` must be absolute, writable, new or empty (or an earlier android-log-mcp root), and must not be the current root, inside it, or a parent of it.
- The running service takes `run/config.lock`, waits for running tool calls, stops recording, carries each device's state into the new root (seq numbers and the `logcat -T` resume point, so recording continues without a gap or duplicates), writes `<control dir>/config.json` atomically and resumes recording there. **Any failure before that commit is rolled back** and the old root stays in use. With no service running, the command does the switch itself (it does not start a service).
- `--delete-old` removes only `v<N>/`, `tmp/`, `bin/`, `log/`, `export/` (and `run/` outside the control directory), then the old root if it is empty. What cannot be removed (for example an open file on Windows, which always includes the running service's own binary under the old root's `bin/`) is reported with exit status 3 and retried when the service next starts.
- **History recorded before a switch stays in the old root and is not queried.** Open it with `--offline <old root>` if you kept it.
- The control directory (socket, locks, `config.json`) never moves.

## Data layout and limits

```
<control dir> = <user cache dir>/android-log-mcp   (fixed; also the default data root)
  run/                     0700: service.lock, service.json, service.sock, spawn.lock, config.lock
  config.json              {"root": "<dir>"}, written by set-root (absent = the default root)
  exe/android-log-mcp[.exe]  written only by `android-log-mcp install` (fixed path for MCP hosts)
<root>/v1/<device-id>/     device data, format 1: seg-*.jsonl (16 MiB segments), meta.json, crashes.jsonl, procs.jsonl, packages.json
<root>/tmp/                scratch files, removed after 1 hour
<root>/bin/<version>-<hash>/android-log-mcp   service binaries: the running one and at most one other
<root>/log/                service.log (1 MiB + one rotated copy), crash.log
<root>/export/             0700: export files (export-<device-id>-<host time>.txt), kept 7 days, newest 20, 256 MiB in total
```

- **On-disk store format is internal and versioned** (`v1/…`, `seg-*.jsonl`, `meta.json`, aux jsonl). Do not parse it as a public API: fields and layout can change between minors. The same additive rule applies: new optional fields do not change the version, readers ignore unknown keys, and a new binary reads old files without error. For a human-readable dump use `logcat_export` / `android-log-mcp export`; for a shareable package use `report` (or `--offline` on an unpacked report's `store/`).
- **Device IDs are stable:** `ro.serialno` for a physical device (the same over USB and Wi-Fi), `avd-<name>` for an emulator. Two devices online at once that resolve to the same ID are filed separately (`<id>~<adb serial>`) and `logcat_status` explains it.
- Anything else under the root (pre-0.4 `<serial>/` directories, `legacy/`, …) is ignored: not read, not counted, not deleted. A leftover `owners/` directory from 0.4 development builds is removed.
- **Retention:** 1 day (`ANDROID_LOG_MCP_MAX_AGE`) and 512 MiB per device (`ANDROID_LOG_MCP_MAX_DEVICE_MB`); the oldest segments go first. A segment is deleted once its newest line is past retention, and an active segment spans at most a quarter of the retention, so logs are kept for at most about 1.25 × the retention. Crash, DropBox and ended device-process records follow the same retention. DropBox entries dated more than the retention before device now are not imported.
- **Total cap:** 2 GiB for everything the tool manages under the root (all devices and format versions, `tmp/`, `run/`, `bin/`, `log/`, `export/`; `ANDROID_LOG_MCP_MAX_TOTAL_MB`). The oldest files go first; each current-format device keeps its newest segment.
- **Export files:** `export/` has its own fixed retention (7 days, the newest 20 files, 256 MiB in total, oldest first; constants, not settings). Only `export-*.txt` and leftover `.export-*.tmp` files are ever deleted there. A file that cannot be removed (open in an editor on Windows) is retried at the next sweep.
- **Sweep** (in the service, at start and every minute; never with `--offline`): device directories that are not being recorded lose expired segments (by the same stamps in `meta.json` the store uses, not by file mtime; the mtime only for a store without stamps) and are removed when nothing in them is newer; then the total cap applies. Directories whose `writer.lock` is held by another android-log-mcp process (e.g. an older version) are left alone. Only known file names are deleted.
- **Low disk space:** below 512 MiB free (`ANDROID_LOG_MCP_MIN_FREE_MB`) recording pauses and a gap is recorded; it resumes with `logcat -T` once twice that is free. `logcat_status` shows why.
- **Memory:** per device, the newest 20,000 lines (at most about 16 MiB) stay in memory; older history is read from disk in blocks into a 32 MiB LRU cache, released after 2 minutes without queries. `logcat_status` shows the current usage.
- Aux files (`procs.jsonl`, `crashes.jsonl`) are compacted about 30 s after start and then every 10 minutes.

## Environment variables

Set them in the MCP host's `env` for android-log-mcp; recording settings are read by the service process when it starts.

| Variable | Meaning |
|---|---|
| `ANDROID_LOG_MCP_ADB` | Path to adb |
| `ANDROID_LOG_MCP_BINARY` | npm launcher only: use this binary instead of the platform package |
| `ANDROID_LOG_MCP_LINGER` | How long a device keeps being recorded after its last log tool call: a duration (`30m`; a bare number is seconds), `0` (stop after each call) or `forever`. Default `max(10m, 2 × the device's log buffer span)`, at most `1h` |
| `ANDROID_LOG_MCP_CALL_WAIT` | Deprecated and ignored (a note on stderr): calls wait for the backfill themselves, up to 10 s |
| `ANDROID_LOG_MCP_MAX_DEVICES` | Devices recorded at once (default 4); starting one more is an error |
| `ANDROID_LOG_MCP_MAX_AGE` | Retention, default `1d`: a Go duration (`72h`, `90m`) or days (`1d`, `7d`, `1.5d`). Invalid or non-positive values log a warning and use the default. `logcat_status` shows the effective value |
| `ANDROID_LOG_MCP_MAX_DEVICE_MB` | Per-device cap in MiB (default 512) |
| `ANDROID_LOG_MCP_MAX_TOTAL_MB` | Cap for everything under the root in MiB (default 2048; `0` = no cap) |
| `ANDROID_LOG_MCP_MIN_FREE_MB` | Pause recording below this much free disk space in MiB (default 512; `0` = off) |
| `ANDROID_LOG_MCP_MEM_RECORDS` | Newest lines kept in memory per device (default 20000) |
| `ANDROID_LOG_MCP_MEM_MB` | Byte cap of that in-memory window in MiB (estimated; default 16) |
| `ANDROID_LOG_MCP_HISTORY_CACHE_MB` | Per-device cache of decoded history blocks in MiB (default 32, LRU over ~512 KiB blocks) |
| `ANDROID_LOG_MCP_REPORT_HINT` | Developer option, off by default (`1` / `true` turns it on); read by the MCP and CLI processes only. When a connect or start error means android-log-mcp itself is broken, the error text then also tells the agent to **ask the user first**, and only if they agree to run `android-log-mcp --offline <root> report --logs-only --out <file>`; the user reviews the zip and submits the issue by hand. It changes only error text: nothing is collected or sent, and the tool has no network code |
| `ANDROID_LOG_MCP_IDLE_RELEASE` | Release the history cache after this long without queries (default `2m`; `0` = never) |

`ANDROID_LOG_MCP_HOME` and `--home` were removed in 0.4.0; if either is set, the program exits with an error saying so.

## Export files (`logcat_export`)

`logcat_export` writes the lines a `logcat_search` selection matches (no `limit`) to `<root>/export/export-<device-id>-<YYYYMMDD-HHMMSS>.txt` (host time; `-2`, `-3` when the name is taken; with `redact=true` the device id in the name is replaced by `dev-<8 hex of a hash>`). The tool has no output path: its arguments come from a model that reads untrusted log text. Files are written under a temporary name and renamed when complete, mode 0600 in a 0700 directory (a protected ACL on Windows), and retained as described under [Data layout and limits](#data-layout-and-limits). A call is not retried after a lost connection to the service (a retry would write a second file): it answers `error code=service_unreachable`.

```
ok view=export device=avd-api35 file=/home/me/.cache/android-log-mcp/export/export-avd-api35-20261017-091205.txt bytes=1843201 lines=10234 matched=10234 exact=1 range=<first>/<last> order=arrival redacted=0 clock_jumps=0 expires=<host time> <clock fields> fmt=1
note code=unredacted msg="..." next: redact=true
```

- `lines` is what was written (a multi-line message counts per line), `matched` the lines of the selection in the window (`exact=0` when the rest could not be counted in 100 ms). They differ only when truncated, which prints `note code=truncated msg="the file is INCOMPLETE: ..."` and `truncated newer=N exact=E cap=max_mb|time_budget [scan_stopped_ms=20000] next: after=<cursor> [| max_mb=<2x>]`. `time_budget`: the scan ran 20 s (a redacted export of a large window runs about 15,000 lines/s, an unredacted one about a million); `after=` continues with the next file, and the concatenated bodies equal one export.
- No match: `empty view=export matched=0 scanned=N ...` with the same hints as `logcat_search`; no file is written.
- Errors: the codes of `logcat_search`, plus `disk_low free_mb= need_mb= next: max_mb=<smaller>` (free space below `max_mb` + the recording guard's 512 MiB) and `export_failed msg= next: android-log-mcp service status`.
- **File format.** `# ` header lines (`key=value`: version, device, model, api, boot, time zone, host and device now, window, parsed filter, `redacted=0|1`, a `# WARNING: not redacted` line or a note on the redaction limits, clock jumps, recording gaps, crash ids, caps), `# ---`, then the body, then `# end lines= bytes= matched= exact= truncated=0|max_mb|time_budget redaction_hits= last_cursor=`. Body lines are `logcat -v threadtime -v year` in device time: `2026-10-17 09:12:03.456  1234  1250 E OkHttp  : message`; `F` is fatal; a multi-line message repeats the prefix on every line; there is no process or package column. A `--------- clock jump seq= source= delta_s= device_before= device_after= host=` line is written before the first line at or after the jump's seq. Crash, ANR and DropBox text is not in the file (header: counts and ids; use `logcat_crashes`). A file without the `# end` footer is not complete.
- **Redaction.** `redact=false` (default) writes messages as recorded. `redact=true` uses the rules and limits of [`report`](#diagnostic-reports-and-privacy): message text only (tag, process, package, pid and device properties are not redacted; the adb address is never written), the filter text and gap reasons in the header too, placeholders valid within one file. `report` stays redacted by default (`--no-redact` turns it off); `logcat_export` is raw by default (`redact=true` turns it on).

- **Newest file in `logcat_status`.** `logcat_status` adds one fact line, `export file= bytes= age_s= complete=1|0`, for the newest file in `export/` (`complete=0`: truncated or without the `# end` footer). Nothing is shown while `export/` has none.

### `android-log-mcp export` (CLI)

```bash
android-log-mcp export --device avd-api35 --package com.foo --min-level W --from 14:00 --out /home/me/logs/foo.txt
android-log-mcp --offline R/store export --out /home/me/logs/offline.txt     # dataset of a report
```

The CLI process sends the selection to the service through the `export` op (started detached when none runs, like `report`), which writes the file with the code of the tool and prints the same `fmt=1` response on stdout (exit 1 on an error). Like the tool, the first call for a device starts recording and waits up to 10 s for the backfill; the call is not retried after a lost connection.

| Flag | Meaning |
|---|---|
| `--device` `--filter` `--package` `--tag` (repeatable) `--min-level` `--from` `--to` `--after` `--before` | As the tool parameters |
| `--max-mb N` | Size cap, 1..512 (default 64) |
| `--out FILE` | Where to write. An **absolute** path in an existing directory; refused **only** inside android-log-mcp's own entries of the data root or the control directory: `v<N>/`, `run/`, `tmp/`, `bin/`, `log/`, `export/` and `config.json` (symlinks are resolved). Any other path, including other files and directories in the root such as `<root>/notes/x.txt`, is allowed. Without `--out` the file goes to `<root>/export/` like the tool's (kept 7 days). A file at `--out` is the user's: it is never swept and the response has no `expires=` |
| `--force` | Overwrite an existing `--out` file (otherwise `error code=exists`). The file is replaced atomically |
| `--redact`, `--redact-extra RE` (repeatable), `--keep-domain D` (repeatable), `--keep-private-ip` | Redaction as in `report`; the last three need `--redact` |
| `--offline DIR` (global flag) | Reads the dataset in this process, with no adb and no service. `--out` is **required** and may not be inside DIR: a dataset is never written to |

**Redaction flags are asymmetric on purpose.** The artefact that goes to other people is redacted unless you say otherwise; a log file for your own review is raw unless you ask:

| | Default | Switch |
|---|---|---|
| `report` | redacted | `--no-redact` turns redaction off |
| `export` / `logcat_export` | **not redacted** (`# WARNING` header line, `redacted=0`, `note code=unredacted`) | `--redact` / `redact=true` turns it on |

**Counting.** In an export response `lines` is the number of physical lines written (a multi-line message counts once per line), `matched` the number of records of the selection in the window (a record with a multi-line message counts once).

## Diagnostic reports and privacy

To send a problem to a maintainer, pack the diagnostics into a zip. **Sensitive values are redacted by default.**

```bash
npx -y android-log-mcp report                                   # every recorded device, last 2 hours
npx -y android-log-mcp report --serial emulator-5556 --package com.foo \
    --since 30m --note "crashes after tapping Login" -o foo-crash.zip
npx -y android-log-mcp report --from "2026-10-10 14:00" --to "2026-10-10 14:30" --note @repro.txt
npx -y android-log-mcp report --package com.foo --am-start com.foo/.MainActivity   # LAUNCHES the app, see below
```

| Flag | Meaning |
|---|---|
| `--serial S` | Only this device (repeatable); default every device with recorded data |
| `--since 2h` / `--from` / `--to` | Window in device-local time (same syntax as the tools). Default last 2 hours; no `--to` = up to the newest line |
| `--package P` | Only this app's lines (by package / device process name), system lines that name it (`START u0`, `am_proc_start`, `Displayed`, …), and its crashes, device processes and DropBox entries |
| `--max-size 50MB` | Cap on uncompressed log data; the oldest lines are dropped first (counted in `manifest.json` and marked as a "report size cap" gap) |
| `--note TEXT` / `--note @FILE` | Repro steps or description, stored as `note.txt` (also redacted) |
| `--am-start PKG/Activity` | Run `am start -W -n` and record its output. **This launches the app**, so it only runs when you ask; it then waits up to 8 s for the launch logs to be recorded |
| `--redact-extra REGEX` | Additional redaction rule (RE2, repeatable; group 1 if present, else the whole match), type `custom` |
| `--no-redact` | No redaction (the manifest warns) |
| `--keep-private-ip` | Keep private addresses (10/8, 172.16/12, 192.168/16, link-local, ULA) |
| `--keep-domain D` | Keep domain `D` and its subdomains (repeatable), e.g. your own API host; not written to the manifest |
| `--no-adb` | Do not use adb: no live device info, DropBox or `adb devices -l` |
| `--logs-only` | No recorded device data: only `diagnostics/` (the service log's last 1 MiB, `crash.log`, `service.json`, `diagnose.txt` with who holds the service lock and whether the socket exists, and `adb devices -l`; all redacted). A small zip to attach to an issue; works with `--offline DIR` while the service is down. `--offline` reports also run `adb devices -l` unless `--no-adb` |
| `-o FILE` | Output (default `./android-log-mcp-report-<time>.zip`; the path goes to stdout, a summary to stderr) |

The service process builds the report (it has the data and adb); with no service process running, the CLI process starts it detached. The service process starts recording the requested devices (with `--serial`, else the single connected one) and backfills their buffers first, waiting at most 10 s; if that is not enough, the manifest warns "backfill incomplete". `--no-adb` and `--offline DIR` use only what is stored. It only reads the data and, apart from `--am-start`, only runs `getprop`, `date`, `cat /proc/sys/kernel/random/boot_id` and `dumpsys dropbox --print` on the device.

| Path in the zip | Content |
|---|---|
| `manifest.json` | Version, time, parameters (values of `--note` / `--redact-extra` / `--keep-domain` omitted), per-device window and line count, file sizes, trimming, hits and distinct values per redaction rule, warnings |
| `note.txt` | The `--note` text |
| `diagnostics/` | `service.log` (last 1 MiB, redacted), `crash.log`, `service.json`, `diagnose.txt`, `adb-devices.txt` (`adb devices -l`), whichever exist |
| `store/v1/<device-id>/` | The recorded store, trimmed to the window/package and renumbered 1..N; load it with `android-log-mcp --offline <unzipped>/store` |
| `devices/<serial>/device.json` | Fingerprint, model, manufacturer, Android version, SDK, time zone and offset, boot_id, device–host clock difference (from the recording if the device is offline, with the source noted) |
| `devices/<serial>/logcat/<boot>.txt` | The same lines as `logcat -v threadtime -v epoch -v uid -v usec -v printable` text, one file per boot |
| `devices/<serial>/dropbox/<tag>.txt` | DropBox entries in the window (`data_app_crash`, `data_app_native_crash`, `data_app_anr`, `SYSTEM_TOMBSTONE`, `system_app_*`) |
| `devices/<serial>/am-start.txt` | `--am-start` output |

```bash
unzip foo-crash.zip -d r
android-log-mcp --offline r/store call logcat_crashes '{"package":"com.foo"}'
```

### What is redacted

Values are replaced by `[REDACTED:<type>#n]`. The same original value gets the same number across the whole zip (logs, crashes, DropBox, note), so lines can still be correlated; numbers come from a random HMAC key that is used once and discarded, so originals cannot be recovered.

| Type | Rule |
|---|---|
| `token` | `Authorization:` / `Proxy-Authorization:` headers, values after `Bearer` / `Basic` |
| `jwt` | `eyJ….eyJ….…` JWTs |
| `secret` | Common key prefixes: `ghp_` `gho_` `ghu_` `ghs_` `ghr_` `github_pat_` `npm_` `sk-` (incl. `sk-proj-`) `AKIA` / `ASIA` `AIza` `xox?-` `glpat-`, PEM private-key headers |
| `credential` | `key=value`, `key: value` and JSON fields whose key contains api_key / secret / token / password / passwd / access_key / private_key / credential, and URL query parameters with those names |
| `url_param` | URL query `sig` `signature` `code` `key` `auth` `ticket` `sid` `session` `otp` `nonce` and AWS / GCS signature parameters |
| `password` | `scheme://user:password@host` |
| `cookie` | `Cookie:` / `Set-Cookie:` values |
| `email` | Email addresses |
| `phone` | Mainland China mobile numbers (11 digits, incl. `+86`) and international numbers with `+` |
| `id_card` | 18-digit PRC resident ID numbers (birth date and GB 11643 check digit) |
| `bank_card` | 13–19 digit card numbers (Luhn + issuer ranges; bare digit runs must be 15/16/19 digits and not look like counters) |
| `device_id` | IMEI, IMSI, ICCID, MEID, serial, Android ID, advertising ID (`key=value`) |
| `ipv4` / `ipv6` | IP addresses (not 127.0.0.1, ::1, 0.0.0.0); `--keep-private-ip` keeps private ones |
| `mac` | MAC addresses (not all-zero / broadcast) |
| `username` | The user name in a home path: `/home/<user>`, `/Users/<user>`, `C:\Users\<user>` (either slash type, JSON-escaped backslashes, any case for the Windows form); the rest of the path stays |
| `home_dir` | The home directory of the user who runs the report, wherever it is (also in the command line and warnings of `manifest.json`) |
| `domain` | Host names, see below; `--keep-domain` keeps listed domains |

`domain`:
- **Hosts in URLs** of any network scheme (`https://`, `wss://`, `ftp://`, `postgres://`, …): only the host is replaced; scheme, `user:`, port, path and query stay (`GET https://api.foo.com:8443/v1/login?page=2` → `GET https://[REDACTED:domain#1]:8443/v1/login?page=2`). IP literals count as `ipv4`/`ipv6`. Non-network schemes (`content://`, `file://`, `android.resource://`, `intent:`) and package-name authorities (`app://com.foo.app:10108`) are left alone.
- **Bare host names** outside URLs (`Unable to resolve host "cdn.foo.com"`): only lower-case names with at least two labels whose last label is in a curated list of public TLDs (com, net, org, io, co, ai, app, dev, cn, uk, jp, de, …) and whose first label is not a reverse-DNS / property prefix (`com`, `org`, `android`, `java`, `ro`, `persist`, …). Package and class names, stack frames, file names (`Foo.java`, `libc.so`, `base.apk`), versions, properties and intent actions are therefore not touched. TLDs that are also common file extensions or words (`.so` `.md` `.py` `.sh` `.id` `.in` `.it` `.to` …) are not in the list, so such hosts are only redacted inside URLs.

**Not redacted:** timestamps, pid, tid, uid, level, tag, package and device process names, device properties, and package / class / file / property names in messages. Rules exclude epoch timestamps and ns counters, hex values and addresses, register and memory dumps, native and Java stack frames, version numbers, HIDL library names, `schedstat`-style number lists, and numbers whose key looks like time / size / bytes / count / duration. All rules have unit tests and are regression-tested over real API 29 logs.

**Redaction is pattern-based and cannot be complete**: custom token formats, names, postal addresses or random strings without a key are not recognized. Review the zip before sharing it, add rules with `--redact-extra` if needed, and send `--no-redact` reports only to people you trust.

## Third-party reference

`server/third_party_ref/` holds AOSP and Android Studio source excerpts (filter syntax, logcat format, DropBox) under Apache-2.0, used only as a reference for porting; they are not compiled.

## 中文摘要

- **安装与配置（两种方式）**：A. **npx**（macOS / Linux 推荐）：MCP 宿主的 command 设为 `npx`，args 为 `["-y", "android-log-mcp"]`；Windows 上每个会话会经过 `npx.cmd` → `cmd.exe` → `node` 三层额外进程（位于 MCP 宿主和 MCP 进程之间），启动较慢；升级时修改 `@版本` 或用 `@latest` 并重启 MCP 宿主。B. **直接运行原生二进制**（Windows 推荐）：运行 `npx -y android-log-mcp@latest install`，把 MCP 宿主的 command 设为它打印的绝对路径（Windows：`%LOCALAPPDATA%\android-log-mcp\exe\android-log-mcp.exe`），args 留空；没有 Node.js 时可从 GitHub Release 下载对应平台的 tgz 并解压出 `package/bin/` 下的二进制；升级时再运行一次 `install` 并重启 MCP 宿主。Windows 需要 **Windows 10 1803 / Server 2019 及以上**。
- **后台服务进程（0.4 起）**：每个用户只有一个后台服务进程负责录制设备并回答所有工具调用；MCP 宿主启动的 MCP 进程不录制，只把调用转发给后台服务进程，需要时自动启动它。后台服务进程崩溃后 MCP 进程和 CLI 进程自动重连并重启它；新版 MCP 进程会替换旧版后台服务进程，旧版 MCP 进程在协议兼容时直接使用新版后台服务进程。`android-log-mcp service status|stop` 查看或停止。只使用本机 AF_UNIX socket（仅当前用户可访问），没有网络监听。
- **按需录制（0.5 起）**：后台服务进程空闲时不运行任何 adb；某台设备第一次被日志工具调用（含 `call`、`report`、`record`）时才开始录制，并先从设备缓冲区回填（最多等 10 秒；没追上时回答以 "backfill in progress" 开头，`logcat_status` 不返回游标，请再调用一次）。每台设备在最后一次调用之后 `max(10 分钟, 2 × 该设备日志缓冲区跨度)`（最多 1 小时，`ANDROID_LOG_MCP_LINGER` 可调）停止录制；MCP 进程启动或退出不影响录制。有 MCP 进程或 CLI 进程连接时后台服务进程不退出；没有连接、没有保持录制、没有正在录制的设备时 30 秒后退出。连接多台设备且没有指定 `device` 时不启动任何设备。
- **保持录制**：`android-log-mcp record --for 2h`（不需要工具调用也持续录制，单次最多 24 小时，截止时间只会延后）、`record --stop`（立即停止）、`record`（查看状态）；agent 用 `logcat_status(record_for="30m")`。保存在 `run/record.json`，崩溃、版本切换、`set-root` 和重启后仍然有效；1 分钟内崩溃 3 次或同一版本自动重启超过 10 次会暂停（显示原因和最后的错误），新的 `record --for` 清除暂停。
- **数据根目录**：默认 `<用户缓存目录>/android-log-mcp`（`android-log-mcp root` 查看）；用 `android-log-mcp set-root <绝对路径> --keep-old|--delete-old` 为所有 MCP 进程和 CLI 进程统一切换，失败会回滚；切换前的历史留在旧目录里，不再被查询。
- **上限**：每台设备保留 1 天、最多 512 MiB；整个根目录最多 2 GiB；剩余磁盘低于 512 MiB 时暂停录制。
- **诊断包**：`android-log-mcp report` 生成 zip（没有后台服务进程时会以 detached 方式启动它并先回填），默认打码 token、密码、邮箱、手机号、身份证、银行卡、设备 ID、IP、MAC、域名等；打码基于模式，分享前请检查。
- **输出格式 `fmt=1`**：所有工具和 CLI 输出第一个词是 `ok|empty|error`，其后是 `key=value`；时间为带偏移的 ISO-8601；每个截断、空结果和错误都带可直接执行的 `next:`；错误码是封闭集合（见上文）。
