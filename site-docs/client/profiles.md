# Profiles

Use one profile per Claude config directory when you run Claude Code with more than one account on the same machine.

## How profiles map to accounts

Claude Code keeps each login in its own config directory: `~/.claude` by default, or another directory selected with `CLAUDE_CONFIG_DIR`. cctrace follows the same split.

| Profile | Stored in | Claude home |
|---|---|---|
| Default | `~/.cctrace/profile.json` | `~/.claude` |
| Named, for example `work` | `~/.cctrace/profiles/work/profile.json` | The directory the profile names, for example `~/.claude-work` |

Each profile carries its own server endpoints, cctrace user identity, upload token, and options, and writes its OTEL variables and hooks into the settings file of its own Claude home. The sync daemon, its lock, its log, and its sync state are also per profile. See [Sync daemon](sync.md).

Which cctrace user a profile reports as depends on how it was created:

- `cctrace init --profile <name>` authenticates on its own, so the profile can belong to a different cctrace user than the default one.
- `cctrace profile add` and the directories `init` detects copy the default profile's user, server, and options. They report as the same cctrace user.

Profile names must start with a letter or digit and may contain letters, digits, `-` and `_` (up to 63 characters). `default` is reserved.

## Automatic detection during init

At the end of `cctrace init` for the default profile, `init` looks for directories in your home directory whose name starts with `.claude-` and that contain a projects directory or a settings file (settings.json). `.claude-mem` is ignored, as is any directory another profile already uses.

```text
  Detected 2 additional Claude home(s):
    1. /Users/alice/.claude-work
    2. /Users/alice/.claude-personal

  Set up sync for which? (all/none/1,2,...) [all]:
```

For each directory you pick, `init` creates a named profile whose name is the directory name without the leading dot (`claude-work`), copies the default profile into it, and writes that directory's settings file.

## Add a profile

```console
$ cctrace profile add claude-4 --home ~/.claude-4
```

`profile add` copies the default profile, points it at the directory given by `--home`, and writes the OTEL variables and hooks into that directory's settings file. `--home` is required. The default profile must exist first.

It refuses a name that already exists and a Claude home that another named profile already uses. If the directory does not exist yet, it warns and creates the profile anyway.

To set up a profile with its own login instead, run init with a name:

```console
$ cctrace init --profile work
```

This runs the full setup and adds one prompt, `Claude config directory (Enter for default ~/.claude)`. `init` warns if another profile already uses the directory you enter.

## List profiles

```bash
cctrace profile list
```

Prints the default profile (once it is set up) and each named profile with its Claude home. `cctrace status` shows both too.

## Remove a profile

```bash
cctrace profile remove work
```

Deletes the directory under `~/.cctrace/profiles/` for that profile after asking for confirmation (`--force` skips it). It does not touch the profile's Claude settings file, so the OTEL variables and hooks stay there. To remove those as well, use `cctrace reset --profile work` instead. See [Reset and uninstall](uninstall.md).

If the profile held a source that other profiles share, no profile collects that source afterwards until one claims it; see [Source folders that profiles share](#source-folders-that-profiles-share).

## Select a profile on the command line

Commands that act on one profile take `--profile <name>`: `status`, `config` (including `config policy`), `env apply`, `sync`, `kill`, `reset`, `local enable`, `local status`, and `local disable`. `sync`, `kill`, and `config policy` also read the `CCTRACE_PROFILE` environment variable when the flag is not given.

The hooks written into a Claude home pass `--claude-dir` with that home and `--auto-profile`. `--auto-profile` picks the named profile whose Claude home matches the `--claude-dir` value (or, without that flag, `CLAUDE_CONFIG_DIR`). When no named profile matches, the default profile is used.

## Source folders that profiles share

Two profiles can end up reading the same source folder. The usual case is the default Codex home: every profile with Codex collection turned on scans it when it exists. The daemon treats a folder as shared when the profiles upload to the same destination, meaning the same sync endpoint, cctrace user, and email, and the folders their collectors read are the same, or one lies inside the other. Uploading such a folder from both profiles would count every session in it twice, so the daemon lets one profile collect it.

- **One profile collects it.** That profile is the holder. The first profile the daemon gives a source to keeps it, whatever the other profiles do afterwards. On a first start the default profile goes first, then the named profiles in name order. A profile that is stopped or turned off is not given new sources, but keeps the ones it holds.
- **The other profile loses only that source.** Everything else it collects keeps uploading: its Claude sessions and every folder it does not share. The profile is not paused as a whole, unless every one of its sources is shared. Then it collects nothing, and `cctrace status` says so.
- **The holder's filters apply.** A shared source is sent as the holder's filters allow (repository allowlist, prompt and tool redaction, excluded accounts). The held-back profile sends nothing from it, so its own filters never widen what is sent.
- **The held-back period is not caught up on.** A profile that starts with no saved progress at all skips the sessions already on disk and collects new ones. A profile that already has saved progress, such as one that read the source before, goes on from it, so it can send files of the held-back period too, some of which another profile has already sent; the server drops the duplicates. When a source changes hands, the new holder continues from the old holder's progress where the daemon can read it; otherwise it skips the sessions already on disk and collects new ones.
- **The holder changes only when you change it,** with one exception. A holder that is stopped or turned off still holds its sources, and a profile you remove leaves its sources unread by every profile, so that a removal never hands them to a profile with other filters. If the holder takes a source out of its own settings, for example by turning that agent's collection off, it lets the source go, and the next profile whose filters are the same or stricter takes it over by itself. If none qualifies, nobody collects the source until you run `cctrace profile claim`. If a change on disk, such as replacing a folder with a link to another folder, makes two sources that different profiles hold into one, the hold made first stays and the other profile is held back from it, once it has stopped reading that source.

When the shared folders are the same folder, nothing is lost: the holder reads every file in it. When one lies inside the other, the daemon still keeps just one profile on it, but some files may be collected by no profile at all, because a collector reads only certain depths of a folder. `cctrace status` marks such a row with a warning.

### See what is held back

`cctrace status` prints a `수집 경로` line for a profile only when it is held back from a source. The output is in Korean:

```text
    수집 경로:     [!] 이 프로필에서 수집하지 않는 경로 2개 -- 같은 서버·사용자로 수집하는 다른 프로필이 맡고 있습니다
                   codex  기본 폴더        3fa91c2d  맡은 프로필: default (수집 중)
                   codex  [명시 Codex 홈]  9b01d7e4  맡은 프로필: personal (중지됨)
                   나머지 경로는 정상 수집합니다. 맡길 프로필을 바꾸려면: cctrace profile claim <프로필> --source <id>
```

Each row is the kind of source, where its folder came from, its id, and the holder with its state. The folder is never printed as a path.

| Column | Values |
|---|---|
| Folder | `홈 폴더` (Claude home), `기본 폴더` (default Codex home), `CODEX_HOME`, `설정 폴더` (a folder in the profile's settings), `[명시 Codex 홈]` (the profile's explicit Codex home) |
| Holder state | `수집 중` (collecting), `중지됨` (stopped), `꺼짐` (turned off), `막힘` (blocked), `이전 중` (handing the source over) |
| Holder missing | `(삭제됨)` (removed profile), `(주인 없음)` (nobody holds it), `(알 수 없음)` (unknown) |

A row ends with `-- 일부 파일은 어느 프로필에도 수집되지 않을 수 있음` (some files may be collected by no profile) when the folders are nested. When every source of the profile is shared, the first line reads `[!] 이 프로필의 모든 경로가 다른 프로필과 겹쳐 수집이 멈춰 있습니다 (duplicate_route)`. When the profile began a folder only after it was let in, a last line reads `겹침으로 막혀 있던 기간의 세션은 수집되지 않았습니다 (수집을 시작한 시점부터 수집)` (the sessions of the held-back period were not collected; collection starts from when it began). `cctrace sync status` and the daemon log show the same facts for all profiles at once; see [Sync daemon](sync.md#sources-shared-between-profiles).

### Give a shared source to another profile

```console
$ cctrace profile claim work --source 3fa91c2d
$ cctrace profile claim work --all --force
```

`profile claim <profile>` moves sources the profile is held back from to the profile. The holder lets go first, then `<profile>` starts reading from the holder's progress. The local daemon must be running (`cctrace sync --daemon`); the command says so and stops when it is not.

| Flag | Meaning |
|---|---|
| `--source <id\|folder>` | A source to claim: the id from `cctrace status`, or its folder in any spelling of the same folder. Repeatable. |
| `--all` | Every source the profile is held back from |
| `--force` | Skip the confirmation |

With neither `--source` nor `--all`, the command lists what the profile is held back from and stops. For each source it prints a plan and asks `Proceed? [y/N]`. Without a terminal, it refuses unless you pass `--force`.

```text
Claim codex source 3fa91c2d (default folder) for profile "work"
  current holder : default (active)
  cursor         : continue from "default"
  filters        : repository allowlist: default 2 entries, "work" none (collects every repository)
                   prompt redaction: default off, "work" off
                   tool redaction: default off, "work" off
                   excluded accounts: default 0, "work" 0
                   note: "work" would send more than "default" does
  effect         : "default" stops reading this source; "work" starts after "default" has released it
Proceed? [y/N]
```

Read the `filters` lines before you confirm. They compare how many entries each profile's filters hold and whether each is on, never their values, because a repository prefix is a path. The `note` line appears when the profile taking the source would send more than the holder does. Where the holder's progress cannot be read, `cursor` reads `holder state unavailable; existing sessions will be skipped, new ones collected`. For a source nobody holds because its holder took it out of its settings, the progress is that of the profile that read it last, and `cursor` reads `continue from` that profile.

After the daemon accepts a claim, the command prints `Claim 3fa91c2d accepted: "default" lets go of it first, then "work" starts reading it.` The move finishes in the background. If the daemon refuses, for example because a hand-over of that source is still in progress, nothing changes and the message says what to do. A source whose holder is removed, or that nobody holds, is claimed at once. While the routes file cannot be used (the daemon logs `[routes] invalid`), the first claim has the daemon rebuild it, and `--all` or several `--source` flags still claim every source you name: before each further one, the command waits, up to ten seconds, for the daemon to publish the rebuilt file.

## Server policy deferral

A server operator can publish a signed policy that moves clients to new server addresses. Only a client built with an update public key or a policy public key applies one (see [Sync daemon](sync.md#server-policy)). Nothing below applies to a client built from this repository without an update or policy public key.

A policy rewrites files that two profiles can share: the Claude settings file in a profile's Claude home and the Codex configuration file in the default Codex home, in the directory `CODEX_HOME` names when it is set, and in each directory of `options.codex_dirs`. When another profile uses one of the same files, the client defers the policy instead of applying it, unless the file is a Codex configuration file and either every profile that uses it moves to the same server or it is the one in the default Codex home (see below). The run ends with an error that ends in `policy deferred`, the profile keeps its current addresses and keeps collecting, and every later check ends the same way until the file is no longer shared. `cctrace config policy status` prints the reason.

The client checks every other profile, the default profile first and then the named profiles by name, and does not stop at the first conflict. The reason lists each conflict as a profile name and a kind of file, ordered by name, and Claude before Codex for the same profile:

```text
shared settings with profiles "home" (claude), "work" (codex); policy deferred
```

| Entry | Cause |
|---|---|
| `"<name>" (claude)` | The other profile has the same Claude home. `<name>` is empty when the other profile is the default profile. |
| `"<name>" (codex)` | Both profiles scan a Codex directory in common other than the default Codex home, one named by `CODEX_HOME` or listed in `options.codex_dirs`, and Codex sync has not been turned off for either. Listed only when that profile does not move with this one. Earlier client versions list a profile that shares only the default Codex home too, which every profile scans when that folder exists. |

The reason holds at most 500 characters. When too many profiles conflict to name them all, the last entries are counted instead: `... and 31 more; policy deferred`. The number counts entries, not profiles, so a profile that conflicts on both counts twice. Earlier client versions stop at the first conflict and report it alone, as `shared Claude settings with profile "<name>"; policy deferred` or `shared Codex settings with profile "<name>"; policy deferred`.

A profile that shares a Codex configuration file moves with this one, and is not listed, when it already uses the policy's new addresses, or when the policy names it with the addresses it uses now and its sync daemon is running in a state the policy can restart: started without `--local` or `--endpoint`, and reading its own Claude home. A different spelling of the same server, another port, or another protocol is not an address the policy names. A shared Claude home always defers the policy, even when the other profile moves too. Earlier client versions defer on every shared Codex configuration file.

When profiles move together, the client writes the shared Codex configuration file after the policy is committed rather than with it, and keeps the write in its policy state until it is done: the file's path, digests of the old and new `[otel]` sections, the old OTEL address, and the old certificate path. The record holds no token. Each later run carries the write out before it asks the server for a policy. If a file outside the default Codex home holds an `[otel]` section that none of the moving profiles writes, the run is deferred with `shared Codex settings edited; policy deferred` until the file holds one of theirs again. If the write fails, the run ends with `shared settings write pending` and the next run tries again. A session of the other profile can put its old section back before that profile applies the policy; once it applies, the file uses the new server.

The Codex configuration file in the default Codex home is the exception. Every profile with Codex sync turned on writes its own `[otel]` section there whenever its sync runs, and the profile that wrote last wins, whatever a policy does, so no policy can hold that file still for the others. A profile that shares only that file with others does not defer the policy, whether the others move or not. The profile moves, and the file is handled by what it holds:

| The file holds | The client |
|---|---|
| This profile's old section | Writes the new section after the policy is committed, as above. |
| This profile's new section, or a section of a profile that moves with it | Changes nothing. |
| A section of a profile that does not move, a section edited by hand, or anything else | Leaves the file as it is and still moves the profile. |

The token in the section never changes: a policy moves the server and keeps the credentials. When the file is left as it is, the policy still counts as applied and the run ends with the check result `ok`. The reason, in `cctrace config policy status` and in the report to the server, reads `default Codex config kept: <path>`. Until this profile's sync next starts and writes its own section, which carries the new address, that file keeps sending Codex metrics wherever its current section points, under that section's token. When several profiles use the default Codex home, Codex metrics go to the server of whichever of them wrote last, as before a policy; a policy does not change that. Earlier client versions defer on this file like any other.

A profile's explicit Codex home (`codex_home`) is written by that profile alone, so it is not a shared file and never defers a policy. With Codex sync turned on for the profile, the client moves the home's `[otel]` section in the same transaction as the rest of the policy, and only when the section is what cctrace generates for the profile: on the address the profile uses now, or on an address the policy moves from on the same host, naming the profile's certificate, the certificate the user supplied before any policy, or none. The one difference is the token: the section keeps the token it carries, which may be one that belongs to no profile on this machine. The rest of the file is kept as it is. A home that is also the default Codex home, the directory `CODEX_HOME` names, or a directory in `options.codex_dirs` follows the rule of that directory above.

| The explicit home's file holds | The client |
|---|---|
| This profile's section as described above | Writes the new section with the policy. |
| A section already on the policy's new address, a section with the token of another profile whose own policy run reaches this home (it scans the folder, or has it as its explicit home with Codex sync on), or no file | Changes nothing and says nothing. The other profile moves its own section. |
| No `[otel]` section, a section that sends elsewhere or was edited by hand, a section in an older form or a multi-line root `otel` key, a file that cannot be read, a Codex configuration file that is a symlink, a section with the token of a profile whose policy run does not reach this home, or a home whose lock the client cannot take | Leaves the file as it is and still moves the profile. |

When the file is left as it is, the policy counts as applied and the run ends with the check result `ok`. The reason, in `cctrace config policy status` and in the report to the server, reads `explicit Codex home kept`, joined with `; ` to the default Codex home's note when both apply. A path of an explicit Codex home in any reason a policy run records or reports is replaced by `[explicit Codex home]`. A lock that another process holds is waited for like any other, and ends the run when the wait runs out. If writing the file fails, the whole transaction is rolled back, the policy is not applied, and the next check tries again.

To separate the Claude settings, give one of the two profiles its own Claude home:

```console
$ cctrace profile edit work --home ~/.claude-work
$ cctrace env apply --profile work
```

`profile edit` changes only the profile's folders, and `env apply` writes the OTEL variables and hooks into the new home. Alternatively, remove the profile and add it again with `--home`. Removing a profile also deletes a login it had of its own. The new profile copies the default profile's user, server, and options, and its sync state starts empty, so it skips content already on disk.

To separate the Codex settings of a directory other than the default Codex home, turn Codex sync off for all but one of the profiles. Turning it off stops that profile's Codex collection:

```console
$ cctrace config set options.codex_sync_enabled false --profile work
```

Two more reasons also keep a policy deferred until the file's owner changes it: the certificate file the client wrote for an earlier policy no longer matches what it wrote, or `server.ca_cert_file` no longer points to it (`managed CA was edited; policy deferred` or `managed CA was changed; policy deferred`), and a Codex configuration file that no other profile uses has an `[otel]` section that differs from what cctrace generates for the profile (`unmanaged Codex OTEL settings differ from the profile; policy deferred`). With Codex sync turned off for the profile, the second no longer applies.
