# Android log MCP stdio server: design v3 (a critical revision for AI agents)

- Date: 2026-10-10 (Asia/Shanghai). Written at design time, before any code; later sections record what was built (issue numbers in the headings).
- Premise: the caller is **always an AI agent** (Claude Code/Desktop, Codex, Cursor, etc.), never a human.
- Builds on `research-v2.md`. Its findings on AS syntax, pid mapping, buffer choice and the recording process (v2 called it the daemon; now the service process) still hold. This document changes only the interface exposed to agents and the behavior around it.
- Labeling convention: **[source]** has a cited source; **[inferred]** is my judgment, not measured; **[unknown]** still has to be verified.
- The output examples in §2 are the design-time sketches. The shipped text format (`fmt=1`, [#85](https://github.com/s1crab/android-log-mcp/issues/85)) is specified in [reference.md](reference.md#output-format-fmt1); the tool descriptions and parameters are in [tools.md](tools.md). Where they differ, those two win.
- Terms follow [glossary.md](glossary.md) (MCP process, service process, CLI process, adb server, adb child, device process, MCP host).

## 0. Best practices relied on (all checked against the original text)

1. **Anthropic, "Writing effective tools for agents"**: https://www.anthropic.com/engineering/writing-tools-for-agents
   - Few, well-chosen tools built around high-value workflows; do not wrap the underlying API 1:1. The text's example: instead of `read_logs`, build `search_logs`, which returns only the relevant lines plus surrounding context.
   - Use prefixes as namespaces.
   - Return high-signal information: natural-language names, no cryptic IDs; offer concise/detailed levels.
   - Pagination, filtering and truncation need sensible defaults; when truncating, tell the agent how to get more.
   - Error messages must be actionable and show the correct format with an example.
   - Parameter names must be unambiguous (`user_id`, not `user`).
   - The text mentions that Claude Code limits tool output to 25,000 tokens by default.
2. **MCP specification 2025-11-25, tools**: https://modelcontextprotocol.io/specification/2025-11-25/server/tools , and schema.ts
   - A tool definition has `title`, `inputSchema` (for no parameters, `{"type":"object","additionalProperties":false}` is recommended), an optional `outputSchema`, and `annotations` (readOnlyHint, destructiveHint, idempotentHint, openWorldHint).
   - When returning `structuredContent`, a tool **SHOULD** also put the serialized JSON in a TextContent; if it declares an outputSchema it **MUST** conform.
   - Business errors are returned with `isError: true`, "so the model can correct itself".
   - `InitializeResult.instructions` can carry server-level guidance (here: for this MCP stdio server).
3. **Claude Code MCP documentation**: https://code.claude.com/docs/en/mcp
   - Output above 10,000 tokens triggers a warning; the default cap is 25,000 tokens (environment variable `MAX_MCP_OUTPUT_TOKENS`).
   - Text results above **50,000 characters** are saved to a file instead.
   - The timeout of one tool call is set by `MCP_TOOL_TIMEOUT` or the per-server `timeout`; the default is about 28 hours.
4. **Codex configuration reference**: https://developers.openai.com/codex/config-reference , `mcp_servers.<id>.tool_timeout_sec` defaults to **60 seconds**.
5. **OpenAI function calling best practices**: https://platform.openai.com/docs/guides/function-calling
   - Clear names and parameters; use enums and structure to rule out invalid states.
   - "Combine functions that are always called in sequence".
   - Keep the initially available functions few (soft advice: fewer than 20).
   - Do not make the model fill in parameters you already know.

---

## 1. What v3 changes relative to v2, and why

| # | v2 | v3 | Reason |
|---|---|---|---|
| 1 | 8–9 tools: list_devices, query_logs, wait_logs, get_crashes, get_app_starts, list_processes, recording, clear_logs, mark | **4 read-only tools**: `logcat_status`, `logcat_search`, `logcat_crashes`, `logcat_app_launches` | Overlap, plus a class of "control state" tools: recording, clear and mark make the agent worry about hidden state. Anthropic and OpenAI both advise few, well-chosen tools and merging tools that are always called in sequence |
| 2 | `query_logs` and `wait_logs` separate | Merged into `logcat_search`; long polling with `after` + `wait_ms` | Same filtering and same result |
| 3 | `list_devices` and `list_processes` are two tools | Merged into `logcat_status(package?)` | The agent's first question is usually "is the device there, is it recording, is the app running": one answer |
| 4 | `recording` starts and stops, records only one device automatically | **All online devices are recorded automatically** (at most 4, configurable); no start/stop tool | Removes the hidden state "which device is being recorded now"; works with zero parameters |
| 5 | `clear_logs` can run `logcat -c` | **Removed.** Replaced by "take a cursor, then reproduce" (`logcat_status` returns `cursor`) | Clearing is destructive and agents sharing a device interfere; a cursor gives "only the logs after this reproduction" |
| 6 | `mark` writes a marker to the device with `log -t` | **Removed**, replaced by the cursor | One tool less, one device write less |
| 7 | A filter parse failure silently falls back to substring matching with a warning, as AS does | **Strict mode**: a parse error returns `isError:true` with the column, a `^` marker and a suggested rewrite. Every successful response **echoes the normalized interpretation** (`filter parsed as: …`) | A silent fallback gives the agent wrong results without any sign. The echo exposes counter-intuitive AS semantics such as the implicit OR on the same key |
| 8 | Only an AS filter string | The AS `filter` plus a few structured parameters: `package`, `tags[]`, `min_level`, `from`/`to`, **always ANDed** | The most common intents (an app, a few tags, a level, a time window) need no syntax, which lowers the error rate; use filter when more expressiveness is needed |
| 9 | Output is JSON or text, 200 entries by default | **Compact text first**: the header states time zone and date once, each line is `HH:MM:SS.mmm L pid/tid package tag: msg`. Plus a hard budget of 30,000 characters and explicit guidance on how to get more | JSON repeats the key names on every line, roughly 2–3 times the tokens of text **[inferred]**; Claude Code saves output above 50,000 characters to a file |
| 10 | No summary | `view:"summary"`: statistics by level, tag and package, plus the top repeated message templates. A truncated lines response gets a mini summary automatically | Seeing the whole picture first and then narrowing saves tokens compared with dumping thousands of lines |
| 11 | No de-duplication | Consecutive identical lines fold into `↳ ×37 identical until 01:20:09.001`. Multi-line messages (stacks) stay whole | Saves tokens and keeps stacks complete |
| 12 | Paging by seq or time only | **Opaque string cursor** containing device, boot and seq, valid across server restarts. `before`/`after` directions | Stateless, replayable, idempotent |
| 13 | get_crashes looks at logs only | Adds **ANR** (events `am_anr` + the ActivityManager `ANR in` block) and supplements traces and past crashes from **dropbox** | Confirmed by the user. dropbox also recovers crashes from "before the MCP started" and from logs that have already rolled over |
| 14 | Crash IDs undefined | Short IDs (`cr7`, `anr2`). With exactly one match the **detail is returned automatically**, including the 40 log lines before the crash from the same device process | Anthropic notes that semantic or short IDs reduce hallucination; one call less |
| 15 | get_app_starts returns raw evidence | `logcat_app_launches` **gives the computed durations**: request → device process start → onCreate → onResume, TTID, TTFD, cold/warm/hot, with the times of the evidence lines | The agent does not have to compute; a cold-start scenario needs 1 call |
| 16 | Time: `since` takes epoch, RFC3339 or a relative duration | `from`/`to` take **device-local time** (`2026-10-10 01:20:00` or `01:20:00`), ISO with offset, or a relative duration (`30m`). Output is always device-local time, with the zone in the header | Device logs are device-local time to begin with. Mixing host and device time zones is a common cause of agent mistakes |
| 17 | Unbounded long wait | `wait_ms` is at most **25,000**; default 0 | Codex's default tool timeout is 60 seconds |
| 18 | v2 set no annotations on any tool | All 4 tools set `readOnlyHint:true`, `openWorldHint:false` | Recommended by the specification; an MCP host can skip confirmation based on it (MCP host behavior **[unknown]**) |

**Explicitly unchanged**: the AS syntax itself (keys, operators, precedence, implicit OR on the same field, case rules); only the failure behavior changes to an error. Storage, the recording process (v2: daemon; now the service process), device discovery and pid mapping stay as in v2.

---

## 2. The final tool set (4 tools, all read-only)

**Common conventions**
- All tool names use the `logcat_` prefix; models know the word logcat. The recommended server name in the MCP host configuration is `android-log`, which Claude Code shows as `mcp__android-log__logcat_search`.
- Every tool has an optional `device` parameter. With only one device that has data it is selected automatically; with several and none given, an actionable error lists the serials of all devices.
- Return format: v1 returns **TextContent only** (compact text) and declares no outputSchema. See open question 2 in §7.
- annotations: `{"readOnlyHint": true, "openWorldHint": false}`, plus a `title`.
- The server instructions (`InitializeResult.instructions`) carry a workflow note of at most 120 words, see 2.5. Whether an MCP host injects them into the model context is **[unknown]**, so every tool description must also be readable on its own.
- Tool descriptions are written in **English**: models know English tool descriptions best and community tools are almost all in English. The rest of this document is also English.

### 2.1 `logcat_status`

**description**
```
Check adb/device/recording state and (optionally) whether an app is running. Call this first if unsure which device is connected, or to get a `cursor` marking "now" before the user reproduces a bug (then pass it as `after` to logcat_search).
Logs from all connected devices are recorded automatically in the background (persisted across sessions); there is nothing to start or stop.
With `package`, also reports install state and live/recently-dead processes (pid, process name, start/end time), including multi-process names like com.foo:remote.
```

**inputSchema**
```json
{
  "type": "object",
  "properties": {
    "device":  {"type": "string", "description": "adb serial. Omit when only one device is connected."},
    "package": {"type": "string", "description": "Exact application id, e.g. com.example.app. Optional."}
  },
  "additionalProperties": false
}
```

Since #23 the lines that rarely change (`root`, `linger`, `lifetime`, `service log`, `adb`, `store`) are shown on the first call of a connection to the service process and again when they change; `verbose: true` always shows them.

**Output example** (a sketch; the data is made up)
```
adb: ok (server 1.0.41)
devices (1):
  emulator-5554  online  Pixel_8_API_35  android 15 (sdk 35)  tz Asia/Shanghai (+08:00)
    device time now: 2026-10-10 01:44:12   recording since 2026-10-09 18:02:31   history from 2026-10-09 17:58:10
    stored 1,204,331 lines; gaps: 1 (2026-10-09 22:10:04–22:10:19, device disconnected)
cursor (now): c1.ZW11LTU1NTQ.9f2a.1204331
package com.foo: installed (uid 10213)
  running: pid 23011 com.foo (since 01:40:55), pid 23102 com.foo:push (since 01:40:57)
  recently dead: pid 21877 com.foo (01:31:02–01:39:47, crashed: see logcat_crashes id=cr3)
```

### 2.2 `logcat_search`

**description**
```
Search recorded logcat lines (buffers main, system, crash, events) of one device. Returns matching lines oldest→newest in a compact format; by default the NEWEST `limit` matches.
Filtering = `filter` (Android Studio Logcat query syntax) AND the structured params (package, tags, min_level, from, to). All parts are ANDed.
Filter syntax (Android Studio): key:value contains (case-insensitive), key=:value exact, key~:regex (RE2, case-sensitive), -key:value negates. Keys: tag message package process line level age is pid buffer. level:error = error and above. age:5m. is:crash, is:stacktrace. Bare words search the whole line.
Combining: `&` AND, `|` OR (put spaces around them), parentheses group. Space-separated terms are ANDed, EXCEPT repeated positive terms on the same key, which are ORed: `tag:A tag:B` = A OR B; use `message:foo & message:bar` for AND. Quote values with spaces: message:"Start proc".
Examples:
  tag=:OkHttp tag=:MyRepo level:debug
  is:crash | tag~:"^am_(anr|crash)$"
  message~:"Displayed com\.foo" -tag:chatty
Invalid filters return an error with the position and a fix; nothing is guessed. The response echoes how the filter was interpreted — check it.
Paging: response gives `before`/`after` cursors. To wait for new lines (e.g. while the user reproduces), pass `after` plus `wait_ms` (≤25000).
Use view="summary" first for broad or unknown time ranges (counts by level/tag/package, top repeated messages).
```

**inputSchema**
```json
{
  "type": "object",
  "properties": {
    "device":    {"type": "string", "description": "adb serial. Omit when only one device has logs."},
    "filter":    {"type": "string", "description": "Android Studio Logcat query, e.g. `tag=:OkHttp level:warn -message:heartbeat`. Optional."},
    "package":   {"type": "string", "description": "Exact application id; matches lines from ALL its processes (com.foo, com.foo:remote, a dotted global process name such as com.foo.remote), including processes that have since died."},
    "tags":      {"type": "array", "items": {"type": "string"}, "maxItems": 20, "description": "Exact tag names, ORed together, e.g. [\"OkHttp\",\"MyRepo\"]."},
    "min_level": {"type": "string", "enum": ["verbose", "debug", "info", "warn", "error", "fatal"], "description": "Minimum priority."},
    "from":      {"type": "string", "description": "Start time, inclusive. Device-local `2026-10-10 01:20:00` or `01:20:00` (= device's today), ISO-8601 with offset, or relative like `30m`, `2h` (= device now minus that)."},
    "to":        {"type": "string", "description": "End time, exclusive. Same formats as `from`. Default: now."},
    "view":      {"type": "string", "enum": ["lines", "summary"], "default": "lines"},
    "limit":     {"type": "integer", "minimum": 1, "maximum": 1000, "default": 50, "description": "Max matching lines to return (output is also capped at ~30k chars). For an overview of a range, view=\"summary\" is about 2k chars."},
    "stack":     {"type": "string", "enum": ["short", "full"], "default": "short", "description": "How Java stacks inside a line are shown: short = each exception and Caused by with its first 3 frames, a repeated stack shown once, at most 2,000 chars per line; full = every frame (up to 8,000 chars per line)."},
    "context":   {"type": "integer", "minimum": 0, "maximum": 20, "default": 0, "description": "Also show N lines before/after each match from the same process (like grep -C)."},
    "before":    {"type": "string", "description": "Cursor: return matches older than this (from a previous response)."},
    "after":     {"type": "string", "description": "Cursor: return matches newer than this (from a previous response or logcat_status)."},
    "wait_ms":   {"type": "integer", "minimum": 0, "maximum": 25000, "default": 0, "description": "With `after`: block up to this long until at least one new match arrives."}
  },
  "additionalProperties": false
}
```

**Combination and precedence (part of the implementation specification)**
- Final predicate = `filter` ∧ `package` ∧ (any of `tags`) ∧ `min_level` ∧ `[from, to)` ∧ cursor bound.
- Structured parameters **never override** the filter; they only intersect. For example, a filter containing `age:10m` together with `from=01:00` yields the intersection of both.
- A combination that is statically known to be empty (for example `from` ≥ `to`, or `before` and `after` given together) is an error with an explanation, not "0 lines".
- `before` and `after` cannot both be given. With `after`, the first `limit` matches from oldest to newest are returned (for following new lines); otherwise the newest `limit` matches.
- The structured parameter `package` is an **exact applicationId match**; `package:` in AS syntax is contains. The difference must be stated in the description.

**Output example: lines** (sketch)
```
device emulator-5554 · times are device-local Asia/Shanghai (+08:00) · date 2026-10-10
filter parsed as: package == com.foo AND (tag = "OkHttp" OR tag = "MyRepo") AND level >= DEBUG AND time in [01:20:00, 01:30:00)
matched 1,284 lines; showing newest 50 (01:29:12.002–01:29:58.731)
count: total=1284 count_exact=true   (design sketch; fmt=1 uses matched=N exact=1 in the header)
format: time level pid/tid process tag: message
01:27:41.002 D 23011/23090 com.foo OkHttp: --> GET https://api.foo.com/v1/feed
01:27:41.315 D 23011/23090 com.foo OkHttp: <-- 200 https://api.foo.com/v1/feed (312ms)
01:27:41.316 I 23011/23011 com.foo MyRepo: feed loaded items=20
  ↳ ×37 identical until 01:27:49.001
01:27:50.120 W 23102/23110 com.foo:push MyRepo: token refresh failed
    java.net.UnknownHostException: Unable to resolve host "push.foo.com"
    	at java.net.Inet6AddressImpl.lookupHostByName(Inet6AddressImpl.java:156)
    	at java.net.Inet6AddressImpl.lookupAllHostAddr(Inet6AddressImpl.java:103)
    	at java.net.InetAddress.getAllByName(InetAddress.java:1152)
    	↳ 21 more frames
    Caused by: android.system.GaiException: android_getaddrinfo failed: EAI_NODATA (No address associated with hostname)
    	at libcore.io.Linux.android_getaddrinfo(Native Method)
    	at libcore.io.ForwardingOs.android_getaddrinfo(ForwardingOs.java:74)
    	at java.net.Inet6AddressImpl.lookupHostByName(Inet6AddressImpl.java:135)
    	↳ 27 more frames
01:28:20.311 W 23102/23110 com.foo:push MyRepo: token refresh failed
    java.net.UnknownHostException: Unable to resolve host "push.foo.com"
    Caused by: android.system.GaiException: android_getaddrinfo failed: EAI_NODATA (No address associated with hostname)
    	↳ same 54 frames as 01:27:50.120
…
stacks: 2 shortened (102 frames hidden) · stack="full" shows every frame
[truncated: 1,234 older matches not shown · view="summary" shows what the whole range contains]
more: before="c1.ZW11LTU1NTQ.9f2a.1188020"  ·  newer/wait: after="c1.ZW11LTU1NTQ.9f2a.1190311"
top tags in all 1,284 matches: OkHttp 911, MyRepo 373 · narrow with e.g. -message:heartbeat, min_level=info, or a smaller from/to
```

**Line format details**
- The time shows only `HH:MM:SS.mmm`; the date is in the header. When the range crosses midnight a `--- 2026-10-11 ---` separator line is inserted.
- The process column shows the device process name (a multi-process app shows `com.foo:push`); `?` when the pid is unknown.
- A single message longer than 2,000 characters is cut and marked `…[+N chars, use filter on this line's pid+time to get full]`. Stack-like messages are allowed up to 8,000 characters.
- Surrounding lines brought in by `context` are indented by two spaces; matching lines are not.
- Folding rule: lines fold only when pid, tag and level are the same and the message bytes are identical and adjacent. No digit normalization, to avoid false folds.
- The time source is always the device timestamp.

**Output example: summary** (sketch)
```
device emulator-5554 · Asia/Shanghai (+08:00) · 2026-10-10 01:20:00–01:30:00
filter parsed as: package == com.foo
total 18,402 lines · by level: V 0 D 12,110 I 5,201 W 902 E 187 F 2
top tags: OkHttp 6,021 · Choreographer 2,310 · MyRepo 1,904 · RenderThread 1,200 · …(10 shown)
top processes: com.foo 17,980 · com.foo:push 422
top repeated messages (digits normalized to #):
  4,010× OkHttp: <-- 200 https://api.foo.com/v1/feed (#ms)
  2,301× Choreographer: Skipped # frames!  The application may be doing too much work on its main thread.
crashes in range: 1 (cr3 01:24:10 java.lang.IllegalStateException) · ANRs: 0
latest errors (5): 01:24:10.511 E 21877 AndroidRuntime: FATAL EXCEPTION: main …
next: logcat_search with filter/tags narrowed, or logcat_crashes id="cr3"
```

### 2.3 `logcat_crashes`

**description**
```
List Java crashes, native crashes (tombstones) and ANRs, newest first, already parsed and grouped — use this instead of searching for "FATAL EXCEPTION" yourself.
Sources: crash/system/events log buffers (AndroidRuntime FATAL EXCEPTION, libc "Fatal signal" + DEBUG tombstone lines attributed to the crashed pid, am_anr + "ANR in" block) plus the device DropBox (data_app_crash / data_app_native_crash / data_app_anr), which also covers crashes from before recording started and ANR thread dumps.
Pass `id` (e.g. "cr3") for full detail: complete stack / Caused-by chain, the crashed process's last 40 log lines before the crash, and for ANRs the reason plus main-thread trace (or why traces are unavailable). If only one crash matches, detail is returned automatically.
```

**inputSchema**
```json
{
  "type": "object",
  "properties": {
    "device":  {"type": "string"},
    "package": {"type": "string", "description": "Exact application id. Omit for all apps."},
    "kinds":   {"type": "array", "items": {"type": "string", "enum": ["java", "native", "anr"]}, "description": "Default: all three."},
    "from":    {"type": "string", "description": "Same formats as logcat_search.from. Default: everything recorded."},
    "to":      {"type": "string"},
    "id":      {"type": "string", "description": "Crash id from a previous list (e.g. cr3, anr1) to get full detail."},
    "limit":   {"type": "integer", "minimum": 1, "maximum": 50, "default": 10}
  },
  "additionalProperties": false
}
```

**ID scheme**
- Per device, monotonically increasing: `cr<N>` is a crash (Java and native share one sequence), `anr<N>` an ANR. IDs are persisted and stable across sessions.
- With `device` given, an ID is unique within the device; with several devices, the ID gets a short serial prefix **[to be decided]**.

**List output** (sketch)
```
device emulator-5554 · Asia/Shanghai (+08:00) · package com.foo · 3 crash groups (5 events) in recorded history
cr5  2026-10-10 01:24:10  java   pid 21877 com.foo (thread main)
     java.lang.IllegalStateException: Fragment FeedFragment not attached to a context.
       at androidx.fragment.app.Fragment.requireContext(Fragment.java:972)
       at com.foo.feed.FeedFragment.onLoaded(FeedFragment.kt:88)
       at com.foo.feed.FeedViewModel$load$1.invokeSuspend(FeedViewModel.kt:41)
       … 23 more frames · same signature also at 01:12:03 (cr2), 00:58:40 (cr1)
anr2 2026-10-10 00:40:12  anr    pid 20111 com.foo  reason: Input dispatching timed out (… is not responding. Waited 5001ms for KeyEvent)
     main thread: Blocked on lock held by thread 23 "DiskIO" — at com.foo.db.Cache.get(Cache.kt:55)   [traces: dropbox data_app_anr]
cr4  2026-10-09 23:02:55  native pid 19876 com.foo  SIGSEGV (SEGV_MAPERR) fault addr 0x0 in libfoo.so (Java_com_foo_Native_decode+44)
more: logcat_crashes id="cr5" for full detail
```

**Detail** (when `id` is given, or exactly one match) contains:
- The full stack with the complete Caused by chain. When the total exceeds 20,000 characters, each section keeps only its first 40 frames and last 5 frames, and says how many were omitted.
- `before crash (pid 21877, last 40 lines)`: in the line format of logcat_search. The shipped default is 20 lines (`before_lines`, see tools.md).
- Native crash: signal, abort message, backtrace (all frames), build fingerprint.
- ANR: Subject or reason, a CPU usage summary (first 10 lines), the full main-thread trace, and the trace of the thread that holds the lock it waits for.
  - When traces cannot be obtained, say why, for example: `traces: unavailable — dropbox entry not found / rate-limited / dumpsys dropbox permission denied. /data/anr needs root. Fallback: run "adb bugreport" yourself.`

**Facts about ANR and dropbox**
- AMS writes an ANR to dropbox with the tag `<processClass>_anr`, where processClass is `system_server`, `system_app` or `data_app`. It passes tracesFile as the dataFile and, when size allows, **inlines the traces content**. dropbox is rate-limited (DropboxRateLimiter).
  - Source [source]: frameworks/base main, `ActivityManagerService.addErrorToDropBox`, `ProcessErrorStateRecord.java` (`addErrorToDropBox("anr", …, tracesFile, …)`)
- The arguments of `dumpsys dropbox` are `[-p|--print] [-f|--file] [--proto] [timestamp]`; other arguments not starting with `-` are search conditions for a tag or a time.
  - The permission check is `checkDumpAndUsageStatsPermission`, which **lets SHELL_UID through**. [source]: `DropBoxManagerService.java`, `DumpUtils.java` (main branch)
- **[unknown]**: whether dropbox tags are enabled, how many entries are kept and whether traces are truncated on old versions and OEM ROMs; all need verification on real devices.
- Implementation: the service process, on a timer (for example every 60 seconds) or after seeing `am_anr`/`am_crash`, runs `dumpsys dropbox --print data_app_anr` (and the same for crashes), parses the result and caches it locally. The agent does not wait when it calls.

### 2.4 `logcat_app_launches`

**description**
```
Report recent app launches with computed timings — use this for "how long does cold start take / time to first onResume".
For each launch: cold/warm/hot, the launched activity, and a timeline relative to the launch request: process start (am_proc_start), Application/Activity onCreate, onStart, onResume (from wm_on_*_called events in the app process), Displayed (time to initial display, TTID) and Fully drawn (TTFD, only if the app calls reportFullyDrawn()).
This tool only reads logs; launch the app yourself first (e.g. `adb shell am force-stop <pkg>` then `adb shell am start -W -n <pkg>/<activity>`), or ask the user to tap the icon, then call this.
```

**inputSchema**
```json
{
  "type": "object",
  "properties": {
    "device":  {"type": "string"},
    "package": {"type": "string", "description": "Exact application id. Omit for all apps."},
    "from":    {"type": "string", "description": "Default: last 30 minutes."},
    "to":      {"type": "string"},
    "limit":   {"type": "integer", "minimum": 1, "maximum": 20, "default": 5}
  },
  "additionalProperties": false
}
```

**Output example** (sketch)
```
device emulator-5554 · Asia/Shanghai (+08:00) · package com.foo · 2 launches
#1 2026-10-10 01:40:55.102  COLD  com.foo/.ui.MainActivity  (pid 23011)
   +0 ms     launch request      START u0 {act=MAIN cat=[LAUNCHER] cmp=com.foo/.ui.MainActivity} from uid 10132
   +38 ms    process started     am_proc_start pid 23011
   +412 ms   MainActivity.onCreate returned   (onCreate took 186 ms)
   +431 ms   onStart returned    (took 9 ms)
   +440 ms   onResume returned   (took 6 ms)   ← request→first onResume: 440 ms
   +812 ms   Displayed (TTID)    system reports +812ms
   —         Fully drawn         not reported (app does not call reportFullyDrawn)
#2 2026-10-10 01:35:12.880  WARM  …
caveats: onX "returned" times are when the callback finished (event logged after return); TTID is measured by the system from the launch request.
```

**Algorithm notes**
- Anchor: ActivityTaskManager's `START u0 {… cmp=<pkg>/…}`.
- Cold start: an `am_proc_start` of that package appears after the anchor and before the first onResume.
- Warm start: the device process already exists, but a `wm_on_create_called` appears.
- Hot start: only `wm_on_resume_called`, no onCreate.
- `wm_on_*_called` must come from the pid of that package and the component name must match.
- `Displayed` is accepted under both the ActivityTaskManager and ActivityManager tags, with or without ` for user N`.
- When evidence is missing, write `not found` per item with a guess at the reason, for example: the events buffer was not recorded; the launch was before the start of the history.
- The cold/warm/hot rules above are **[inferred]** and must be cross-checked against the LaunchState of `am start -W`.

### 2.5 Server instructions (draft)

```
Android logcat recorder. Logs from every connected device are captured continuously in the background and kept across sessions; nothing to start/stop/clear.
Typical flows: crashes/ANRs → logcat_crashes. Startup time → logcat_app_launches. Anything else → logcat_search (use view="summary" first for wide ranges).
To capture a reproduction: logcat_status → take `cursor` → user reproduces → logcat_search(after=cursor, wait_ms=20000).
All times are device-local; responses state the device timezone.
```

---

## 3. The filter language exposed to the agent (AS syntax + a few extensions + strict errors)

### 3.1 Syntax cheat sheet

| Form | Meaning |
|---|---|
| `tag:x` `message:x` `package:x` `process:x` `line:x` | contains, case-insensitive |
| `key=:x` | exact equality, case-sensitive |
| `key~:re` | partial regex match, case-sensitive; `(?i)` turns case sensitivity off; the engine is RE2 |
| `-key:x` `-key=:x` `-key~:re` | negation |
| `level:<verbose\|debug\|info\|warn\|error\|assert>` | at or above that level |
| `age:<N>[smhd]` | the last N seconds, minutes, hours, days (by the device clock, see 3.4) |
| `is:crash` `is:stacktrace` | same definition as AS |
| a bare word or `"with space"` | contains, on the whole line |
| `a & b`, `a \| b`, `( … )` | AND, OR (`&` binds tighter), grouping; the operators **must have spaces on both sides** |
| terms separated by spaces | AND; **exception**: positive terms on the same key are ORed; so are several `level:`, several `age:`, and `is:crash` with `is:stacktrace` |

**Extensions (not AS; marked `[ext]` in the description and the echo)**
- `pid:<n>`, `tid:<n>`: exact match.
- `buffer:<main|system|crash|events>`: can be negated.
- `level:` additionally accepts the single letters `V/D/I/W/E/A/F` and `fatal` (same as assert). Agents often carry over logcat's `*:E` habit.
- `name:` and `package:mine`: **not supported**, because MCP has no notion of a "current project". They return an error that suggests the `package` parameter.

### 3.2 Interpretation echo

The first line of every successful response gives the normalized predicate tree, for example:

`filter parsed as: (tag = "OkHttp" OR tag = "MyRepo") AND level >= DEBUG AND NOT message contains "heartbeat"`

An agent can use this to notice that its `message:foo message:bar` was interpreted as OR.

### 3.3 Error behavior (instead of AS's silent fallback)

Every error is returned with `isError: true`; the text has three parts: the problem, the position, the fix.

| Case | Example text returned |
|---|---|
| Unknown key | `Filter error at col 12: unknown key "lvl". Keys: tag message package process line level age is pid tid buffer.`<br>`  tag:OkHttp lvl:error`<br>`             ^`<br>`Did you mean: level:error` |
| Uppercase key | `Filter error at col 1: keys are lowercase — use tag: instead of Tag:` |
| Invalid level | `level:"err" is not a level. Use one of verbose debug info warn error assert (or V D I W E A F).` |
| Invalid age | `age:"10min" — use <number><s|m|h|d>, e.g. age:10m` |
| Unclosed quote or unbalanced parentheses | The column, plus a suggested balanced rewrite |
| Regex not supported by RE2 | `tag~:"(?<!Ok)Http" uses look-behind, which is not supported (regex engine: Go RE2 — no look-around, no backreferences). Rewrite, e.g.: tag~:Http -tag~:OkHttp` |
| Space-separated terms inside parentheses `(a b)` | `Inside parentheses terms must be joined with & or \|, e.g. (tag:a & level:error)` |
| `name:` or `package:mine` | `package:mine is not supported here (no IDE project). Use the package parameter: package="com.foo"` |

**Warnings** (not errors; written on the header `note:` line)
- Suspicious form `tag:foo|bar`: `note: value "foo|bar" contains "|" — if you meant OR write tag:foo | tag:bar`
- When several terms on the same key are merged into an OR: `note: tag:A tag:B are ORed (same key)`
- With 0 results, give the most likely reasons: the package had no device process in that time range; the tag differs in case (`=:` is case-sensitive); the time window is before the start of the history; which similar tag names exist nearby.

### 3.4 Intentional differences from AS (all go into the description or the documentation)

1. Errors are reported, not degraded.
2. Extension keys pid, tid, buffer, and single-letter levels.
3. `name:` and `package:mine` are not supported.
4. `age:` uses the **estimate of the device clock**; AS uses the host clock. The reason is that log timestamps come from the device. Device "now" = host now + the offset measured at connection time; the offset is measured with `adb shell date +%s`, accurate to 1 second, which is enough **[inferred]**.
5. The regex engine is RE2, not java.util.regex.
6. `foo bar` is interpreted as AND according to the AS **code**; the AS documentation says it is a phrase. The two conflict, see v2.

---

## 4. Errors and edge cases (actionable text)

| Case | Returned (isError) |
|---|---|
| adb not on PATH | `adb not found. Install Android platform-tools or set ANDROID_LOG_MCP_ADB=/path/to/adb (also checked: $ANDROID_HOME/platform-tools, ~/Library/Android/sdk/platform-tools).` |
| adb server fails to start | Append the first 3 lines of the error output of `adb start-server` |
| No device | `No device connected. Connect a device with USB debugging enabled or start an emulator; logcat_status shows the state. Previously recorded devices: emulator-5554 (last seen 01:10:22) — pass device="emulator-5554" to query its stored logs.` |
| Device unauthorized | `Device R5CT… is unauthorized: accept the "Allow USB debugging" prompt on the phone.` |
| Several devices and none given | `2 devices have logs; pass device=one of: emulator-5554 (Pixel_8_API_35, online), R5CT12345 (SM-S918B, online).` |
| The given device has no records | List the devices that have records |
| Package never seen | `No logs from package "com.fo" in range. Did you mean com.foo? (installed: yes). Known packages with logs in range: com.foo, com.android.systemui, …` (not an error: 0 lines with a hint) |
| The app is not running now | Not an error: `com.foo has no running process (last pid 21877 died 01:39:47).` |
| Time cannot be parsed | `from="yesterday 3pm" not understood. Use "2026-10-10 15:00:00", "15:00:00", ISO-8601, or a duration like "30m".` |
| Time window before the start of the history | Returned normally, with a note in the header: `note: history starts 2026-10-09 17:58:10; earlier logs were not recorded (device buffer had rolled over).` |
| Cursor no longer valid (device rebooted or data pruned) | `cursor refers to boot 9f2a which has been pruned/replaced; showing from oldest available instead.` (this is a note; only a malformed cursor is an error) |
| `wait_ms` timed out | Not an error: `no new matches within 20000 ms; call again with the same after cursor to keep waiting.` |
| Output budget exhausted | See the truncation tail in 2.2: the remaining count, the cursor, suggestions to narrow the range |
| Recording gap | Note in the header of the affected time range: `gap: 22:10:04–22:10:19 (device disconnected)` |

---

## 5. Scenario walk-through (fewest calls)

**A. Time from a cold start to the first onResume** (1–2 calls)
1. The agent runs `adb shell am force-stop com.foo && adb shell am start -W -n com.foo/.ui.MainActivity` with its own shell; without shell access, ask the user to open the app by hand.
2. `logcat_app_launches(package="com.foo")` returns `request→first onResume: 440 ms` and the time of each phase directly.
3. To see the app's logs during the start: `logcat_search(package="com.foo", from="01:40:55", to="01:40:57")`, with the absolute times taken from the previous result.

**B. Crash or ANR analysis** (1–2 calls)
1. `logcat_crashes(package="com.foo")`. With a single match it returns the detail directly: the full stack plus the 40 lines before the crash. With several, the list already has the exception, the first app frame and the repeat count.
2. When needed, call `logcat_crashes(id="cr5")`, or look at a wider context: `logcat_search(package="com.foo", from="01:23:40", to="01:24:11", min_level="debug")`.
3. An ANR takes the same path. The detail has the reason and the main-thread trace; when traces are unavailable it says why and gives an alternative (bugreport).

**C. Locating a bug with a few tags** (1–3 calls)
1. When the range is unclear, first `logcat_search(package="com.foo", from="15m", view="summary")` to see the top tags and repeated messages.
2. `logcat_search(package="com.foo", tags=["OkHttp","MyRepo","Billing"], min_level="debug", from="15m")`。
3. To exclude: `filter="-message:heartbeat -tag:Choreographer"`. To AND two message conditions: `filter="message:order & message~:\"id=\\d+\""`.

**D. A given time window** (1 call)
- `logcat_search(from="2026-10-10 01:20:00", to="2026-10-10 01:25:00", filter="level:warn")`, or a relative time `from="30m"`, or `filter="... age:10m"`; the results intersect.

**E. Reproducing live** (2 calls)
1. `logcat_status()` to get the `cursor`.
2. Ask the user to reproduce.
3. `logcat_search(after=cursor, package="com.foo", wait_ms=20000)`. If no new logs arrived, call again with the same cursor.
4. Alternatively, after the reproduction call `logcat_crashes(package="com.foo", from=<the time from the previous step>)` directly.

---

## 6. Other decisions related to agent friendliness

- **No hidden session state**: every tool is a pure query; the same parameters with the same cursor give the same result (except `wait_ms` and "newest N lines", which change over time). The cursor is an opaque string `c1.<base64 serial>.<boot>.<seq>` that survives service restarts and can be shared between agents.
- **Every zero-parameter call is useful**:
  - `logcat_status()`: devices, recording state, cursor.
  - `logcat_search()`: the newest 50 lines (200 before #23), with a top-tags summary.
  - `logcat_crashes()`: recent crashes of all apps.
  - `logcat_app_launches()`: launches in the last 30 minutes.
- **Size control**:
  - The hard budget per response is 30,000 characters, leaving headroom below Claude Code's 50,000-character save-to-file threshold; about 8–10k tokens.
  - The default limit is 50 (#23; it was 200, whose default output of about 27.5k characters was cut by the 30k budget at about 175 lines). Beyond that the output uses the truncation format of 2.2, and the `[truncated: …]` line points to `view="summary"`; when the 30k budget cannot hold `limit` lines there is an extra `[output cap: …]` line. Since #85 (`fmt=1`) the same information is one `truncated … next:` line; see reference.md.
  - **Short stacks (#23).** A Java stack logged with a line is one record of 20–40 frame lines (~1 KB); shown in full it dominated the response whatever `limit` was. With `stack="short"` (default) each exception and each `Caused by` keeps its head and first 3 frames (`↳ K more frames`, Java's `... N more` and the frames of a `Suppressed:` exception included); a stack whose frames were already printed in the same response becomes `↳ same K frames as HH:MM:SS.mmm`; a line holding a stack is cut at 2,000 chars, keeping every `Caused by` head and its first frame first (the last `Caused by` is usually the root cause). A `stacks: N shortened (M frames hidden)` line says when anything was left out. `stack="full"` shows every frame (8,000-char cap), as before. When the context lines overflow the budget, whole records are dropped so a `same frames` reference never points at a line that was cut.
  - Per-line caps (2,000 chars, 8,000 for a full stack) apply to every query; the cut marker says so (`…[+N chars; lines are cut at 2,000 chars in every query, narrowing does not show more]`).
  - Declaring `anthropic/maxResultSizeChars` for Claude Code could be considered; whether it is worth it is **[unknown]**.
- **Match counts (issue #20).** `logcat_search` stops counting 100 ms after its page (`limit`) is full. The total is then a lower bound, and the text says which span was counted and how to get an exact count: `matched ≥14,043 lines (counted the newest 21 min only, since 23:32:09; older history not counted — narrow with from/to, e.g. from="1h", for an exact count)`. Without a filter or time window, the exact total comes from the store's record range (`Store.Count`: the in-memory window plus the per-block line counts of the segment index), without decoding records. (Superseded by `fmt=1` (#85): there is no `count:` line; the header carries `matched=N exact=0|1`, with `counted_from` / `counted_to` when the count is a lower bound.) Every lines response had one machine-readable line, `count: total=N count_exact=true|false [counted_from|counted_to="<device-local time>"]`, so agents need not parse the prose. The top-tags line says when it covers only the counted matches (or only the page). These fields are a text line rather than MCP `structuredContent`, because Claude Code passes only `structuredContent` to the model when both are present, which would drop the log lines (see §7 Q2).
- **Timeouts**: only `wait_ms` blocks, at most 25 seconds. Other queries aim to return within 1 second (in-memory index plus segment scan). A cold query that scans a large stretch of history returns the part already scanned with `continue: before=<cursor>` and does not block for long.
- **Time zone**:
  - All output uses device-local time, with `tz <IANA> (+hh:mm)` in the header. The zone comes from `getprop persist.sys.timezone`; the exact command **[needs a real device]**.
  - Absolute input times are also interpreted as device-local; an ISO time with an offset is converted by that offset.
  - When the host and device zones differ, `logcat_status` says: `note: host timezone differs (UTC-07:00)`.
- **pid with inline name**: every line carries the device process name, resolved at ingest time (the v2 approach), so an ended device process is also shown correctly.
- **Safety**: the tools run no arbitrary shell and write nothing to the device. They call only the fixed `logcat`, `ps`, `dumpsys dropbox`, `getprop`, `pm list packages -U` and `date`.

---

## 7. Open questions (need a user decision or measurement)

1. **Whether to provide a side-effecting `logcat_measure_launch(package, activity?, mode=cold|warm, runs=1..5)`**: it force-stops the app, runs `am start -W` and returns a summary.
   - Pro: MCP hosts without a shell (such as Claude Desktop) can measure a cold start in one step; several runs give a median.
   - Con: it kills the app (destructiveHint) and goes beyond "read logs only".
   - Recommendation: not in v1, or behind a start-up flag `--enable-actions`, off by default.
2. **structuredContent and outputSchema**: whether an MCP host hands the text or the structuredContent to the model is **[unknown]** and must be measured per MCP host. The specification says a tool that returns structuredContent SHOULD also give JSON text, which can double the tokens.
   - Recommendation: v1 returns text only. In v1.1, evaluate adding structuredContent on the small results of status, crashes and launches.
   - Finding (2026-10, #20): Claude Code forwards only `structuredContent` to the model when a result has both (anthropics/claude-code#9962, #55677). Machine-readable fields therefore go into a fixed text line (`count: …`), not `structuredContent`.
3. **Record all devices automatically**, or only one. The former is simplest for the agent, but resource use grows linearly with the number of devices; the limit of 4 was a guess.
4. **Whether strict errors need an "AS-compatible lenient mode" switch.** For the agent use case, the recommendation is not to offer one.
5. **Default limit and default folding of repeated lines.** Resolved: the default limit is 50 (#23; the earlier 200 items / 30k characters budget was too large, see §6) and consecutive identical lines are folded by default (§1 row 11). The Anthropic advice to run an eval (compare calls, tokens and success rate on real tasks) still applies to any further change.
6. **Availability of dropbox on old versions and OEM ROMs**, and whether ANR traces are truncated.
7. **The cold/warm/hot rules** and whether `wm_on_*_called` exists on old versions (these events exist on the main branch; the earliest version that has them is **[unknown]**).
8. **Whether English tool descriptions are acceptable**; a Chinese alias note can be added if needed.
9. The format of crash IDs with several devices.

## 8. Diagnostic bundle: the `report` subcommand (v0.2.0, issue #1)

Purpose: the user sends diagnostic data to the maintainer, who uses it to replay, reproduce and add regression tests. Usage and redaction rules are in the "Diagnostic reports and privacy" section of docs/reference.md; only the design trade-offs are recorded here.

- **Not an MCP tool but a CLI subcommand**: packaging writes local files and may start an app (`--am-start`), so it should not be something an agent calls casually. The subcommand reads the data directory only through `store.OpenReadOnly`, does not take the writer lock and does not start recording, so it does not interfere with the running recording process (now the service process).
- **The logs go into the bundle in two forms**:
  - `store/v1/<device-id>/` (since 0.4, manifest `format` `android-log-mcp-report/2`): this tool's own format, trimmed by window / package, seqs renumbered 1..N (the `seq` of a crash and the `first_seq` of a session are mapped by lower bound), one segment only. `--offline <unpacked dir>/store` loads it read-only (no recording, no pruning, no files created) and the 4 tools work as usual. 0.3 zips (`store/<serial>/`) can no longer be loaded (issue #7).
  - `devices/<serial>/logcat/<boot>.txt`: the same records re-emitted in the format used when recording, `logcat -v threadtime -v epoch -v uid -v usec -v printable`, one file per boot; DropBox entries keep the `dumpsys dropbox --print` format. The replay tests' `Pipeline.ReplayText` / `ApplyDropbox` load it unchanged, and crash detection and launch analysis run again. Known limitation: two adjacent records with identical headers are merged on replay (the same as "merging multi-line messages" in the README).
- **Redaction applies only to the body**: the log message, crash / ANR / tombstone text, DropBox body, `am start` output and the note. Time, pid, tid, uid, level, tag, package name, device process name and device properties are structured fields that do not pass through the redactor, so the structure cannot be damaged; the rules themselves also exclude timestamps, hex numbers, stack frames and version numbers (unit tests + a regression over the full API 29 fixture).
- **Placeholder `[REDACTED:<type>#n]`**: numbers are assigned in order of first appearance, the same value gets the same number; the mapping uses a randomly generated HMAC key per bundle that is discarded after use, and memory holds only the HMAC; the bundle never holds the original value. The regexes of `--redact-extra`, the domains of `--keep-domain` and the content of `--note` are not written into the manifest (only the count / whether there is one).
- **Domains**: hosts in URLs are redacted whatever the top-level domain (only the host is replaced; scheme / port / path stay); a bare host name is recognized only with a curated set of public top-level domains and when its first label is not a reverse-domain prefix (`com.`, `android.`, `java.`, `ro.`, `persist.`…), to avoid treating package names, class names, file names, property names and intent actions as domains. The cost is that bare host names ending in something that is both a file extension / common word and a TLD, such as `.so` `.md` `.sh` `.py` `.rs` `.id` `.top` `.online`…, are missed; those in URLs are not affected; `--redact-extra` can fill the gap. On the full API 29 fixture the domain rule hits only 11 real hosts (`connectivitycheck.gstatic.com`, `time.android.com`, `www.google.com` and so on); the test pins this list, and any extra hit counts as a false positive.
- **Size cap**: computed on "uncompressed JSONL + logcat text"; several devices share one cap and the oldest records are dropped first; the number dropped goes into the manifest, and a `report size cap` gap is added to the in-bundle `meta.json`, which `logcat_status` shows.
- **`--package`**: keeps that app's records (by the package name / device process name resolved at ingest) and the system records whose body mentions the package name (`START u0`, `am_proc_start`, `Displayed`, `ANR in`, etc.), so the launch timeline and the crash context stay complete in the bundle. `packages.json` keeps only that app's uid too, so the whole installed-app list does not leave the machine.
- **`--am-start`**: runs first and then collects the logs; after running it waits up to 8 seconds until the service process has persisted the logs from after the start of `am start`. When no service process is recording, `am-start.txt` says so. The component name is validated with a regex to prevent shell injection.

## 9. Memory and disk limits (issue #2)

Background: after long use in Codex, users reported high memory and disk use. The original implementation had these problems:
- Each device cached the most recent 100k lines in memory, counted by lines only;
- A history query cached up to 3 whole decoded segments (16 MiB each, larger once decoded);
- An MCP process / CLI process that opened the data read-only also pre-read the whole window at start and never released it;
- Auxiliary files were append-only and never cleaned;
- The data directory had no total cap, and recording did not pause when the disk was nearly full.

### 9.1 Memory

- **Recent window**: 20000 lines by default, and also limited to 16 MiB by estimated bytes, whichever comes first. Estimate = struct + 2 × (the lengths of message, tag, device process name and package name), where the second copy stands for the normalized line the filter engine caches on the record (`Record.Line`), so the estimate is on the high side. It trims only once the window exceeds the limit by 25%, so each append does not copy the whole window.
- **History reads work per block**:
  - The first time a segment is read, a block index is built: it is cut only at line boundaries into blocks of about 512 KiB, each recording its seq range and file offset; while indexing, only the leading `{"s":N` of a line is parsed, not the whole line.
  - For the segment being written, the index extends as the file grows, and the last, unfilled block is reopened and filled further.
  - A query decodes only the blocks that overlap the seq range, and decoded blocks go into an LRU counted in bytes (32 MiB by default). A single block larger than the limit is returned as usual but not cached.
  - When a segment is deleted, its index and cached blocks are dropped with it.
  - While decoding, short strings such as boot, tag, buffer, device process name and package name are interned within the block, so the records of one block share one copy of each string.
  - Record decoding (#22 D1, `store.DecodeRecord`): the hand-written decoder handles only the format the store itself writes (known single-letter keys, compact, integers in range, strings with any escapes). Any other input (unknown or duplicate keys, whitespace, floats, null, syntax errors) goes to `encoding/json` as a whole line, so results and errors are exactly those of `encoding/json`. Differential tests (`FuzzDecodeRecord`, all fixtures, real stores) compare the whole struct field by field.
- **Release when idle**: every query (`Scan`) resets an idle timer, 2 minutes by default. On expiry the history cache is cleared; for a read-only instance the recent window is dropped as well, and `debug.FreeOSMemory` returns memory to the system. The recorder (now the service process) keeps its recent window because it keeps appending to it anyway.
- **Read-only instances load on demand**: when a read-only instance opens, it reads only the last line backwards from the end of the last segment to get lastSeq, the newest time and the resume position, and does not pre-read the window. While not loaded, `Refresh` only advances lastSeq and accumulates no records. The first `Scan` loads the window backwards from the block index. `Stats` reads the first line directly for the earliest time, bypassing the cache, so `logcat_status` itself never triggers a load.
- Still only one process records (`writer.lock`). The shared service process is covered in issue #3.

Measurements: 300k log lines, 5 segments, a 76 MB data directory, `--offline` mode, one recorder (holding the lock) and one read-only reader. Each process ran 5 queries first, including a full-history scan, then idled for 150 seconds and queried once more. RSS and the live heap after the last GC:

| | RSS / heap before the change | RSS / heap after the change |
|---|---|---|
| Recorder: at start | 133 / 80 MiB | 20 / 6 MiB |
| Recorder: after 5 queries | 301 / 134 MiB | 65 / 26 MiB |
| Recorder: idle for 150 s | 301 / 98 MiB | 19 / 5 MiB |
| Reader: at start | 140 / 87 MiB | 10 / 0 MiB |
| Reader: after 5 queries | 278 / 138 MiB | 66 / 25 MiB |
| Reader: idle for 150 s | 278 / 98 MiB | 14 / 0 MiB |

Query results are byte-for-byte identical (search, crashes and launches were compared on the same data directory). Query time is the same or faster: a full scan takes about 1 second, and a single-segment time window dropped from about 1.0 second to about 0.55 second.

### 9.2 Disk

- **Auxiliary file compaction** (`device.CompactAux`, run only by the recorder, once about 30 seconds after start and every 10 minutes after that):
  - `procs.jsonl` and `crashes.jsonl` keep only the newest version of each entry, placed where the entry first appeared, so the results of `LoadProcs` / `LoadCrashes` do not change;
  - Ended device processes, crashes and `dropbox_seen` keys whose newest version was written before the retention period (`ANDROID_LOG_MCP_MAX_AGE`, default 1 day) are deleted. The write time is host time: every line carries `"ht"` (the host time of the write), and a `dropbox_seen` line is `{"k":"tag|b:body FNV-64a","ht":…}` (when the body states the crashed pid; otherwise `tag|device time`; old `tag|device time` keys remain valid, #63). A device clock set back to 1970 or forward by a year does not affect retention (#50). Old lines without `"ht"` are still judged by device time until they expire;
  - The device-side DropBox keeps 3 days by default, longer than the 1-day default retention, so after a `dropbox_seen` key is deleted the device may still hold the matching entry. So that these old crashes are not re-imported as new ones, `ApplyDropbox` does not import entries older than "time of the newest stored log line − retention" (by the device clock, so replaying a fixture does not drift with the date). The cutoff is device now − retention (#53);
  - After the device clock is set back, DropBox rewrites entries whose time is later than device now to the time of the next new entry (`DropBoxManagerService.createEntry`). So `dropbox_seen` identifies entries by body, not by time; a Java / native crash entry is also merged into an existing crash of the same boot, same pid and same process, whatever the device time difference (a process instance crashes only once). An ANR is still matched by the 60-second rule, because a process can continue after an ANR and ANR again (#63);
  - Compaction holds the auxiliary file lock, mutually exclusive with `AppendAux`, so no append is lost; it rewrites only when the file shrinks. `*.tmp` leftovers older than 10 minutes are deleted.
- **Crash index** (`device/crashindex.go`, #22): one in-memory index per device, shared inside the process by the absolute path of `crashes.jsonl` (the recorder and tool calls share it, as do read-only stores of the same directory).
  - Per crash ID only a summary of the newest version is kept (without `Body`, `Traces`, `CPU`, `Dropbox`, `Tombstone`), plus the offset and length of that version in the file; list views, grouping and DropBox matching use the summary, and when the stack or traces are needed the line is decoded by offset (`CrashFull`).
  - Every access first runs `Stat`: if the file is unchanged nothing is read; if it grew only the newly appended complete lines are decoded (an incomplete last line waits for next time); if the file was replaced (compaction rename), became shorter, or the 64 bytes before the end of the index no longer match, the whole index is rebuilt.
  - The order is exactly that of the former `LoadCrashes`: first by first appearance, then a stable sort by device time, newest first (no new time-order assumption is introduced; C2 of #48 is handled separately).
  - `EmitCrash` uses the summaries to pick the DropBox-only crash to merge and then decodes only that one; `ApplyDropbox` likewise. The compaction frequency is unchanged.
- **Total directory cap** (implemented since 0.4 by `layout.Sweep`, see §10; 2 GiB by default, checked every minute):
  - Sum the sizes of all files in all device directories and delete segments from oldest to newest by mtime; the last segment of each device is never deleted.
  - A directory this process records is cleaned through its own store (which keeps the in-memory segment table in sync); a directory nobody records is cleaned after taking `writer.lock`; a directory another android-log-mcp process records is skipped and left to that process.
- **Pause when free space is low** (`store.DiskGuard`, checked every 10 seconds):
  - The free-space check is abstracted as `FreeSpaceFunc`: statfs on Linux and macOS, `GetDiskFreeSpaceEx` on Windows, a fake injected in tests.
  - Pause when free space drops below 512 MiB; resume only above 2 times the threshold (1 GiB), leaving hysteresis so it does not toggle repeatedly.
  - A recording session first flushes its queue to disk, then records an open gap, then ends logcat; during the pause it still holds `writer.lock`, so no other android-log-mcp process takes over recording.
  - On resume it takes the `logcat -T` resume path: the first new record closes the gap; if the device buffer still holds lines that were already stored (so no data was lost), the gap is simply deleted.
  - `logcat_status` shows the pause reason and the resume threshold.

### 9.3 Configurable retention (v0.3.0)

- New `ANDROID_LOG_MCP_MAX_AGE`; the default changed from 7 days to **1 day**. It accepts two forms:
  - A Go duration (`72h`, `90m`);
  - A day suffix (`1d`, `7d`, `1.5d`).
- An empty value uses the default. An invalid or non-positive value prints a warning on stderr and falls back to the default. The other integer `ANDROID_LOG_MCP_*` variables now also warn on error; they used to be ignored silently.
- Retention still deletes whole segments, and only when even the newest line of a segment (its persisted host-time stamp in `meta.json`, not the file mtime) is past retention. The original had two problems:
  - A quiet device may not fill a 16 MiB segment for days, and the segment being written is never deleted, so a 1-day retention was meaningless;
  - Retention was checked only when switching segments.
- What happens now:
  1. A segment being written is replaced by a new one when, counted from when it started being written (host time), its span exceeds MaxAge/4; after a restart such an old segment is not continued either, and the start time of a segment is persisted (`seg_times`), clamped to now and re-anchored if absurdly old, so a clock set-back neither freezes nor mass-deletes segments. Device time plays no part (#50). So logs are kept for at most about 1.25 × MaxAge;
  2. The recorder (now the service process) runs `Store.Prune()` once a minute.
- Auxiliary file cleanup uses the same retention as the logs (see 9.2).
- The `stored …` line of `logcat_status` shows the effective retention and the per-device cap.
- The default window of `report` is still 2 hours, within 1 day. When `--since` goes beyond the retained history, the start of the history is stated in the manifest and the output as before.
- For upgrade notes see the Breaking entry in CHANGELOG 0.3.0.

## 10. Storage layout and device identity (issues #3, #5, #7, #9, #10)

Goal (GOALS "Bounded", "Observe, never interfere"): one data root that owns every file the tool creates, one recorder per user, and no unbounded leftovers.

### 10.1 Layout

```
<control> = os.UserCacheDir()/android-log-mcp       fixed per user; also the default data root
  run/                      0700 (Windows: protected DACL, user + SYSTEM)
    service.lock            held by the one service process (§11)
    service.json            {pid, socket, version, proto, root, exe, embedded, started} (0600)
    service.sock            AF_UNIX socket (0600); long paths fall back to /tmp/alm-<uid>/<hash>.sock
    spawn.lock              serializes MCP / CLI processes starting the service process
    config.lock             serializes set-root
  config.json               {"root", "cleanup": [old roots with leftovers]} (set-root; absent = default)
<root> (config.json root, else <control>)
  v1/<device-id>/           store format 1 (store.DataDir); other vN/ dirs are swept like unrecorded data
  tmp/                      scratch, expires after 1 h
  bin/<version>-<sha12>/    the service binary (copied by the MCP or CLI process that starts it)
  log/service.log(.1)       service log, 1 MiB + one rotation; crash.log(.1) (debug.SetCrashOutput)
```

Two ways to run:
- **Normal (default):** MCP processes and CLI processes (`call`, …) record nothing and forward to the service process (§11), which is the only process that opens the data root. `ANDROID_LOG_MCP_HOME` and `--home` were removed in #9; if either is still set, the MCP or CLI process exits 2 with an error that names `set-root`, so an MCP host configuration is never silently ignored.
- **`--offline <dir>`:** the only read-only mode, fully in-process. It opens `<dir>/v1/*` with no adb, never talks to the service and never records, prunes, migrates or creates a file (`Manager.NoADB`; `attach` and `MaintainOnce` return immediately). Used for unzipped reports and replay.

Everything else under the root (pre-0.4 flat `<serial>/` dirs, `legacy/`) is ignored: not listed, not read, not counted, not deleted. A legacy `owners/` dir (0.4 dev builds) is removed by the sweep (known names only).

### 10.2 Device ID

`device.ResolveID(addr, props, avdName)` is a pure function (unit-tested): emulator → `avd-<name>` (`ro.boot.qemu.avd_name`, else the console's `adb emu avd name`); physical → `ro.serialno` unless empty/placeholder; fallback `fp-<sha256(manufacturer|model|android_id)[:12]>`, or with no android_id `fp-<sha256(manufacturer|model|fingerprint|addr)[:12]>`. The probe is one read-only `adb shell` (getprop ×8, `settings get secure android_id`, boot_id). The adb serial is the connection address only: `meta.json` keeps it as `addr`. The manager is keyed by ID; `Manager.Get` accepts the ID or the adb serial (`Device.Matches`).

Known limitation: an API ≤ 29 emulator attached via `adb connect` has no AVD name and gets an `fp-` ID.

**Collisions (issue #6).** Two devices online at the same time may resolve to one ID (the same AVD launched twice with `-read-only`; duplicated serials). They are told apart by boot id within the service: same ID, different adb serial, both online, different boot id ⇒ a different device, filed as `<id>~<sanitized adb serial>` with a `note:` in `logcat_status`. Same boot id (USB + Wi-Fi adb) is one device; a new boot on the same serial, or on a new serial after the old one went offline, is the same device. The suffixed ID is only stable while that instance keeps its address (history is per AVD, not per emulator process).

### 10.3 Attach

On a new online adb serial the service's manager probes the identity asynchronously (retried every 10 s on failure), then `attach`: check for an ID collision; open `<root>/v1/<id>` as writer and record. If the store's `writer.lock` is held by another android-log-mcp process (an older version, or a second service process pointed at the same directory by hand), the device is shown as **NOT recorded** with that pid and is opened read-only; the maintenance tick retries. Since the service is the only writer per user, the per-device owner locks, follow mode and takeover logic of #5–#9 were removed (#3).

### 10.4 Sweep (`layout.Sweep`, in the service at start and every minute)

1. `run/`: remove state files whose pid is dead or whose socket does not accept a connection, orphan sockets, other files older than MaxAge; never `*.lock` files (held locks would be lost) and never `record.json` or `wanted.json` (`KeepRun`; a stale `wanted.json.tmp` goes by age).
2. Legacy `owners/`: lock files nobody holds, then the dir.
3. `tmp/`: files older than 1 h.
4. `bin/`: keep the running binary's dir (`Manager.KeepBin`) and the newest other one (by binary mtime; reuse touches it); delete older ones (known names only).
5. Age: every device directory under `v<N>/` not recorded by this service process and not busy (`writer.lock` held) loses segments older than MaxAge, including the newest; the directory is removed when nothing in it is newer. Recorded directories are pruned by their store (`Store.Prune`).
6. Total cap over all managed files (device dirs of every `v<N>/`, `tmp/`, `run/`, `bin/`, `log/`): delete the oldest files first; each current-format device keeps its newest segment.
Only known file names are deleted. The sweep never runs with `--offline`.

### 10.5 Locks

`store.TryLock` is the only lock primitive, held while the file stays open: `flock(LOCK_EX|LOCK_NB)` on unix; `LockFileEx(EXCLUSIVE|FAIL_IMMEDIATELY)` on one byte at offset 2^62 on Windows (so the content stays readable); an `O_EXCL` file elsewhere. The OS releases it on close or process death. Used for `run/service.lock`, `run/spawn.lock`, `run/config.lock` and `v1/<id>/writer.lock`. Tested on all three CI OSes (`store/lock_test.go`).

### 10.6 `set-root` (issue #3; history in #9, #10)

#9's live switch coordinated many independent writers through a config file and owner-lock acknowledgements; #10 reverted it because of four races (failed moves never retried; the original old root lost on a retried switch; `--delete-old` racing with readers on Windows; concurrent set-roots not exclusive). With one service as the only writer it is a request to that writer (`service/setroot.go`):

1. `set-root DIR (--keep-old|--delete-old) [--timeout 60s] [--json]` sends `set-root` to the running service. With no service, the CLI takes `run/service.lock` itself (so none starts meanwhile) and runs the same steps without recording; it never starts a service.
2. The service write-locks its op mutex (waits for in-flight tool calls, bounded by `--timeout`), takes `run/config.lock` (bounded; "another set-root is in progress"), and validates DIR (`layout.ValidateTarget`: absolute, resolved, not the current root, not inside or a parent of it, not inside the control dir, new/empty or an earlier root, writable).
3. Stop recording (`Manager.Close`: streams end, stores flushed and closed).
4. `device.CarryOver`: for every device dir of the old root, `store.ContinueIn` copies `meta.json` (sessions, gaps, the `logcat -T` resume point) and the aux files into the new root and sets `seq_floor` (seqs continue, cursors stay monotonic), `holes` (seqs recorded elsewhere when moving back into a root used before) and `resume_dedup` (`{boot_id, seq_floor, count}`; the hashes of the newest stored lines of the boot, chosen by seq, are in `resume_dedup.bin`, 8 bytes each, at most 500,000). A resume, `-T` or whole-buffer, merges them into its de-dup set while the boot is the same and fewer than `count` lines have been stored since the move (#56).
5. Commit: `config.json` written atomically (tmp + rename).
6. Start a new manager on the new root; the service log and crash log move along; `service.json` is rewritten.
7. Old root: `--keep-old` leaves it; `--delete-old` removes only `v<N>/`, `tmp/`, `bin/`, `log/` (and `run/` outside the control dir), then the old root if empty. Leftovers (e.g. open files on Windows, where the running service's binary in the old root's `bin/` always remains) are reported (exit 3), noted in `config.json` `cleanup` and retried when the service starts.

Any failure before step 5 rolls back: carried state removed or restored from a backup, a target created by this switch removed, recording restarted in the old root, `config.json` untouched. Tests: `cmd/android-log-mcp/setroot_test.go` (no-service and service paths, keep/delete, validation, rollback injected at carry and commit, config-lock timeout, concurrent set-roots, deleting the default root keeps `run/` and `config.json`), `store/continue_test.go`, `device/carry_test.go`, `layout/setroot_test.go`; live in `scripts/service-e2e.sh` with markers around both switches.

History is not moved: when the root is switched back, lines logged while the other root was active that are still in the device buffer are read again into it, once each and without duplicates inside the root (step 4 carries only the newest stored hashes, and a stored clock jump makes the service re-read the whole buffer; #115).

## 11. Service process (issue #3)

One long-lived service process per user records the devices in use (issue #11) and answers every tool call; MCP processes only forward between stdio and the service process. GOALS "local only" holds: AF_UNIX only, no TCP fallback, no token.

### 11.1 Processes and IPC

- `internal/ipc`: newline-delimited JSON frames (≤ 4 MiB) `{id, op, body}` → `{id, body, error}`, request ids, `cancel` op, ≤ 64 connections. Socket in `run/` (0700, socket 0600, `SetUnlinkOnClose`); peer check `SO_PEERCRED` (Linux) / `LOCAL_PEERCRED` (macOS); on Windows a protected DACL `D:P(A;OICI;FA;;;<user SID>)(A;OICI;FA;;;SY)` on `run/`. Without AF_UNIX (Windows < 10 1803) `ipc.ErrUnsupported` carries the minimum-version message. Named pipes are a possible future option.
- Ops: `hello {proto, version, pid, role}` → `{proto, min_proto, version, pid, root, embedded, tools, instructions, recording}`; `call`, `status`, `stop`, `handoff`, `report`, `set-root`, `record` (proto 2). Everything but `hello`/`status` needs a hello first. Tool calls take the op mutex shared; `set-root` takes it exclusively.
- The MCP process answers MCP `initialize` / `tools/list` from the tool definitions and instructions in the service process's hello, so the MCP process and the service process never disagree on schemas (fallback: its own, if the service cannot be reached; every call then reports the problem and retries).

### 11.2 Start, fallback, recovery

- `service.Client.ensure`: dial; on failure, under `run/spawn.lock`: copy the binary to `<root>/bin/<version>-<sha12>/` (`InstallBinary`, atomic rename; reuse touches it), start `service --detached` (unix `Setsid`; Windows `CREATE_NO_WINDOW|CREATE_NEW_PROCESS_GROUP|CREATE_BREAKAWAY_FROM_JOB` with SW_HIDE, retried without breakaway on ACCESS_DENIED; not `DETACHED_PROCESS`, which leaves the service without a console so every console child (adb) opens a visible window — fixed in 0.4.1. All child processes go through `procattr.Hide`), wait ≤ 10 s for the socket.
- **In-process fallback:** if the spawn fails (or after 3 spawns within a minute), the MCP process runs `service.Start` itself (`Embedded` reason, logs tee'd to stderr and the service log) and serves the same socket; other MCP and CLI processes use it. When that MCP process exits, the others reconnect and start a new service process.
- **Recovery:** a broken connection (`ipc.ErrClosed`) makes the MCP or CLI process reconnect (starting a service process if needed) and retry the call, up to 3 attempts; tools are read-only, so a retry is safe.
- `service.lock` is the single-instance guarantee; a second `service` exits with "already running (pid, version)".

### 11.3 Versions

`Proto`/`MinProto` (currently 2/1; 2 added `record`) are separate from the semver. On hello the MCP or CLI process (`service.Client`) compares versions (`CompareVersions`: semver, pre-release < release, build metadata ignored): if the **service process is older**, the MCP or CLI process sends `handoff` (refused by the service process unless the caller is newer), waits ≤ 30 s for it to stop and starts its own. Otherwise it uses the service if `ProtoCompatible(client proto, service min, service proto)`; an older compatible MCP or CLI process gets a note in `logcat_status`; an incompatible one fails with `ErrIncompatible` (output text: "Upgrade this client…") and never replaces the newer service process. `report`/`set-root` never hand off.

### 11.4 Lifecycle (issue #11)

- **Idle:** `device.Manager` runs in lazy mode in the service: `Start` only resolves adb; no `start-server`, `track-devices` or `logcat` until a device is wanted. `Manager.Want(id)` starts tracking (one `trackSession`: start-server, track-devices) and marks the device wanted; `attach` records only wanted devices (others are only identified, so `logcat_status` can list them as "connected, not recorded"). When the last wanted device is released, tracking stops too (`stopTracking`), so an idle service has no adb child.
- **Triggers:** every log tool (`mcpserver/use.go`: `target` resolves `device=` or the single online device; with several and none named it starts none and lists them; `MAX_DEVICES` reached → `ErrMaxDevices` naming the variable), `report` (`startForReport`) and `record --for` / `record_for` (pins).
- **Catch-up** (`device/catchup.go`): before (re)starting logcat, `logcat -d -t 50 -b main,system,crash,events` (the stream's format) gives T_end, the buffer's last record by arrival: its header (device time at µs, pid, tid, level, tag) and how many of the last 50 lines' records share that header (one retry; if it fails the device counts as caught up with a note). An empty buffer is caught up at once. Otherwise the replay ends at the n-th streamed record with that header, not at "device time ≥ T_end", which a line logged before a clock jump can meet early (issue #52 D3); only after the stream was quiet for 1 s does a later line that agrees with the connect clock read end it (T_end record evicted). The stream is caught up when that line has passed the pipeline into the store (`Accepted`/`Appended` counters, so deduplicated lines also count). Calls wait `CatchUpWait` (10 s) at most (`WaitCaughtUp`); then they prepend "backfill in progress (1,423 of ~27,000 lines; earliest …)" and `logcat_status` returns no cursor and asks to be called again. The total (`Progress.EstTotal`) is only estimated when the whole buffer is read: the "Now" row of `logcat -S -b main,system,crash,events` (entries logd holds; one entry is one stored record, multi-line messages included), read once in the background when the stream starts, rounded to two significant digits and never below the lines stored; a `-T` resume shows "N lines so far".
- **Backfill speed** (`device/stream.go`, `sideWork`): the stream is `adb exec-out logcat …` (raw: no pty, no shell-protocol framing or CRLF translation; several times faster than `adb logcat` over the emulator's transport). API < 21 (from the probe's `ro.build.version.sdk`), or an exec-out stream that ends with an error before its first line (not "device not found/offline"), falls back to `adb logcat` for the process lifetime (the reader strips CR). Killing the adb client closes the exec service and the device-side logcat exits (checked on API 29). Until caught up (at most `sideWorkWait`, 1 min) the recorder runs no other adb command on the device: `psLoop` (clock + `ps`), `pm list packages`, the buffer span and DropBox start afterwards. Crash lines in the backfill only note their DropBox tag; after catch-up each noted tag is fetched once, merged with the new-boot history tags, skipped if the last fetch of the tag succeeded < 5 min ago; a tag stays pending until its fetch completed (a live crash during a fetch queues one more). Unknown pids seed `ps` only for lines at or after the probe's device time (history is left to `psLoop`). The identity probe also reads the recorder probe (model, release, SDK, zone, boot id, clock; one adb shell): the recorder reuses it when it starts within 30 s; the clock is read before and after the slow `settings get secure android_id`, and that time is excluded from the round trip the offset is centred on.
- **Package attribution** (`procs.Owner`, issue #41): a device process gets its package once per process lifetime, and every line copies it. With base = the name before `:`: (1) an application uid with one package → that package; (2) otherwise, among the candidates (the uid's packages when the uid is shared, else all installed packages), base itself if installed, else the longest candidate that base extends at a `.` (`com.google.android.gms.persistent` → `com.google.android.gms`; `com.foobar.sync` never → `com.foo`); (3) a shared uid that no candidate name matches (`android.process.acore`) → the uid's alphabetically first package, so the pick is deterministic (attributing such shared-uid system processes better: #44); (4) otherwise the old fallbacks (isolated or unknown app uid → base; a name without `.` → none). The `.` rule only runs towards installed packages, and `package=` stays an equality test plus the `pkg:` prefix, so `package=com.google` never matches `com.google.android.gms`. `SetPackages` (and `Restore` with a loaded list) re-attributes every device process of the boot and persists the changed entries, so entries created before `pm list packages -U` returned are corrected; lines already stored keep their package. A device process with an application uid (10000–19999) missing from the list re-reads the list as side work, at most once per 60 s. Stored crashes, process lists (`logcat_status`, `report`) and the zero-match hint map process names with the same rule (uid unknown).
- **Resume and de-duplication (issue #52):** meta keeps two device times: `arrival_time_ns`, the newest arrival's (the `-T` resume point), and `last_time_ns`, the maximum. A same-boot resume reads the whole buffer without `-T` when (1) device now is before the newest arrival (snapshot restore, clock set back), (2) the newest buffered line is dated before the newest arrival, or (3) a clock jump is stored among the last M lines of the boot session or was measured at this connect; M = the entries logd holds (`logcat -S`, row "Now") × 1.5, at most 500k. Otherwise it resumes with `-T <newest arrival>`. Replayed lines are compared with stored ones as a multiset: hash of device time, pid, tid, level, tag and message, with counts; a replayed line is dropped only while its hash has a count left, so a line logged twice in the same µs survives. The counted stored lines are chosen by seq: all stored lines of the boot session (newest 500k at most) for a whole-buffer read, since one buffer can keep its boot lines while another rotated many times, so the lines it spans are not bounded by M (issue #75); for `-T`, the newest lines back to the first one dated 10 s before the resume point. Only the replayed part is compared (issue #48): live lines are never dropped. `logcat_status` reports a whole-buffer resume and how many lines it skipped.
- **Repair reads after a clock set-back (issue #65):** logd up to Android 11 (API ≤ 30) keeps a reader's position as a device time. When it prunes a full buffer, it treats the reader with the earliest position as blocking if the entry to prune is dated at or after that position. If that reader lags a few seconds (`isBusy`: about 4 s), logd makes it skip the next entries of that buffer (`kickMe` → `triggerSkip_Locked`), or disconnects it above 2× the buffer size.
  - After a set-back, the oldest entries are dated after the live stream's position, so the stream can silently lose blocks of new lines from the buffer being pruned. On the emulator this was main only: 277 of 30,212 entries in 14 runs. Android 12+ logd compares sequence numbers and is not affected.
  - **When.** `reconcileLoop` runs only on API ≤ 30, and only while a clock jump back lies among the stored lines the device buffer may still hold and one of those lines is dated after device now − 3 s. Then it is in repair mode. The mode ends by itself once those lines have rotated out. Outside the mode it makes no adb call; it only checks the recorded clock jumps. Mode changes go to the service log, and `logcat_status` shows the mode and the recovered count.
  - **Read.** Every 20 s it runs `logcat -d -t N` (by count, from the start of logd's list, so not affected by time positions). N is the whole buffer (`logcat -S` entries) on the first read of a mode. After that, N = 2 × the lines stored since the previous read + 500, at least 2,000 and at most the buffer: a skip only hits entries logged after the stream's position, at most 256 per prune.
  - **Compare.** About 2 s after the read (so lines the stream still held are stored), the read is matched against all stored lines of the boot session, newest 500k at most (issue #75). One buffer (system) can keep its boot lines while another (main) rotated many times, so the stored lines a read can hold are not bounded by the read's size or order. The session index is built once per repair mode and then extended with the new seqs. A dumped copy counts as stored while the session has a copy left for it: recHash counts (identical lines count per copy), then header counts (time, pid, tid; a record split or merged differently, or printed with another tag). Records before the first stored one count only if the index holds the session's first line.
  - **Store.** Only holes are stored: records after the first stored one and before the last one. They go through the stream goroutine (`Pipeline.AcceptLate`), get a seq in the order they were recovered, and are left out of clock-jump detection.

- **Per-device idle stop (linger)** (`service/linger.go`, `Manager.CheckIdle`): a wanted device is released when `now − last call ≥ linger(device)` and it has no call in progress (a `wait_ms` call is busy while it waits) and no active pin. `linger(device) = max(10 min, 2 × that device's main-buffer span)`, at most 1 h, 10 min until measured (the global `linger:` line states the rule; each device shows its own value); `ANDROID_LOG_MCP_LINGER` overrides (`0` = after each call, `forever`). Clients connecting or disconnecting play no role. Release = stop logcat, wait for the reader, close the store and reopen it read-only (history stays queryable). The next use resumes with `-T` and dedup; buffer loss in between is a gap with the reason "recording was idle …".
- **Service lifetime** (`lifeLoop`): the service exits `ExitGrace` (30 s) after there are no MCP or CLI process connections (role `cli` status queries do not count, so polling `service status` cannot keep it alive), no active pin and no recorded device; tracking started by a call that recorded nothing stops after `TrackGrace` (30 s) (interpretation of decision 0 / "service idle": a device still within its linger keeps the service). Embedded services live as long as the MCP process that runs them.
- **Carry-over:** `set-root` snapshots the wanted devices with their last-call times (`Manager.Snapshot`/`Restore`), so started devices, timers and pins survive it. A handoff or crash does not carry call-started devices; pins are carried (§11.7).
- **Stop** (`service stop`, signal, handoff, lifetime): stop accepting, let in-flight requests finish and their replies be written (≤ 5 s), close connections, stop the manager (stores flushed), remove `service.json`, release the lock. A killed logcat is always reaped (`cmd.Wait`).
- **adb:** `adb start-server` runs in its own process group (`Setpgid`) before `track-devices`, so the adb server is never a child of the service and is never killed. adb children of the service get `Pdeathsig=SIGKILL` on Linux and otherwise end on a broken pipe when the service exits. (A Windows Job Object was considered and not used, to rule out any risk to the adb server.)
- **Stopping adb children (issue #24):** stopping a device or the service process cancels its background work and waits up to 5 s for it. A cancelled adb call kills its adb child together with the child's descendants. Work still running after 5 s is logged, and its adb processes are killed the same way. These kills walk the host process table (`/proc` on Linux, `sysctl kern.proc.all` on macOS, a Toolhelp snapshot on Windows; `device/proctree*.go`) and kill the descendants pid by pid. They do not kill a process group or a Windows job object. The reason is that an adb client we start can itself start the adb server (`adb fork-server server`) when none is running. On unix that server can stay in the client's process group, which is ours. On Windows it would be in the same job object. Killing the group or the job would kill the server, which other tools and the user depend on. The walk instead treats any process named `adb` below our adb child as the server and skips it with its subtree; an adb client never starts another adb client. On Windows a parent id is never updated after the parent exits and pids are reused, so the walk only accepts a child that started after its parent (creation-time check). On unix orphans are re-parented, so the parent ids are current. `exec.Cmd.WaitDelay` (2 s) keeps a child process of adb that holds the output pipes from hanging a cancelled call.

### 11.5 Report

`report` sends `ReportArgs` (output path made absolute by the CLI, `--note @file` read by the CLI). With no service the CLI starts one detached (`NoEmbedded`: a spawn failure is an error, never an in-process service). The service starts the requested devices (`--serial`, else the single online one), waits ≤ 10 s for their backfill (otherwise a "backfill incomplete" warning in `manifest.json`), flushes its stores and builds the zip with its own adb (`service.BuildReport`). `--no-adb` starts no device; `--offline DIR report` packs that dataset without adb or service.

### 11.6 Tests

Unit (issue #11): `device/lazy_test.go` with the scriptable fake adb `internal/fakeadb` (a copy of the test binary named `adb`; devices, buffers, `date`, per-line delay and an invocation log in a directory): idle runs no adb, only the wanted device starts, deterministic catch-up with a slow dump, empty buffer, resume after idle with `-T`, clock rollback without duplicates, per-device idle, pinned device stays, `MAX_DEVICES`, snapshot/restore; `mcpserver/lazy_test.go`: first call backfills, several devices start none, no cursor during backfill, `record_for`, descriptions; `service/pins_test.go`: pin deadlines, `record.json` (atomic, clamp, unknown version), crash-loop, crash budget and its resets, crash counting from a stale `service.json`, the `record` op, set-root carry-over. Multi-process (`cmd/android-log-mcp/lazy_test.go`): no adb before the first call, backfill on the first search, 2 s linger with a connected client, `record --for` without a service, `record --stop`, a client restarting a killed pinned service, a handoff keeping pins, `report` starting the service.

Earlier unit tests: `ipc` (framing, cancel, limits, socket-path fallback, permissions/ACL, peer check), `service` (single instance, ops, handoff only to newer, versions, linger, log rotation), `layout` (sweep of `bin/`, `run/` locks, legacy `owners/`). Multi-process (`cmd/android-log-mcp`, the test binary re-executes itself as the CLI, and a copy of itself named `adb` is a fake adb): clients share and recover from a killed service; in-process fallback and takeover; version handoff both ways and the incompatible-protocol error; service lifetime with a 1 s grace; the adb server survives `service stop` and `kill -9` of the service; report via the service and without one; set-root (above). All on Linux, macOS and Windows in CI.

### 11.7 Recording pins (`record`, `record_for`; issue #11)

- `android-log-mcp record [--device X]... --for D` / `--stop` / state, and `logcat_status(record_for=D)` (`"0"` stops). `ParsePin`: units required, `0`, ≤ 24 h (`MaxPin`), longer is an error. Deadline = `max(current, now + D)`.
- Persisted in `<control>/run/record.json` (`{"v": 1, "pins": {id: {deadline, set_by, set_at, version, restarts, recent_crashes, suspended, last_error}}}`), written atomically under `run/record.lock`; `layout.SweepRun` keeps it (`KeepRun`). On read: past pins removed, deadlines > 24 h ahead clamped with a warning; an unknown `v` or unparsable file is ignored, never written, and `record --for` refuses.
- The CLI starts the service detached for `--for` (never in-process) and returns without waiting for the backfill; `--stop` and the state never start one (offline they edit/read `record.json`). The service starts pinned devices on start (`startPinned`), keeps them past their linger, and stays alive while a pin is active.
- **Crash accounting** (`service/record.go: loadPins`): a starting service that finds a `service.json` of a dead pid (read before the `run/` sweep) counts a crash for every active pin, with the last error from `crash.log` (`panic:` / `fatal error:` newer than the old start) or "service pid N (version V) ended without shutting down". 3 crashes within 1 min → `suspended: crash-loop`; more than 10 automatic restarts per (version, pin) → `suspended: crash-budget`. A new `record --for` clears both; a takeover by a different version resets the budget, the same version keeps it. Suspended pins start nothing and do not keep the service alive.
- **Restart:** a connected MCP process watches its connection; when it breaks, `service.json` is stale and an unsuspended pin is active, it starts a new service at once (`ClientOptions.RestartPinned`). Without connected MCP processes there is no supervisor (README "Known limitations").

---

## 中文摘要

- 本文是 AI agent 视角的 v3 设计：4 个只读工具（`logcat_status`、`logcat_search`、`logcat_crashes`、`logcat_app_launches`）、严格的过滤语法并回显解释、紧凑文本输出、不透明 cursor、设备时间优先。
- §2–§7 是设计时的草案，输出示例是示意；实际输出格式（`fmt=1`）见 [reference.md](reference.md#output-format-fmt1)，参数见 [tools.md](tools.md)。
- §7 的开放问题第 5 条（默认 limit 和折叠）已解决：默认 limit 为 50，连续相同行默认折叠。
- §8–§9 记录 `report` 子命令和内存/磁盘上限；§10–§11 记录存储布局和后台服务进程。
