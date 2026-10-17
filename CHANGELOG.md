# Changelog

All notable changes to android-log-mcp are documented in this file.

The format is based on [Keep a Changelog 1.1.0](https://keepachangelog.com/en/1.1.0/), and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html). Before 1.0, breaking changes bump the minor version (see [AGENTS.md](AGENTS.md)). Entries marked **Breaking** need action or change behavior on upgrade; upgrade notes are only here, never in the README. Every user-facing change adds an entry under `[Unreleased]`; a release moves them under the new version, and the GitHub Release notes are taken from that section.

## [Unreleased]

### Changed

- After a crash, a kill or an automatic replacement (a newer MCP or CLI process, a hung service process), the new service process resumes recording the devices that were still within their linger time (default 10 min to 1 h after the last log tool call), even if the MCP or CLI process that used them has gone ([#226](https://github.com/s1crab/android-log-mcp/issues/226)). Each device stops at the end of that linger as before; an idle exit, `service stop` and `service stop --force` restore nothing, nor does a cold start; `ANDROID_LOG_MCP_LINGER=0` disables it. The devices are carried in the new optional file `run/wanted.json` (the writer's pid and start time, the write time, per device the idle time and linger; unknown keys and a garbage file are ignored, and older binaries never read it).

### Fixed

- A call made right after the service went away (for example an MCP process whose in-process service stopped) no longer fails with `write: broken pipe`: a write to a closing service socket is treated as a lost connection, so the client reconnects, starts or takes over the service and retries the read-only call ([#237](https://github.com/s1crab/android-log-mcp/issues/237)).
- A native crash is no longer listed twice when its tombstone lines are read after the log line `Fatal signal` was already stored: the tombstone, `am_crash` and the DropBox entry now attach to the existing crash (same id, same row) instead of creating a second one a moment later ([#233](https://github.com/s1crab/android-log-mcp/issues/233), [#225](https://github.com/s1crab/android-log-mcp/issues/225)).
- The sources of one ANR (`am_anr`, the `ANR in` block, the DropBox entry) are paired in the order they arrived, no longer by a 120 s window ([#225](https://github.com/s1crab/android-log-mcp/issues/225), second of three changes). A dump that takes longer than two minutes (slow emulator, loaded device) no longer produces a second ANR; the pairing gives up only after 10 minutes. Two real ANRs of one process are never merged, whatever the gap and wherever the service process restarts, and a DropBox entry or block with another reason does not attach to an unrelated ANR. `crashes.jsonl` crashes of kind `anr` written by this version carry the optional field `n` (ordinal within the process lifetime); older binaries ignore it. Crashes written by 0.9.x keep the old pairing windows.
- A DropBox entry no longer attaches to the crash or ANR of an earlier process lifetime of the same pid ([#225](https://github.com/s1crab/android-log-mcp/issues/225), third of three changes). The entry is placed in the process lifetime that owned its pid at its device time, and a crash from another lifetime is never its source, however close in time; a Java or native crash entry no longer attaches to "the same pid and process whatever the time" across a restart of the app. The remaining 15 s, 60 s and 120 s matching windows are gone for crashes written by this version: sources of one crash are paired by source (one per kind), process lifetime and, for an ANR, the 10 minute cap; a logcat crash that arrives after its DropBox-only twin is adopted by the same rules, so a DropBox entry written more than two minutes after the logcat lines of the ANR is still one crash. Crashes written by 0.9.x (no `ev` field) keep the old windows, only for that data. No new field and no format change.

## [0.9.1] - 2026-10-17

Patch release: the on-disk store is declared an internal format, and `meta.json` is trimmed with retention ([#173](https://github.com/s1crab/android-log-mcp/issues/173)). No tool or CLI change; segment files are unchanged and older binaries read `meta.json` as before. Platform status: Windows and macOS are covered by CI only (unit tests with the fake adb) and are **unverified** with a real or emulator-attached adb. The emulator checks are summarised in [validation/0.9.1/README.md](validation/0.9.1/README.md).

### Changed

- Performance: redaction (`report`, `logcat_export redact=true`, `export --redact`) is several times faster on typical logs ([#177](https://github.com/s1crab/android-log-mcp/issues/177)). Each message is scanned once and a rule's regular expression only runs when the text can contain what it matches. On the real API 29 captures, per-line redaction went from about 1.6 MiB/s to about 49 MiB/s (about 15,600 to about 476,000 lines/s); logs with many sensitive values, and one `Text` call on a whole multi-line text that contains matches, gain less. The output is byte-identical (the old implementation is kept as a test reference), so the numbers of the `[REDACTED:<type>#<n>]` placeholders do not change.
- Docs: the on-disk store (JSONL segments, `meta.json`) is declared an internal, versioned format; agents and users should use `report` and `logcat_export` rather than parsing it ([#173](https://github.com/s1crab/android-log-mcp/issues/173)).
- The `cursor_pruned` note of `logcat_search` now says which line "oldest" is: `note code=cursor_pruned boot=<boot> first=<seq> using=oldest` ([#173](https://github.com/s1crab/android-log-mcp/issues/173)).
- `meta.json` is trimmed together with retention ([#173](https://github.com/s1crab/android-log-mcp/issues/173)): when segments are pruned, `seg_times`, the `clock_jumps` before the first retained seq and the `sessions` that ended before it are dropped, so a long-lived device no longer grows its meta forever. The session that covers the first retained seq is always kept, because the boot of a seq is the last session starting at or below it. `meta.json` is written compactly (no indentation). What tools read is unchanged; older binaries read the file as before.

### Fixed

- A crash or ANR is no longer listed twice when the service process restarts between two of its sources (version handoff, hung-service replacement, `set-root`, linger exit): the detector now looks for the open crash in the stored crash list instead of its own memory, so a late `am_anr`, `ANR in` block, `am_crash`, tombstone or DropBox entry attaches to the crash it belongs to ([#225](https://github.com/s1crab/android-log-mcp/issues/225), first of three changes). A DropBox entry fetched while the detector writes the same crash no longer drops the detector's update (the write now re-reads the latest stored version under one lock), and a pid reused by a new process lifetime inside the pairing window is two crashes, not one. The pairing windows themselves are unchanged. `crashes.jsonl` gains optional fields (`inc`, `incw`, `ev`) on crashes written by this version; no format version changes, older binaries ignore them and list the same crashes, and files written by 0.9.0 and 0.8.1 list identically.

## [0.9.0] - 2026-10-17

Highlights: the `logcat_export` tool and `android-log-mcp export` CLI ([#172](https://github.com/s1crab/android-log-mcp/issues/172)), and a round of store and device robustness fixes (failed writes, torn tails, follower takeover, retention after a clock correction, recorder restarts). Segment files are unchanged; `meta.json` gains an optional `seg_times` field that older binaries ignore.

Platform status: Windows and macOS are covered by CI only (unit tests with the fake adb); they were not tested with a real or emulator-attached adb for this release and are **unverified**. The emulator checks are summarised in [validation/0.9.0/README.md](validation/0.9.0/README.md).

### Added

- **`logcat_export`** ([#172](https://github.com/s1crab/android-log-mcp/issues/172)): writes the lines a `logcat_search` selection matches to a text file for a human to review (`logcat -v threadtime -v year` lines in device time, a `# ` header, `--------- clock jump` markers, a `# end` footer) and returns only its path and counts, never log lines. Files go to `<root>/export/` (the tool has no output path). Limits: `max_mb` (default 64, max 512) and a 20 s time budget; a cut export says so (`note code=truncated`, `truncated newer=N cap=max_mb|time_budget next: after=<cursor>`) and `after=` writes the rest. Not redacted by default (`note code=unredacted`); `redact=true` masks the message text with the rules of `report`, `redact_extra` adds RE2 patterns. A redacted export of a large window is slow (about 15,000 lines/s) and usually ends at the time budget. The CLI command follows in a later change.
- **`android-log-mcp export`** ([#172](https://github.com/s1crab/android-log-mcp/issues/172)): the CLI side of `logcat_export`, through a new service op. The same selection flags as the tool, `--out FILE` (an absolute path; refused inside the data root's or control directory's own entries), `--force` to overwrite, `--redact` / `--redact-extra` / `--keep-domain` / `--keep-private-ip`; `--offline DIR export` needs `--out`. **Not redacted unless `--redact`**, the opposite of `report` (`report --no-redact`); both are documented side by side in [docs/reference.md](docs/reference.md#android-log-mcp-export-cli).
- `logcat_status` lists the newest export file: `export file= bytes= age_s= complete=1|0`.
- A data root now has an `export/` directory (mode 0700, a protected ACL on Windows) for the export files of the coming `logcat_export` ([#172](https://github.com/s1crab/android-log-mcp/issues/172)). It is bounded from the start: files older than 7 days, more than 20 files or more than 256 MiB in total are removed (oldest first), it counts toward the total cap, and `set-root --delete-old` removes it.

### Changed

- Tool annotations: `logcat_export` is not read-only and not idempotent, and `logcat_status` is no longer annotated read-only (it starts recording, and `record_for` pins it). An MCP host that auto-approves read-only tools may now ask first. The tool definition carries the new optional fields `writes` and `not_idempotent`; an older MCP process ignores them.
- [docs/GOALS.md](docs/GOALS.md) principle 5 now reads: a report meant for other people is redacted by default; a log file exported for the developer's own review is written as recorded unless redaction is requested, and says so. `report` still redacts by default.
- The MCP process no longer retries `logcat_export` after a lost connection to the service (a retry could write a second file); it answers `error code=service_unreachable`.
- The `logcat_export` tool description is about 1 KB shorter in `tools/list` (details stay in docs/reference.md) and a repeated sentence of `logcat_search` was folded: `out.tools_list.bytes` 13,730 → 13,097. No behaviour change.

### Fixed

- The sweep of device directories nobody is recording (at service start and every minute) no longer deletes segments by file mtime ([#218](https://github.com/s1crab/android-log-mcp/issues/218)). After a host clock correction the mtimes can be far in the past while the data is recent, and the sweep removed it although the store's own retention would have kept it (#210). The sweep now uses the store's rule: the `seg_times` stamps in `meta.json` (a future or absurdly old stamp is set to now and persisted; the mtime is used only for a store that has no stamps yet), expires oldest first, removes the newest segment only when all have expired, and orders the total-size cap by stamp. A directory with a live `writer.lock` is still left alone.
- The same ANR record fed twice (same pid and seq) no longer opens a second, partial ANR in `logcat_crashes` ([#175](https://github.com/s1crab/android-log-mcp/issues/175)): since the one-to-one pairing of #111 a repeat found no ANR lacking its source. The repeat now leaves the ANR unchanged; a genuine second ANR of the process stays separate. Repeats of the same text under a different seq are not recognised by this rule.
- `Store.RewriteAux` now refuses a store that is not the writer, like `AppendAux` and `CompactAux`, and a failed rewrite of an aux file (`crashes`, `procs`, `dropbox_seen`) removes its `*.jsonl.tmp` instead of leaving it to eat disk space ([#196](https://github.com/s1crab/android-log-mcp/issues/196)).
- A log line stamped after the year 2262 (a device clock set far ahead, or a corrupt line) is no longer turned into a negative time ([#195](https://github.com/s1crab/android-log-mcp/issues/195)): the epoch form (`seconds * 1e9` wrapped) and the `-v year` date form are rejected as not a log line when the time does not fit 64-bit nanoseconds.
- Store hardening ([#188](https://github.com/s1crab/android-log-mcp/issues/188)): `after=MaxUint64` made `Scan`, `Count`, `CountMatch` and `AggClasses` return everything (`after+1` wrapped to 0); they now return nothing. `nil` class ids meant "every class" for scans but "no class" for the block-count path of `CountMatch`/`AggClasses`; they now mean "every class" everywhere (an empty set still matches nothing). Latent: the search tools already clamp cursors and pass a non-nil set.
- Retention no longer trusts segment file mtimes or a future start ([#184](https://github.com/s1crab/android-log-mcp/issues/184), [#186](https://github.com/s1crab/android-log-mcp/issues/186), [#189](https://github.com/s1crab/android-log-mcp/issues/189)). After the host clock was set back (RTC correction), a segment stamped in the future was never rotated or deleted, and `OpenDir` could prune with the wrong "now"; frequent restarts could also keep a segment past `MaxAge/4` forever. The store now persists a host-time first/last stamp per segment in `meta.json` (`seg_times`), clamps stamps to now (so a future segment ages from the correction), re-anchors stamps older than max(30 days, 10 × MaxAge) to now (never an immediate mass delete after a clock fix), and keeps the segment start across restarts so the `MaxAge/4` rotation holds. A store without `seg_times` (older version) gets its stamps filled from the file mtimes on first write. Segment files are unchanged and an older binary ignores the new meta field and keeps using mtimes.
- `android-log-mcp export --out` could not be stopped from writing into the data root's own entries (`v<N>/`, `run/`, `tmp/`, `bin/`, `log/`, `export/`, `config.json`) by spelling them in another letter case on a case-insensitive file system (Windows, macOS) ([#205](https://github.com/s1crab/android-log-mcp/issues/205)). The guard now compares by file identity after resolving symlinks (and by name ignoring case on those systems).
- A follower store re-reads `meta.json` whenever its mtime or size changed, not only when the mtime is newer, so a host clock set back no longer leaves read-only consumers and promoted followers on stale clock jumps and sessions ([#183](https://github.com/s1crab/android-log-mcp/issues/183)). A failed `meta.json` save (a Windows follower holding the file, a full disk) now stays dirty and `Flush` retries it ([#187](https://github.com/s1crab/android-log-mcp/issues/187)).
- `android-log-mcp export --out` no longer creates its temporary file in your directory ([#206](https://github.com/s1crab/android-log-mcp/issues/206)): it is written in `<root>/export/` (where a leftover is swept after an hour) and renamed into place; when the rename crosses volumes the content is copied next to the target and renamed there. With `--offline` (no data root) the temporary file is still created next to `--out`.
- Each in-session restart of the recorder (the logcat stream dies while the device stays attached) no longer leaves the previous iteration's `reconcile` and side-work goroutines running ([#194](https://github.com/s1crab/android-log-mcp/issues/194)): every `recordOnce` has its own context, cancelled when it returns, so repeated restarts no longer pile up repair loops and adb work.
- A search no longer races with the recording of new lines on the block index of a segment ([#197](https://github.com/s1crab/android-log-mcp/issues/197)): the index a search reads is now a snapshot taken under the history lock.
- Concurrent track-devices updates can no longer start two recording sessions for one device ([#193](https://github.com/s1crab/android-log-mcp/issues/193)): the check that no session runs and the publication of the new session are one critical section.
- A follower store promoted to writer now takes over what the old writer left ([#180](https://github.com/s1crab/android-log-mcp/issues/180)): it re-lists the segments, reloads the newest seq and the recent window, and cuts a torn last line before its first append. Before, it reused seqs the old writer had written (duplicate seqs on disk) and its first record joined a partial last line and was lost.
- `crashes.jsonl`, `procs.jsonl` and `dropbox_seen.jsonl`: an entry appended after a torn last line (a hard kill or short write in the middle of a line) now starts a new line instead of joining the partial one and being lost ([#181](https://github.com/s1crab/android-log-mcp/issues/181)). Readers already skip the undecodable partial line.
- A failed write no longer wedges the recorder or loses queued lines ([#182](https://github.com/s1crab/android-log-mcp/issues/182), [#191](https://github.com/s1crab/android-log-mcp/issues/191)). `Store.Append` is all or nothing: on a write or flush error it rolls back the seq, the in-memory window and the meta times, removes segments the batch created and cuts the partial bytes, also forgetting any history index or cached blocks that covered them (the cut is retried before the next write if it fails), so a transient disk-full no longer leaves records only in memory or burns seqs. `rotateLocked` and `Flush` no longer ignore flush errors, and `Close` retries a failed `meta.json` save. `Pipeline.Drain` keeps a batch queued when `Append` fails and `Appended()` counts only stored records, so catch-up no longer reports lost lines as stored.
- A multi-line entry that the live assembler stored in pieces (a slow adb transport delays the rest of the entry past the 150 ms flush) is no longer stored a third time by the replay after a reconnect ([#192](https://github.com/s1crab/android-log-mcp/issues/192)). The resume now also recognises the whole entry made of consecutive stored records with one header. The live flush is unchanged, so the entry is still stored as separate records on such a transport.
- DropBox ANR entries are matched one-to-one ([#176](https://github.com/s1crab/android-log-mcp/issues/176)). With two ANRs of one pid less than two minutes apart, an entry could attach to the neighbouring ANR (the nearest one by time) and later replace its traces. An ANR that already has a DropBox entry is no longer offered another one, an ANR logged after the entry's header time ranks behind those logged before it, and existing ANR traces are never overwritten.

## [0.8.1] - 2026-10-17

Platform status: Windows and macOS are covered by CI only (unit tests with the fake adb); they were not tested with a real or emulator-attached adb for this release and are **unverified**. The emulator checks ran on API 29 locally (software emulation / TCG); API 35 is noted in the release PR / validation README.

### Added

- Exact filtered counts for tag / package / process / level / uid queries via per-block class counts in the history index ([#37](https://github.com/s1crab/android-log-mcp/issues/37)). Countable `logcat_search` queries report `exact=1` with no 100 ms count budget; blocks with zero matching classes are skipped; `view=summary` levels / tags / processes are exact over the whole range. Index built lazily at query time (ingest unchanged). Cap: >4096 classes in a block falls back to decode so the result stays exact.

### Fixed

- `CountMatch` / `AggClasses` skipped decoding only for seq-range misses; blocks wholly outside a time window were still decoded, which slowed `from`/`to` and `view=summary` queries after #37. They now use the same `outside` check as `ScanTime`.

## [0.8.0] - 2026-10-16

Platform status: Windows and macOS are covered by CI only (unit tests with the fake adb); they were not tested with a real or emulator-attached adb for this release and are **unverified**. The emulator checks ran on API 29 and API 35 on the GitHub runners, and on API 29 locally (software emulation).

Output format `fmt=1` ([#85](https://github.com/s1crab/android-log-mcp/issues/85), agent-first output, [docs/GOALS.md](docs/GOALS.md)). The whole set ships in one breaking release (0.8.0); this version covers `logcat_search`, `logcat_status`, `logcat_crashes`, `logcat_app_launches` and the errors shared by all tools (the CLI in this part).

### Upgrade notes

0.8.0 is one deliberate output break ([#85](https://github.com/s1crab/android-log-mcp/issues/85)): parse once, against `fmt=1` (see [docs/reference.md](docs/reference.md#output-format-fmt1)). Pre-1.0, so breaking is a minor version (AGENTS.md §4). The first token of every response is now `ok`, `empty` or `error`; read the header line first, then split rows on spaces / `key=value`.

| # | Breaking change | What to do |
|---|---|---|
| B1 | `logcat_search` has a `summary sampled=N top3_share=P% tags=... msgs=...` head line when more matches exist than shown (`sampled` is the number of matches the digest was computed from, at most 2000, not `matched`; `~` before a percentage means `sampled` < `matched`); the `top tags in ...` tail line is gone | read the head line |
| B2 | Every truncation is one `truncated <what>=N exact=0\|1 ... next: param=value` line (replaces `[truncated: ...]`, `[output cap: ...]`, `more:`, `raise limit`); crashes, launches and status gain markers | follow `next:` |
| B3 | The `clock:` prose line is gone; `clock_jumps=N ... next: after=...` appears only when a jump splits the matches | parse the one line |
| B4 | Header is `key=value` with ISO times; `host now`, `format:` and `count:` lines are gone (`host_now`, `tz` with `verbose=true`); `fmt=1` | match on the first token and keys |
| B5 | Rows: one-token process (`uid:system`, never `?(uid system)`), `\| ` continuation lines, `^ rep=N`, ASCII separators, no thousands separators, durations in seconds/ms | split rows on whitespace |
| B6 | Empty results and errors are `empty ... scanned= range=` and `error code=<code> ... next:`; `call` maps JSON/usage mistakes to the same shape | match `code=`, not message text |
| B7 | `logcat_crashes` detail shows 20 lines before a crash (was 40) | pass `before_lines=40` (max 100) for the old amount |
| B8 | CLI: `call` writes nothing to stderr normally; `service status`, `record`, `report` stderr and `set-root` print `key=value` lines; usage errors are one `error code=usage ... next:` line | use `--json` (unchanged, numeric fields added) or parse `key=value` |
| B9 | `logcat_search view="summary"` is `levels` / `tags` / `procs` / `msgs` / `crashes=N anrs=N next: logcat_crashes ...` / `latest_errors=N` lines instead of the prose block (`total ... · by level`, `top tags:`, `top processes:`, `top repeated messages`, `crashes in range`) | parse the key lines; counts are over the whole range |
| B10 | Header values: a string is bare unless it is empty or contains a space, control character, `"`, `=` or `\`; otherwise a JSON string (a Windows path is always `"C:\\x"`). `next:` commands quote paths for the host shell, one per line | read quoted values with a JSON string parser |
| B11 | Status and error text uses the glossary terms ([#40](https://github.com/s1crab/android-log-mcp/issues/40)): `service status --verbose` / `logcat_status` print `service: service process pid …, connected N` (was `service: background service pid …, clients N`) and `N MCP/CLI process(es) connected` (was `N client(s) connected`); the `client: version …` line is `note code=version_skew mcp_process=… service=… protocol=… compatible=1`. The JSON `clients` field is unchanged | match the new text (or `code=version_skew`), or read `clients` from `service status --json` |

### Changed

- **Breaking:** text that agents and users read says **MCP process**, **CLI process**, **service process** and **MCP host** (see [docs/glossary.md](docs/glossary.md)) instead of "client", "MCP client" and "background service": `service status`, `logcat_status` (lifetime and linger lines), errors (version skew, handoff, repair), `install`, the `--home` error, usage texts, the `report` README and the perf baseline descriptions. The version-skew note of `logcat_status` is structured: `note code=version_skew mcp_process=0.7.0 service=0.8.0 protocol=2 compatible=1`. Not renamed: the JSON `clients` field of `service status --json` and `service.json`, Go identifiers and perf metric keys ([#40](https://github.com/s1crab/android-log-mcp/issues/40)).
- `tools/list` (sent at the start of every session) is **smaller**: 10,378 bytes on 0.7.x, 10,230 bytes now (-1%), although `logcat_search`, `logcat_crashes` and `logcat_app_launches` gained the `verbose` parameter and a one-line description of the fmt=1 response (the full format is in [docs/reference.md](docs/reference.md)).
- **Breaking:** `logcat_search` responses start with one header line `ok`, `empty` or `error`, then `key=value` fields (`view device matched exact shown order range dev_now clock offset_s clock_age_s fmt=1`). Times are ISO-8601 with offset, numbers are plain integers (no thousands separators), durations are seconds (`_s`). The old prose header (`device … · device time … · device now … · host now …`), the `format:` line and the `count:` line are gone. `host now` is no longer printed by default; pass `verbose=true` to get `host_now`, `tz` and the column legend. Upgrade: parse the first line as `ok|empty|error k=v …`; read the count from `matched`/`exact` (`exact=0` means a lower bound, with `counted_from`/`counted_to`).
- **Breaking:** rows of `logcat_search` use one-token columns `time level pid/tid process tag: message`: the process is never `?(uid system)` any more but `uid:system` (`?` = unknown); continuation lines of a multi-line message start with `| `; `↳ ×N identical until T` became `^ rep=N until=T`; Java stack markers became `^ frames_hidden=K` and `^ same_frames=K as=T`. Upgrade: split a row on whitespace into 5 tokens, the rest is the message.
- **Breaking:** every cut is one line `truncated <what>=N exact=0|1 … next: param=value`, replacing `[truncated: …]`, `[output cap: …]`, `more:`, `more/wait:`, `stacks: … shortened` and the `…[+N chars; …]` per-line marker (now `...[line_cut=N more_chars=M next: …]`). The next step is always executable; the page cursors are `next: before=…` (older) and `newer: after=…`.
- **Breaking:** clock jumps print one line `clock_jumps=N splitting=S seq=… delta_s=… matches_before=… matches_after=… next: after=…`, and only when a jump splits the matches. The prose `clock:` line and the `clock_jump:` key=value line are gone (`verbose=true` prints each jump as `clock_jump seq=…`). The same jump is no longer printed twice.
- **Breaking:** empty results and errors are first-class: `empty … matched=0 scanned=N range=<stored first>/<stored last>` with `hint code=… next: …` lines; errors are `error code=<code> … next: …` with a closed set of codes (`bad_param`, `bad_filter`, `bad_time`, `bad_cursor`, `host_time`, `empty_window`, `unknown_device`, `several_devices`, `unknown_tool`, `unknown_param`, `bad_type`, `bad_json`, `device_*` …). `unknown tool` lists the tools. The `filter parsed as:` echo is `filter parsed=…` and is printed only when `filter` is set.
- **Breaking:** the `top tags in …` tail line of a wide `logcat_search` moved to the head: when more matches exist than are shown, one `summary sampled=N top3_share=P% tags=a:12%,b:6% msgs=515x"tag: message with #"` line (at most 300 chars) follows the header; `sampled` is how many matches the digest was computed from (the first 2000, not the matched count), and the percentages carry a `~` when `sampled` is smaller than `matched`, so one call says what the hidden matches are ([#85](https://github.com/s1crab/android-log-mcp/issues/85)). `view="summary"` is `levels` / `tags` / `procs` / `msgs` lines with counts, crash counts plus the `logcat_crashes` call, and one `next:`.
- `logcat_search` gains `verbose` (see above). The shared-uid package list (up to 24 packages for one query) is `note code=shares_uid … shares_uid=N next: verbose=true` by default.
- **Breaking:** `logcat_status`, `logcat_crashes` and `logcat_app_launches` use the same header (`ok|empty|error`, `view device … dev_now clock offset_s fmt=1`), ISO-8601 times with offset and the same `truncated … next:`, `note code=…`, `hint code=… next: …` and `error code=… next: …` lines as `logcat_search`; `host_now` and `tz` are printed with `verbose=true` only. `logcat_status` prints one fact per line (`adb`, `store`, `device id=… state=… model=… tz=…`, indented `recording state=…`, `window`, `stored`, `memory`, `note`), the cursor is `cursor=…` in the header (`cursor=none` while the backfill runs or several devices are online), and `package=` adds `package id=… installed=1|0|unknown uid=… running=N` and one `proc pid=… name=… state=running|ended … reason=… [crash=cr3|anr=anr1] scope=this_boot|before_reboot` row per process (names are one token). `logcat_crashes` lists `crash id=… time=… kind=… pid=… process=…` entries with `same_signature same_sig=N ids=… next: id=…`; the detail has `signal:`, `crash_location:`, `tombstone`, `main_thread traces=…` and `before_crash pid=… lines=N` sections. `logcat_app_launches` prints `#N launch time=… kind=COLD|WARM|HOT component=… pid=… ttid_ms=…|ttid=none ttfd_ms=…|ttfd=none` with `step name=… at_ms=… [took_ms=…]` lines; the explanatory sentences are `note code=…` codes now (see [docs/reference.md](docs/reference.md)). Upgrade: parse the header line first; split rows on `key=value`.
- **Breaking:** `logcat_crashes` shows the 20 lines before a crash by default (was 40); `before_lines` (1-100) asks for more, and a cut says `truncated before_lines=20 exact=0 next: before_lines=40`. `logcat_crashes` and `logcat_app_launches` gain `verbose`.
- **Breaking (CLI):** `android-log-mcp call` prints only the tool response: stderr stays empty unless the service was started (`service state=started pid=… version=…`). Usage and JSON mistakes are `error code=usage|bad_json … next: android-log-mcp call logcat_status '{"device":"avd-api29"}'` (exit 2), a service that cannot be reached is `error code=service_unreachable … next: android-log-mcp service status`. `record` prints `ok|empty record action=… service_pid=…` plus one `device id=… state=… caught_up=0|1 idle_stop=… pinned_until=…|-` line per device (ISO times, no nanoseconds); `service status` prints `ok service running=1 pid=… version=… root=… recording=…` and a `next: … --verbose` line (`--verbose` adds the former text); `report` stderr lines are `report device=… records=… crashes=… device_info=…`; `set-root` success is `ok set-root root=… old_root=… old=kept|removed|…`. Usage errors of `record`, `report`, `set-root`, `root` are one `error code=usage … next: <command>` line (exit 2); the full usage only for `-h`. `--json` of `record` and `service status` is unchanged and gains numeric fields (`detail_code`, `uptime_s`, `linger_min_s`, `linger_max_s`, `exit_grace_s`). `root` (bare path) is unchanged.
- `logcat_search` hints at three silent filter mistakes ([#86](https://github.com/s1crab/android-log-mcp/issues/86)): a bare `OR` / `AND` (a message keyword, not an operator), an unquoted multi-word value after a text key (`message:rate limit`), and a top-level `|` mixed with implicit AND (`|` binds tighter than a space). The response gains `note code=filter hint=or_word|multi_word_value|mixed_or msg=… next: filter=…` with the fixed filter; at most one note per kind and two per response, none for correct queries, lowercase `or`/`and`, plain implicit terms or regex keys. The query still means what it did. The `filter` description in `tools/list` gains one clause.

### Fixed

- After the device clock was set back, DropBox re-dates its old entries and an ANR entry read for the first time after that showed up as an extra ANR next to the one already stored ([#152](https://github.com/s1crab/android-log-mcp/issues/152)); the real time in the entry body (`----- pid N at <time> -----`) now decides the match and the retention cutoff, the header time stays a fallback. Java and native crash entries carry no usable body time and already match by pid.
- A DropBox entry is now matched to its logcat crash or ANR across a device clock jump ([#93](https://github.com/s1crab/android-log-mcp/issues/93)). The entry time is compared with the crash time as stored and as shifted by the clock jumps stored after the crash ("possible" jumps are ignored); the windows stay 60 s (Java and native crash) and 120 s (ANR), and kind, pid, process and boot must still agree. Before, a crash and its DropBox entry that straddled a jump were listed twice, or a different crash of the same process fell inside the window and got the wrong DropBox details. When two different crashes are equally near, the entry is left unmatched instead of merged into one of them.
- Windows: stopping a device (or the service) now waits up to 500 ms for each killed adb grandchild process to really exit, so a process that is still exiting no longer keeps the data or temp directory locked ([#73](https://github.com/s1crab/android-log-mcp/issues/73)).

## [0.7.1] - 2026-10-16

### Fixed

- Correction to the 0.7.0 upgrade notes: the long-path fallback socket did **not** move. When `<control dir>/run/service.sock` would exceed the socket path limit (a very long home or cache path), the socket is, as in 0.6.0, `/tmp/alm-<uid>/<hash>.sock` (Windows: `%TEMP%\alm-<hash>\s.sock`). Nothing needs to be done when upgrading; the unreachable cache-dir fallback code was removed ([#108](https://github.com/s1crab/android-log-mcp/issues/108)).
- One ANR is no longer reported twice when the `ANR in` block follows the `am_anr` event by more than 30 s (slow ANR dumps on loaded devices and emulators): events, logcat block and DropBox entry are now matched per process over up to 120 s ([#111](https://github.com/s1crab/android-log-mcp/issues/111)).
- An ANR whose DropBox entry arrives 60 to 120 s after the `am_anr` event (a slow ANR dump on a loaded device or emulator) is no longer listed twice when the DropBox entry was fetched before the logcat events were seen: the DropBox entry of an ANR now uses the same 120 s window as the other sources, in both orders. Other crash kinds keep their 60 s window ([#129](https://github.com/s1crab/android-log-mcp/issues/129)).
- The first `logcat_status` (or log tool call) that replaces a running service process (version handoff, or automatic replacement of a hung service process) now starts the requested device, like on a cold start, instead of answering `recording: idle` / `not recording (device not connected)` until the next call; a slow device identity no longer prevents the device from being started, also when the device is named by its adb serial ([#109](https://github.com/s1crab/android-log-mcp/issues/109)).

## [0.7.0] - 2026-10-15

### Upgrade notes

- **`android-log-mcp service stop` exits 1 when a service holds the lock but does not answer**, instead of printing `service: not running` and exiting 0. Scripts that treated that exit code as "stopped" should call `service stop --force` ([#83](https://github.com/s1crab/android-log-mcp/issues/83)).
- **The long-path fallback socket moved** (unix only; used when `<control dir>/run/service.sock` would exceed the socket path limit, e.g. a very long home path). It is now `<user cache dir>/android-log-mcp/run/s-<hash>.sock` instead of `/tmp/alm-<uid>/<hash>.sock`. A service started by an older version keeps its old socket until it stops or is replaced ([#83](https://github.com/s1crab/android-log-mcp/issues/83)).

### Added

- `android-log-mcp service stop --force` ends a service that holds the lock but does not answer: SIGTERM first, SIGKILL after 10 s (Windows: a stop request, then TerminateProcess after 10 s; lines the service had not flushed yet can be lost, and the command says so). It only signals a process that is verified to be the service recorded in `run/service.json` (same executable and start time), and it never kills a service that runs inside an MCP process (that would kill the MCP process): it refuses and says to end that MCP process instead ([#83](https://github.com/s1crab/android-log-mcp/issues/83)).
- `android-log-mcp report --logs-only` makes a small zip with no recorded device data: the service log (last 1 MiB), `crash.log`, `service.json`, a diagnosis of the service lock and `adb devices -l`, all redacted. It works with `--offline DIR` while the service is down ([#83](https://github.com/s1crab/android-log-mcp/issues/83)).
- Every report now contains `diagnostics/` (service log, `crash.log`, `service.json`, `diagnose.txt`, and `adb devices -l` when adb is available), also `--offline` reports (they run `adb devices -l` only; `--no-adb` skips it). The values go through the usual redaction, plus two new rules that apply to every report: user names in home paths (`/home/<user>`, `/Users/<user>`, `C:\Users\<user>`) and the home directory of the user who runs the report become `[REDACTED:username#n]` / `[REDACTED:home_dir#n]` (also in the command line and warnings of `manifest.json`), and the serials in `adb devices -l` become `[REDACTED:device_id#n]`.
- `ANDROID_LOG_MCP_REPORT_HINT=1` (developer option, off by default): connect and start errors that mean android-log-mcp itself is broken then tell the agent to ask the user first and, if they agree, run `report --offline <root> --logs-only`; the user reviews the zip and submits the issue by hand. It changes only error text. There is no network code in android-log-mcp: nothing is uploaded or sent.
- `service status` says so when a service holds the lock but does not answer (exit 1; `{"running":false,"unresponsive":true,…}` with `--json`).
- `run/service.json` records the service's OS start time (`start_time`).

### Changed

- The service watches its socket every 30 s and recreates it when the file was removed (for example by a tmp cleaner), logging a warning ([#83](https://github.com/s1crab/android-log-mcp/issues/83)).
- MCP process and CLI process messages are levelled: "using the service pid …", "replacing the older service …", "connection lost" and similar notes go to the service log only; only warnings and errors also reach stderr, which MCP hosts such as Cursor show as `[error]` ([#84](https://github.com/s1crab/android-log-mcp/issues/84)).
- Documentation reorganised ([#87](https://github.com/s1crab/android-log-mcp/issues/87)): the README only covers install, a short tools list, filter syntax and FAQ. Details moved to [docs/tools.md](docs/tools.md), [docs/reference.md](docs/reference.md) and [docs/compatibility.md](docs/compatibility.md); the repository layout is in [AGENTS.md](AGENTS.md). The README "Upgrading" section is removed: upgrade notes and breaking changes are only in this changelog.
- `logcat_crashes` lists crashes newest first by arrival, no longer by device time ([#55](https://github.com/s1crab/android-log-mcp/issues/55)). After the device clock is set back, crashes that arrive later have older device times and used to sort as old, so the newest page could leave them out. Shown times and the `from`/`to` window stay device time; ids are unchanged. A DropBox-only crash of an old event that is imported late appears at the top of the list.

### Fixed
- An MCP process no longer gives up with "holds the lock but does not answer" or a vague `context deadline exceeded` when the service process is hung ([#83](https://github.com/s1crab/android-log-mcp/issues/83)):
  - A service counts as reachable only when it answers a hello, not when the socket merely accepts a connection.
  - A service that is verified as this tool's, is not shutting down and has been silent for more than 10 s is replaced automatically (at most 3 replacements per minute). A service that is not recognised as ours, or that runs inside an MCP process, is never signalled; the error names the pid and the manual command.
  - When several MCP processes start at once, one repairs the service and the others wait for its result (up to 60 s) instead of each waiting in turn.
  - Connection errors name the service log and the commands to try.
- A lock that was just released could look held for a few milliseconds while a child process was being started (macOS and Linux) ([#82](https://github.com/s1crab/android-log-mcp/issues/82)). The recorder then logged "being written by another process … not recording it", `set-root` could fail with "is being written by another process", and the service could report "already running for this user" or take a stopped service for a running one. The cause is that a `flock` belongs to the open file description, and a forked child holds a copy of every open fd until its `exec`. A lock that looks held is now retried for up to 200 ms (1 ms to 10 ms backoff) before it is reported held; the worst wait measured on a macOS CI runner was 23 ms. A lock that really is held is reported after the 200 ms. Windows is not affected and is unchanged.
- After `set-root`, lines that the old root already stored are no longer stored again by a later whole-buffer read ([#56](https://github.com/s1crab/android-log-mcp/issues/56)). The move now carries the hashes of the newest stored lines of the current boot (up to 500,000, in a new `resume_dedup.bin` next to `meta.json`, not inside it), and every resume in the new root uses them until the device reboots or that many new lines are stored. Before, only the first resume used a small set taken near the newest line's device time, so a device clock jump or set-back after the move could store old lines twice.
- The crash detector follows device clock jumps ([#55](https://github.com/s1crab/android-log-mcp/issues/55)). Its matching windows (15 s Java, 30 s ANR, 60 s `am_crash` native) and its quiet period are counted on a time line without the jumps. A clock set no longer splits one crash into two (the `am_crash` event and its stack, or the native crash and its tombstone), no longer merges two crashes of a reused pid, and no longer keeps a pending native crash open until the device clock catches up. A pending native crash now also ends when no more lines arrive: until now only the next line ended it.
- Lines are attributed to the right device process after the device clock is set back ([#55](https://github.com/s1crab/android-log-mcp/issues/55)). A pid reused by another app after a back jump used to give its lines to the previous app, or to none, because lifetimes were compared by device time. Lifetimes now carry the sequence numbers of their start and end (`start_seq` / `end_seq` in `procs.jsonl`; older files load without them and keep the time rule), and the owner of a pid is the newest lifetime that started before the line in arrival order. Device time only gives the 2 s fork-race slack and the 5 s grace after an end, and only when no clock jump lies between the two records.

## [0.6.0] - 2026-10-15

### Upgrade notes

- **`logcat_search` returns 50 lines by default** (was 200). Pass `limit: 200`, or page with the `before` cursor, to get the previous amount ([#23](https://github.com/s1crab/android-log-mcp/issues/23)).
- **Java stacks inside lines are short by default.** Pass `stack: "full"` for whole stacks, as before. `logcat_crashes(id=…)` still shows a crash's whole stack ([#23](https://github.com/s1crab/android-log-mcp/issues/23)).
- **Device time vs host time wording** ([#48](https://github.com/s1crab/android-log-mcp/issues/48)):
  - Every response header now reads `device now … · host now … · clock offset …`.
  - `logcat_status` labels host times (`recording since host time …`, `last seen at host time …`), and its cursor line reads `cursor (newest line, <serial>)`.
  - Update anything that parses these lines.
  - Query windows (`from`/`to`, `age:`) are always device time.
- **`host:` window values are rejected** (`from="host:5m"`, `age:host:5m`). Use device time windows. For "what happens from here on", take the `logcat_status` cursor and pass it as `after`.
- **Relative windows end at device now + 2 s** (`age:5m`, `from="5m"`, the default window of `logcat_app_launches`). Lines dated later (logged before the device clock was set back) are counted in a note that gives the absolute `from`/`to` that shows them.
- **`logcat_status` shows rarely changing lines once per connection.** These are `root:`, `linger:`, `lifetime:`, `service log:`, `adb:` and `store:`. Pass `verbose: true` to always get them ([#23](https://github.com/s1crab/android-log-mcp/issues/23)).
- **Shared-uid device processes** whose name matches none of the uid's packages (e.g. `android.process.acore`) are attributed to no package instead of the uid's first package. `package=` still includes them for every package of the uid ([#44](https://github.com/s1crab/android-log-mcp/issues/44)).

### Changed

- **Breaking:** A device process of a shared uid whose name matches none of the uid's packages, such as `android.process.acore` (uid of `com.android.providers.contacts`, `com.android.contacts` and `com.android.calllogbackup`), is attributed to no package instead of the uid's alphabetically first package ([#44](https://github.com/s1crab/android-log-mcp/issues/44)). `package=` in `logcat_search`, `logcat_crashes` and `logcat_status` includes such device processes for every package of the uid and adds a `note:` naming the packages. The `package:` filter term is unchanged. Data recorded before the upgrade keeps its old attribution until it expires.
- Searches over older history decode stored lines about 4× faster ([#22](https://github.com/s1crab/android-log-mcp/issues/22)). A hand-written decoder handles the stored record layout and hands anything else to `encoding/json`, so results are unchanged. On the synthetic one-day history, a full scan reads 976k records/s instead of 259k (cold), a search with no match takes 0.73 s instead of 2.8 s, and `logcat_app_launches` over 24 h 0.41 s instead of 1.7 s.
- `from` / `to` / `age:` windows skip stored blocks wholly outside the device-time window instead of scanning back from the newest line ([#21](https://github.com/s1crab/android-log-mcp/issues/21)). On the 515k-line perf fixture: `age:` search 110 → 0.38 ms; an old `from`/`to` range 1,536 → 0.5 ms (cold 1,549 → 89 ms). Other query timings are unchanged within noise.
- **Breaking:** Device time and host time are named everywhere ([#48](https://github.com/s1crab/android-log-mcp/issues/48), glossary updated). Every response header shows `device now … (observed | estimated, observed … ago | unknown) · host now … · clock offset …` (about 115 chars more per response). `logcat_status` shows the detailed form, plus auto_time (read once after connect or a clock change, on the next status call) and the stored clock jumps, and labels host times (`recording since host time …`, `last seen at host time …`). The status cursor line reads `cursor (newest line, <serial>)`. Device now comes from one device clock read at connect plus the live lines (device time − host arrival time); the 30 s device clock poll is gone, and no query reads the device clock.
- **Breaking:** A relative window (`age:5m`, `from="5m"`, the 30 min default of `logcat_app_launches`) ends at device now + 2 s. Lines dated later (logged before the device clock was set back) are left out and counted in a note that gives the absolute `from`/`to` that shows them. `logcat_search {}` stays unbounded: newest lines by arrival, whatever their device time.
- **Breaking:** `host:` values (`from="host:5m"`, `age:host:5m`) are rejected with an explanation: queries use device time only; for "what happens from here on" use the `logcat_status` cursor with `after`.
- **Breaking:** `logcat_search` answers are much smaller by default ([#23](https://github.com/s1crab/android-log-mcp/issues/23)). Upgrade note: pass `limit: 200` and `stack: "full"` for the previous output.
  - The default `limit` is 50 instead of 200. `logcat_search {}` on the API 29 fixture is 8.4k chars instead of 27.5k (about 2.1k tokens instead of 6.9k); the rest is one `before` cursor away, and a truncated page says that `view="summary"` (about 2k chars for any range) shows what the whole range contains.
  - Java stacks inside a line are short by default (new parameter `stack`, `"short"` or `"full"`). Each exception and each `Caused by` keeps its head and its first 3 frames; the rest becomes `↳ K more frames` (Java's `... N more` and the frames of a `Suppressed:` exception are counted there). A stack whose frames were already shown in the same response becomes `↳ same K frames as HH:MM:SS.mmm`. A line holding a stack is cut at 2,000 chars, and every `Caused by` head and its first frame are kept first. A `stacks: N shortened (M frames hidden) · stack="full" shows every frame` line says when anything was left out. Ten warnings with the same 30-frame stack and a `Caused by` now take 3.8k chars instead of 27.5k, and all ten fit (before, the 30k cap showed seven). `stack: "full"` shows the lines exactly as before.
  - The cut marker of a long line no longer suggests narrowing the query, which never showed more: `…[+N chars; lines are cut at 2,000 chars in every query, narrowing does not show more]`. A response that hits the ~30k-char cap now says so (`[output cap: only 7 of the 10 matches asked for fit in ~30k chars · …]`).
- `logcat_search` answers in milliseconds on large histories ([#20](https://github.com/s1crab/android-log-mcp/issues/20)). It used to keep counting matches for up to 1.5 s after the page was full (about 1.5 s per call on a one-day history). Now:
  - Without a filter or time window, the total is exact and comes from the stored record range, without scanning.
  - With a filter, counting stops 100 ms after the page is full. The total is then a lower bound, and the response says which span was counted and how to get an exact count: `matched ≥14,043 lines (counted the newest 21 min only, since 23:32:09; older history not counted — narrow with from/to, e.g. from="1h", for an exact count)`.
  - Every lines response has a machine-readable `count:` line: `total`, `count_exact` and, for a lower bound, `counted_from` / `counted_to`.
  - The top-tags line says when it covers only the counted matches, or only the page.
  - On a one-day history, `logcat_search {}` takes about 1 ms instead of 1.5 s, and filtered searches whose page fills quickly about 110 ms.
- Crash ingest and crash lists no longer slow down as crashes accumulate ([#22](https://github.com/s1crab/android-log-mcp/issues/22)).
  - The recorder used to re-read and decode the whole `crashes.jsonl` for every new crash (twice). `logcat_crashes`, `logcat_status` and the zero-result hints of `logcat_search` did the same on every call.
  - Each device now has one in-memory crash index, shared by the recorder and the tool calls of the service process. It keeps a small summary of the newest version of each crash and the file offset of that version. It reads only the bytes appended since the last read, and rebuilds itself after the file is compacted or rewritten. A stack, ANR traces or DropBox text is decoded only for the crashes a response shows.
  - On the synthetic one-day history, recording runs at 448k lines/s instead of 47k, and `logcat_crashes {}` takes 0.7 ms instead of 17 ms.
  - With 2,000 stored crashes, recording a new crash takes 0.2 ms instead of 41 ms (35 KB allocated instead of 18 MB), and the crash list takes microseconds instead of 45 ms. Output is unchanged, including the order of the crash list.
- **Breaking:** `logcat_status` no longer repeats the lines that rarely change on every call ([#23](https://github.com/s1crab/android-log-mcp/issues/23)). `root:`, `linger:`, `lifetime:`, `service log:`, `adb:` and `store:` are shown on the first call of each MCP or CLI process's connection and again whenever one of them changes; otherwise they become one line, `unchanged since shown earlier in this session: root, linger, lifetime, service log, adb, store (logcat_status(verbose=true) shows them)`. This saves about 500 characters (about 35 %) on every later call. The `service:` line (pid, version, uptime, connected MCP and CLI processes), recording state, pins, devices, adb problems and the cursor are always shown. The new `verbose` parameter always shows every line. `android-log-mcp service status` is unchanged.

### Fixed

- Crashes in `android.process.*` or `android.hardware.*` device processes are no longer attributed to the framework package `android` ([#70](https://github.com/s1crab/android-log-mcp/issues/70)). `logcat_crashes(package="android")` and `report --package android` only include crashes of `system_server`, also for crashes recorded before the upgrade, and the crash details print no `package:` line for such processes.
- `logcat_app_launches` pairs launch steps correctly when the device clock is set ([#54](https://github.com/s1crab/android-log-mcp/issues/54)). Steps were sorted by device time across the whole window, so after a clock jump a launch request could take another launch's `Displayed` line, lose its own, or show a duration of minutes to years. Steps are now sorted by device time only within a clock segment (between two stored clock jumps), in arrival order across segments. A step logged after a jump that followed its request still belongs to that launch, even when its device time is outside the queried window (for example 1970 or a year ahead), and is shown with its device time and `duration: unreliable (device clock jumped)` instead of a number; the system-measured TTID / TTFD and callback durations are still shown. The answer also lists the clock jumps among the scanned launch lines (`clock:` / `clock_jump:`).
- Lines are no longer stored twice after the device clock is set back on Android 11 and older ([#75](https://github.com/s1crab/android-log-mcp/issues/75)). A whole-buffer resume compared only the newest stored lines, 1.5 × the buffer's entries, although the system buffer can still hold boot lines while main rotated many times. The #65 repair reads compared a window that ended shortly after the read's first lines. Both now compare with all stored lines of the boot session. Duplicated lines had also shown up in `logcat_app_launches` as a second launch, and they could hide a TTID logged after a clock jump.
- Lines are no longer lost while the device clock is set back on Android 11 and older ([#65](https://github.com/s1crab/android-log-mcp/issues/65)). That logd positions readers by device time, and while its buffer held lines dated after device now it could make the live stream skip blocks of new lines. Those lines are now recovered from the device buffer, which is read every 20 s while that is the case. `logcat_status` shows when this repair mode is on and how many lines it recovered.
- `logcat_crashes` no longer shows phantom crashes after the device clock is set back ([#63](https://github.com/s1crab/android-log-mcp/issues/63)). Android's DropBox re-dates entries that are dated after device now, and such an entry was imported again as a new crash at the earlier device time. DropBox entries are now recognised by their contents, and a Java or native crash entry joins the crash with the same boot, pid and device process whatever its device time.
- DropBox crashes are imported again after a device clock change ([#53](https://github.com/s1crab/android-log-mcp/issues/53)). Entries dated more than the retention before the highest device time ever stored were skipped. After a jump back past the retention (for example a reboot with a 1970 clock), no new entry was imported, and after a +1 year jump that was undone, none was for a year. The cutoff is now device now − retention; entries already imported stay out through their `dropbox_seen` keys.
- Reconnecting on the same boot no longer loses or duplicates lines after a device clock change ([#52](https://github.com/s1crab/android-log-mcp/issues/52)).
  - Lines logged while disconnected after the device clock was set back, dated before the newest stored line, were dropped as already stored. A clock jump measured at connect, or stored among the lines that may still be in the device buffer, now makes the service read the whole buffer instead of `logcat -T`.
  - After a clock jump below the oldest line in the device buffer, a resume imported the whole buffer again. The stored lines to compare against are now the last lines of the boot session by arrival (the buffer's entry count × 1.5, at most 500k), not those dated after the buffer's oldest line.
  - After a +1 year jump that was undone, every later reconnect read the whole buffer: `-T` now resumes from the newest arrival's device time, not the maximum stored device time.
  - A line logged twice in the same µs by the same thread was kept once: replayed lines are compared as a multiset (counts per hash).
  - Catch-up matches the buffer's last line by its header and copy count, not by its device time alone.
- Lines are no longer silently missed after the device clock was changed ([#21](https://github.com/s1crab/android-log-mcp/issues/21), [#48](https://github.com/s1crab/android-log-mcp/issues/48)). A `from`/`to` or `age:` window used to stop scanning at the first line older than the window in arrival order, so after a clock jump (set back, set forward by a day or a year, reset to 1970) lines inside the window could return 0 matches, and `view="summary"` could print an inverted range. Every line inside the device-time window is now found, and the output marks each clock jump where it falls (`clock:` line with the matches on each side and an `after` cursor for the lines after it, plus a machine-readable `clock_jump: seq=… delta=… device_before=… device_after=… host=… source=…`). Jumps are detected from live lines (two consecutive lines more than 60 s off the clock offset, or one after the framework's `Setting time of day` line), from replayed lines (a step back of more than 60 s, the `Setting time of day` line, or a step forward of more than 1 h reported as possible), and from the connect clock read after a reconnect.
- Lines logged after the device clock was set back are no longer dropped when recording had resumed on the same boot (after an idle stop, a reconnect or `adb root`). They were taken for duplicates of stored lines because they were dated before the last stored line; de-duplication now applies to the replayed part only.
- Catch-up after connecting no longer ends early when the device clock was set back before the connection: the last buffered line is matched by its exact device time instead of "device time at or after it", so replayed lines dated after it no longer count as live.
- Retention no longer depends on the Android device's clock ([#50](https://github.com/s1crab/android-log-mcp/issues/50)). After the device clock was reset to 1970 or set back by more than the retention window, the periodic compaction of the per-device crash, device process and DropBox files deleted entries that were just recorded. With the clock set ahead, it kept them too long. Rotating a quiet device's log segment by age had the same problem. Each entry of these files now records the host time it was written, and compaction and segment rotation go by host time only, like log segment deletion already did. Entries written by earlier versions keep the old rule until they expire.
- `package=` in `logcat_search`, `logcat_crashes`, `logcat_status` and `report` now includes device processes whose name extends the package with a dot, such as `com.google.android.gms.persistent` and `com.google.android.gms.unstable` ([#41](https://github.com/s1crab/android-log-mcp/issues/41)). A device process belongs to the installed package that its name equals or, failing that, to the longest installed package that its name extends at a `.`. On a shared uid, the name decides between the uid's packages. `package=com.google` still does not match `com.google.android.gms`, and `package=com.foo` does not match `com.foobar`. Device processes that started before the package list was read are re-attributed once it is read, and an app installed later is picked up by re-reading the list (at most once a minute while a device is recorded). Data recorded before the upgrade may still show the old attribution (the device process name as package) until it expires.

## [0.5.2] - 2026-10-11

### Fixed

- A device that disconnects and reconnects quickly no longer has two recording sessions running at once ([#30](https://github.com/s1crab/android-log-mcp/issues/30)). The new session starts only after the previous session's recorder stopped (at most 10 s) and its background work returned (at most 5 s, after which its adb children are killed together with their own child processes, never the adb server). A tool call right after the reconnect again waits for the new session's backfill: the previous session could end that wait early. If the previous session does not stop in time, the new session starts anyway, and the service log and `logcat_status` say so for that device ("recording session N started … before session N-1 had stopped: …").
- When a device starts recording again, the read-only view of its directory used while it was idle is closed at once instead of after one minute.

## [0.5.1] - 2026-10-11

### Fixed

- Stopping the recording of a device (idle stop, end of a pin, `record --stop`) or stopping the service no longer leaves adb processes behind ([#24](https://github.com/s1crab/android-log-mcp/issues/24)). It cancels the device's background work (process table, packages, buffer span, DropBox fetches, device tracking, identity probes) and waits for it, at most 5 s, before the store is closed. Work still running then is named in the service log, and its adb processes are killed with their child processes (never the adb server). A cancelled adb call also stops waiting for output pipes that a child process of adb still holds open. A DropBox fetch can now be cancelled, and a fetch interrupted by a stop runs in the next recording session.

## [0.5.0] - 2026-10-11

### Changed

- **Breaking:** recording starts on use ([#11](https://github.com/s1crab/android-log-mcp/issues/11)). An idle background service runs no adb at all. A device starts being recorded on the first log tool call that uses it (`logcat_status`, `logcat_search`, `logcat_crashes`, `logcat_app_launches`, also via `call`), on `report` and on `record --for`, and is first backfilled from its log buffer: calls wait up to 10 s for that and otherwise say "backfill in progress"; `logcat_status` then returns no cursor and asks to be called again. With several devices connected and no `device`, nothing is started and the devices are listed; exceeding `ANDROID_LOG_MCP_MAX_DEVICES` is an error. Lines that dropped out of the device's log buffer while it was not recorded are not in the history; use `android-log-mcp record --for DURATION` to keep recording without calls.
- **Breaking:** recording of a device stops after its own idle time since its last log tool call (`ANDROID_LOG_MCP_LINGER`, default `max(10m, 2 × the device's log buffer span)`, at most `1h`; `0` = after each call); a call that waits with `wait_ms` counts as use. Clients connecting or disconnecting no longer affect recording. The service no longer exits after the linger: it stays while clients are connected and exits 30 s after there are no clients, no pin and no recorded device.
- **Breaking:** `report` starts the background service (detached) when none runs, starts recording the requested devices and backfills them (up to 10 s; otherwise the manifest warns "backfill incomplete"), instead of building the report in its own process.
- `set-root` keeps the started devices, their idle timers and pins. Service protocol 2 (adds `record`); 0.4 clients keep working: a 0.5 MCP process takes over an older service process as before, and a 0.4 MCP process keeps working with a 0.5 service process.
- Faster first backfill, mostly on emulators: the device's log buffer is streamed with `adb exec-out logcat` (`adb logcat` on Android < 5.0 or when exec-out fails); while backfilling no other adb command runs on the device (`ps`, `pm list packages`, `dumpsys dropbox`, buffer span), and afterwards each DropBox tag needed by crashes in the backfill is fetched once (together with the new-boot history; skipped if fetched in the last 5 minutes); lines from before recording started no longer trigger `ps`; the identity and device probes are one adb shell. On an API 29 emulator a full buffer (~28,000 lines) now catches up in 29–37 s instead of 54–70 s; reading the buffer alone takes ~27 s there.
- "backfill in progress" shows the estimated total, e.g. "(1,423 of ~27,000 lines; earliest …)", estimated from the entries logd holds (`logcat -S`); after a `-T` resume it shows "(N lines so far; …)".

### Added

- `android-log-mcp record [--device X]... --for DURATION`, `record --stop` and `record` (state; `--json`): keep a device recorded without tool calls or clients until a deadline (at most 24 h, never shortened), stop it now, or show each device's recording, backfill, idle deadline and pin. `--for` starts the service detached when none runs. Agents use `logcat_status(record_for="30m")` (`"0"` stops).
- Pins are stored in `<control dir>/run/record.json` and survive crashes, version handoffs, `set-root` and restarts. A connected client restarts a crashed service at once while a pin is active. A pin is suspended after 3 crashes within a minute (`crash-loop`) or more than 10 automatic restarts per service version (`crash-budget`), shown with the last error and cleared by a new `record --for`.
- `logcat_status`, `service status`, `root --json` and the client's stderr line show per device whether it is recorded or idle, the backfill progress, the idle deadline and pins.

### Deprecated

- `ANDROID_LOG_MCP_CALL_WAIT` is ignored with a note on stderr: calls wait for the backfill themselves.

### Fixed

- `logcat_status` / `service status`: the global `linger:` line no longer says "(not measured yet)"; it states the rule, each recorded device shows its own value.
- Recording resumes without losing or duplicating lines after the device clock went back (e.g. an emulator snapshot restore): the buffer is read without `-T` and already stored lines are skipped; `logcat_status` reports it.
- A log tool call (or `report`) that arrived while its device was being stopped for idleness could answer from the stored data alone: no new lines, no backfill note, and `logcat_status` returned a cursor from before the stop ([#15](https://github.com/s1crab/android-log-mcp/issues/15); seen on Windows CI). Such a call now waits (within the same 10 s) until the device is recorded again; otherwise it says "backfill in progress (recording is restarting …)" and `logcat_status` returns no cursor. An idle stop re-checks under its lock that no call used the device in the meantime and is skipped if one did. A device restarted within 30 s of its identity probe no longer reuses that probe's clock reading: each saved probe is used once, and its clock is advanced by its age.

## [0.4.1] - 2026-10-10

### Added

- `android-log-mcp install [--json]`: copies the binary to a fixed path, `<user cache dir>/android-log-mcp/exe/android-log-mcp[.exe]` (Windows `%LocalAppData%\android-log-mcp\exe\android-log-mcp.exe`), and prints it, so MCP clients can run the native binary directly instead of the `npx.cmd` → `cmd.exe` → `node` → binary chain. Re-running it updates the copy even while clients run it; a copy that is still running is renamed aside and removed by a later install. `root --json` shows it as `exe`. The README recommends this on Windows, with Codex, Cursor and Claude Code examples; `npx` remains supported.

### Changed

- README: "Install and configure" now has two paths, A. npx and B. the native binary (recommended on Windows; from `install` or a GitHub Release tarball), with Codex, Cursor and Claude Code examples for Windows and macOS/Linux, upgrade steps and a table of which path to use on which OS.
- The npm launcher states `shell: false` explicitly (it already started the binary directly with `windowsHide`); a test guards it.

### Fixed

- Windows: console windows kept popping up while the background service ran (for example with Codex or ChatGPT as the MCP client). The service was started with `DETACHED_PROCESS`, i.e. without any console, so every console child it started (`adb devices`, `track-devices`, `shell getprop`, `logcat`, `start-server`, …) got a new visible console window. The service now starts with its own hidden console (`CREATE_NO_WINDOW`, still in a new process group and outside the client's job), and every child process the program starts is created without a window. A source scan test makes sure no new `exec.Command` skips this.

## [0.4.0] - 2026-10-10

A breaking release: a new storage layout, one data root for all clients, and one background service per user that does all recording.

Upgrade notes are in the **Breaking** entries below.

> 中文摘要：破坏性版本。新的存储结构 `v1/<设备ID>/`，旧数据不迁移、不读取；移除 `ANDROID_LOG_MCP_HOME` 和 `--home`，改用 `root` / `set-root` 统一管理数据根目录；每个用户一个后台服务负责全部录制，最后一个客户端关闭后继续录制一段时间（`ANDROID_LOG_MCP_LINGER`）；Windows 最低要求 Windows 10 1803 / Server 2019。

### Added

- **Background service** ([#3](https://github.com/s1crab/android-log-mcp/issues/3)): one long-lived process per user records every connected device and answers every tool call; MCP and `call` processes are thin stdio clients.
  - Started on demand and detached, from a copy of the binary under `<root>/bin/<version>-<hash>/` (the running and at most one other version are kept). If that is impossible, the client runs the service in its own process and serves the same socket for other clients.
  - Crash recovery: clients reconnect on their next call, one starts a new service and the call is retried; recording resumes with `logcat -T` and deduplication. A crash-loop guard stops respawning after 3 exits within a minute.
  - Linger: the service keeps recording for `max(10 min, 2 × device log buffer span)`, at most 1 h, after the last client disconnects; new variable `ANDROID_LOG_MCP_LINGER` (`30m`, `0`, `forever`). A new service waits at least 30 s for its first client.
  - Versions: a client replaces the service only if the service is older; a newer service is used as is. A separate protocol version lets compatible older clients use a newer service; incompatible clients get an error asking to upgrade.
  - Local only: AF_UNIX socket in a user-only directory (0700 / 0600; protected DACL for the user and SYSTEM on Windows) with a same-user peer check. No TCP.
  - Visibility: client stderr and `logcat_status` show the service pid, version, protocol, data root, linger and service log (`<root>/log/service.log`, 1 MiB plus one rotation, counted in the total cap; `crash.log` for runtime crashes).
  - `android-log-mcp service [status [--json] | stop]`; `android-log-mcp service` runs it in the foreground.
  - The adb server is started outside the service's process group and is never killed; the service's own adb children end with it.
- **`android-log-mcp set-root DIR --keep-old | --delete-old [--timeout] [--json]`** ([#3](https://github.com/s1crab/android-log-mcp/issues/3), [#10](https://github.com/s1crab/android-log-mcp/issues/10)): moves the data root for every client. The service stops recording, carries each device's state over (seq numbers, the `logcat -T` resume point and the boundary lines, so there is no gap and no duplicate), commits `config.json` atomically under `run/config.lock` and resumes; any failure before the commit is rolled back. `--delete-old` removes only the tool's own directories; leftovers are reported (exit 3) and retried at the next service start. Works without a running service (never starts one).
- **`android-log-mcp root [--json]`** prints the data root, control directory, config file, socket and running service ([#9](https://github.com/s1crab/android-log-mcp/issues/9), [#10](https://github.com/s1crab/android-log-mcp/issues/10)).
- **Stable device IDs** ([#5](https://github.com/s1crab/android-log-mcp/issues/5)): `ro.serialno` for physical devices (the same over USB and Wi-Fi), `avd-<name>` for emulators, a fingerprint hash as fallback. Two devices online at once with the same ID are told apart by boot id and filed as `<id>~<adb serial>` with a note in `logcat_status` ([#6](https://github.com/s1crab/android-log-mcp/issues/6)).
- `scripts/service-e2e.sh`: live check of the service on a device through the npm launcher.
- CI: the npx compatibility jobs also run the MCP handshake through the background service.

### Changed

- **Breaking:** new versioned storage layout `<root>/v1/<device-id>/` under one data root, by default `<user cache dir>/android-log-mcp` ([#5](https://github.com/s1crab/android-log-mcp/issues/5), [#7](https://github.com/s1crab/android-log-mcp/issues/7)). **Data recorded by 0.3 or earlier is neither migrated nor read**, and 0.3 report zips cannot be loaded with `--offline`; the old `<serial>/` directories (e.g. `emulator-5554/`) are ignored, not deleted: remove them, and any manual backups, when you no longer need them.
- **Breaking:** recording moved into the background service; it continues for the linger time after the last MCP client closes ([#3](https://github.com/s1crab/android-log-mcp/issues/3)). Nothing changes in the MCP host configuration. A still-running 0.3 MCP process uses its own old directories and does not conflict, but keeps its own recording: restart old MCP host sessions after upgrading.
- **Breaking:** Windows 10 1803 / Windows Server 2019 or later is required (AF_UNIX); older Windows gets a clear error.
- **Breaking:** `--offline <dir>` is the only read-only mode and takes the dataset directory (`--home DIR --offline` no longer works); the `report` flag that skips adb is now `--no-adb` ([#7](https://github.com/s1crab/android-log-mcp/issues/7)).
- `report` is built by the running service (or by the command itself when none runs); `call` goes through the service.
- Everything the tool creates is bounded: retention and the total size cap cover all format versions, `tmp/`, `run/`, `bin/` and `log/`; anything else under the root is ignored ([#5](https://github.com/s1crab/android-log-mcp/issues/5)).
- Windows file locks use `LockFileEx`, released by the OS when the process exits (no staleness heuristics); `flock` on Linux/macOS ([#9](https://github.com/s1crab/android-log-mcp/issues/9)).
- CI: Windows test failures now fail the build.
- README rewritten English-first; this changelog added.

### Removed

- **Breaking:** `ANDROID_LOG_MCP_HOME` and `--home`. If either is still set (in an MCP host's `env` / `args`, for example), the MCP process exits with an error naming `set-root`: remove it ([#9](https://github.com/s1crab/android-log-mcp/issues/9), [#10](https://github.com/s1crab/android-log-mcp/issues/10)).
- Per-device owner locks (`owners/`), follower mode and takeover between MCP processes; a leftover `owners/` directory from development builds is removed automatically ([#3](https://github.com/s1crab/android-log-mcp/issues/3)).
- Migration of the pre-0.4 flat layout and the `legacy/` handling ([#7](https://github.com/s1crab/android-log-mcp/issues/7)).

### Fixed

- A crash of the recorder when the newest segment is empty after a seq jump (`Store.Scan` indexed past the in-memory window) ([#9](https://github.com/s1crab/android-log-mcp/issues/9), [#10](https://github.com/s1crab/android-log-mcp/issues/10)).
- Leaked `adb track-devices` processes after a recorder was killed: adb children now end with the service.

### Security

- The service socket is reachable only by the same user (directory 0700, socket 0600, peer credential check; Windows ACL for the user and SYSTEM) and there is no network listener.

## [0.3.0] - 2026-10-10

### Added

- Memory and disk limits ([#2](https://github.com/s1crab/android-log-mcp/issues/2)): new variables `ANDROID_LOG_MCP_MEM_RECORDS`, `ANDROID_LOG_MCP_MEM_MB`, `ANDROID_LOG_MCP_HISTORY_CACHE_MB`, `ANDROID_LOG_MCP_IDLE_RELEASE`, `ANDROID_LOG_MCP_MAX_DEVICE_MB`, `ANDROID_LOG_MCP_MAX_TOTAL_MB`, `ANDROID_LOG_MCP_MIN_FREE_MB`.
- Configurable retention with `ANDROID_LOG_MCP_MAX_AGE` (Go duration or days, e.g. `7d`); `logcat_status` shows the effective value.
- Total cap for the data directory (default 2 GiB) and a recording pause when free disk space drops below 512 MiB, resuming with `logcat -T`.

### Changed

- **Breaking:** the default retention is **1 day instead of 7**; on first start, older segments and older crash, DropBox and ended-process records are deleted. Set `ANDROID_LOG_MCP_MAX_AGE=7d` to keep the old behavior.
- In-memory window capped at 20,000 lines or about 16 MiB per device (was 100,000 lines, unbounded bytes); history is read from disk in about 512 KiB blocks into a 32 MiB LRU cache, released after 2 minutes without queries. Measured recorder RSS with 300k records: about 300 MiB → 65 MiB after queries, 19 MiB idle.
- Aux files (`procs.jsonl`, `crashes.jsonl`) are compacted periodically, dropping expired entries.

### Fixed

- Windows: a source file named `aux.go` (a reserved device name on Windows) broke checkouts.

## [0.2.1] - 2026-10-10

### Added

- `android-log-mcp compat [--serial S] [--json | --raw]` and `scripts/compat-probe.sh`: a read-only probe of a device's logcat / ps / pm / dumpsys features, evaluated with the recorder's own parsers.
- README compatibility matrix (verified vs designed-only) and evidence from an API 29 emulator.
- CI: npx compatibility jobs on Node 16 – current (Linux) and Node 22 on macOS and Windows; the release workflow creates a GitHub Release with the npm tarballs.

### Fixed

- uid names `llkd`, `lmkd`, `dhcp`, `mtp` and `dns` from API 29 logcat are mapped instead of left unparsed.

## [0.2.0] - 2026-10-10

### Added

- `android-log-mcp report`: packs recorded logs, device info, DropBox entries, an optional `am start -W` result and a note into a zip, with sensitive values (tokens, credentials, emails, phone numbers, ID and card numbers, device IDs, IPs, MACs, domains) redacted by default; the packed store loads back read-only ([#1](https://github.com/s1crab/android-log-mcp/issues/1)).
- Global `--offline` flag to browse a dataset without adb.

### Fixed

- Windows: stores closed in tests and safer file replace/delete (meta rename and segment pruning retried while another process has the file open).

## [0.1.0] - 2026-10-10

### Added

- First release: an MCP stdio server giving AI agents read-only access to Android logcat, with background recording of every connected device, the tools `logcat_status`, `logcat_search` (Android Studio filter syntax with strict errors), `logcat_crashes` and `logcat_app_launches`, and the `call` debug subcommand.
- npm packaging: a Node launcher plus per-platform binary packages (linux / darwin / win32 × x64 / arm64), CI and release workflows.

[Unreleased]: https://github.com/s1crab/android-log-mcp/compare/v0.9.1...HEAD
[0.9.1]: https://github.com/s1crab/android-log-mcp/compare/v0.9.0...v0.9.1
[0.9.0]: https://github.com/s1crab/android-log-mcp/compare/v0.8.1...v0.9.0
[0.8.1]: https://github.com/s1crab/android-log-mcp/compare/v0.8.0...v0.8.1
[0.8.0]: https://github.com/s1crab/android-log-mcp/compare/v0.7.1...v0.8.0
[0.7.1]: https://github.com/s1crab/android-log-mcp/compare/v0.7.0...v0.7.1
[0.7.0]: https://github.com/s1crab/android-log-mcp/compare/v0.6.0...v0.7.0
[0.6.0]: https://github.com/s1crab/android-log-mcp/compare/v0.5.2...v0.6.0
[0.5.2]: https://github.com/s1crab/android-log-mcp/compare/v0.5.1...v0.5.2
[0.5.1]: https://github.com/s1crab/android-log-mcp/compare/v0.5.0...v0.5.1
[0.5.0]: https://github.com/s1crab/android-log-mcp/compare/v0.4.1...v0.5.0
[0.4.1]: https://github.com/s1crab/android-log-mcp/compare/v0.4.0...v0.4.1
[0.4.0]: https://github.com/s1crab/android-log-mcp/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/s1crab/android-log-mcp/compare/v0.2.1...v0.3.0
[0.2.1]: https://github.com/s1crab/android-log-mcp/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/s1crab/android-log-mcp/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/s1crab/android-log-mcp/releases/tag/v0.1.0
