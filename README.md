# slskify

Paste a Spotify playlist, album or track link. slskify finds each song on
Soulseek through your own [slskd](https://github.com/slskd/slskd) instance,
picks the right file, and downloads it. Matches it is confident about are
downloaded automatically. Uncertain ones go to a review queue, and the rest of
the run keeps going while they wait.

- Pure Go terminal app (Bubble Tea). No CGO, no Python, no spotdl, no ffprobe.
- **Works with a remote slskd.** By default slskify talks only to the Spotify
  Web API and the slskd REST API. It never touches slskd's download folder.
- **Optional library organizing** (slskd on the same machine): finished
  downloads are re-tagged from Spotify (title, artists, album, track, disc,
  date, genre, ISRC, cover art) and moved into your music folder, e.g.
  `~/Music/Nightly/THE VOID/haunted house.mp3`. Off by default. See
  [Organizing downloads](#organizing-downloads-optional).
- **Matching comes first.** "Is this the right recording?" is scored separately
  from "how good is the file?". A FLAC of the wrong song never beats a good MP3
  of the right one.

## Setup

### 1. Spotify app (client ID and secret)

1. Go to <https://developer.spotify.com/dashboard> and create an app. Any
   redirect URI will do (e.g. `http://127.0.0.1:8888/callback`); slskify does
   not use it. Select **Web API**.
2. Copy the **Client ID** and **Client secret**.

slskify uses the **Client Credentials** flow, so it can only read **public**
content. Spotify has restricted apps in development mode:
- The app owner may need a Premium account.
- Spotify-owned editorial and algorithmic playlists (IDs starting with
  `37i9dQZ`, e.g. "Today's Top Hits", Discover Weekly) are blocked.

slskify detects these cases and tells you what to do: make the playlist public,
or copy the tracks into a playlist of your own.

### 2. slskd API key

Add an API key to `slskd.yml`:

```yaml
web:
  authentication:
    api_keys:
      slskify:
        key: <a random string of 16-255 characters>
        role: readwrite
```

Restart slskd. The URL is usually `http://localhost:5030`.

### 3. First run

```sh
go install github.com/cmerk2021/slskify/cmd/slskify@latest
slskify
```

Prebuilt Linux binaries (amd64, arm64, armv7) are attached to each
[GitHub release](https://github.com/cmerk2021/slskify/releases). Releases are
built by GoReleaser when a `v*` tag is pushed. Run
`goreleaser release --snapshot --clean` to build locally.

If credentials are missing, the setup screen opens. It asks for the slskd URL
and API key, and the Spotify client ID and secret. It **tests both connections
live** and saves the config (mode `0600`) only when both tests pass. Re-open
the setup screen with `slskify setup` or **ctrl+s**. Secrets are masked and
never logged.

The setup screen also has an **Organize downloads** switch (focus it and press
`space`). Turning it on shows the slskd downloads directory, the music library
directory and the path template. See
[Organizing downloads](#organizing-downloads-optional).

## Usage

```
slskify [url]                  TUI. No URL: pick a past run to resume or paste a new link
slskify setup                  credentials screen only
slskify dry-run <url>          resolve, search, print ranked candidates; never downloads
slskify config-path            print config and database locations
```

Accepted links: Spotify track, album and playlist URLs or URIs
(`https://open.spotify.com/playlist/…`, `spotify:album:…`, and `intl-xx` URLs).
Artist links are rejected in v1.

| Flag | Meaning |
|---|---|
| `--config` | config file path |
| `--db` | state database path |
| `-q, --quality` | quality preset (default from config) |
| `-s, --speed` | speed preset (default from config) |
| `--slskd-url`, `--slskd-api-key` | slskd connection |
| `--spotify-client-id`, `--spotify-client-secret` | Spotify credentials |

`dry-run` adds:
- `-n/--top N`: candidates shown per track (default 5).
- `--rejected N`: rejected candidates shown (default 3).
- `--limit N`: only the first N tracks.
- `--track SUBSTR`: only tracks whose title or artist matches.

**Precedence:** CLI flags > environment variables > config file > defaults.
The environment variables are `SLSKD_URL`, `SLSKD_API_KEY`,
`SPOTIFY_CLIENT_ID` and `SPOTIFY_CLIENT_SECRET`.

**Locations:**
- Config: `os.UserConfigDir()/slskify/config.toml` (Linux:
  `~/.config/slskify/config.toml`).
- State DB: `$XDG_DATA_HOME/slskify/state.db` (Linux:
  `~/.local/share/slskify`; macOS: `~/Library/Application Support/slskify`;
  Windows: `%LocalAppData%\slskify`).

### Flow and keys

launch → setup (if needed) → run picker / paste URL → tracks load →
**pre-run screen** (choose quality and speed presets and, with organizing
on, the output mode and playlist file) → main view.

| Screen | Keys |
|---|---|
| everywhere | `ctrl+c` quit (state is saved; in-flight work is stopped cleanly) |
| setup | `tab`/`↑↓` field, `space` toggle organizing, `enter` next field / test & save on the last, `ctrl+t` test & save, `esc` cancel |
| run picker | type/paste URL, `enter` load (empty URL = resume highlighted run), `↑↓`, `ctrl+s` setup |
| pre-run | `tab` switch section, `↑↓` choose, `space` toggle the playlist file, `enter` start, `n` new / `e` edit / `c` copy / `d` delete (twice) quality profile, `esc` back |
| quality profile editor | `tab` next field, in the rank list: `a` add, `enter` edit, `d` delete, `shift+↑↓` or `[` `]` move, `1`–`9` quick-add a common rule; `ctrl+s` save, `esc` cancel |
| main | `←→`/`tab` tabs, `↑↓ pgup pgdn g G`, `enter` detail, `r` review, `s` skip, `p` pause/resume, `ctrl+s` setup, `q` quit |
| detail / review | `1`–`9` pick that rank, `enter` pick highlighted row, `s` skip, `e` edit query and re-search, `n`/`N` next/prev (review), `o` cycle sort, `x` show rejected, `p` pause, `esc` back |

The tabs are:
- **All**
- **Needs Review**: needs review plus not found, highlighted when nonzero.
- **Downloading**: ranked and downloading.
- **Done**
- **Failed**: failed and skipped.

`r` on the Needs Review tab starts review mode at the first unresolved track,
with an "N of M" indicator. Picking, skipping or re-searching moves on to the
next track automatically. New arrivals and finished re-searches are added to
the end of the queue, so the list you are looking at never reshuffles.

## How matching works

Per track: **query generation → hard filters → match confidence (0–100) →
confidence gate → quality ranking → decision.**

### Queries
Queries are run in order. Results are merged and deduplicated by
(user, filename), and the run stops early once an auto-eligible candidate is
found:
1. `primary-artist title`
2. `title` alone, but only if the title is distinctive (not very short or
   generic)
3. `primary-artist album title`, but not for singles or compilations
4. the same with feat. and version parentheticals stripped

Punctuation is removed because Soulseek search is token-based. You can edit
the query in review.

### Album grouping
Peers usually share whole albums in one folder (e.g.
`/music/Nightly/THE VOID/`). When two or more pending tracks of a run come
from the same album (an album link, or a playlist with several songs from one
album), they share **one** search:
1. `album-artist album` is searched once, with edition labels like
   "(Deluxe Edition)" dropped. For compilations and Various Artists releases
   the album name alone is used, and only if it is distinctive.
2. Every track of the album is matched against those results. Tracks with a
   confident match are queued for download straight away, with no search of
   their own.
3. The remaining tracks run the usual per-track queries, and the album
   results are merged into theirs.

Within a quality tier, files from a folder that supplies several of the
album's tracks rank ahead of other peers. This keeps an album together on as
few peers as possible. Quality still comes first: a folder of MP3s never beats
a confident FLAC under `lossless-first`. Re-searches with an edited query are
never grouped. `dry-run` groups tracks the same way.

### Hard filters
These run before scoring. Rejected files are kept, with the reason, and shown
for context when nothing is found.
- locked files
- non-audio files, or extensions the quality policy doesn't allow
- bitrate below the policy minimum
- size outside `min_size_mb`/`max_size_mb`
- known duration differing from Spotify's by more than `duration_hard_reject_s`

### Confidence signals
The filename and the last parent folders are parsed. Every signal adds a line
to the breakdown.

| Signal | Points | Notes |
|---|---|---|
| title | up to 45 | Normalized: case, diacritics, punctuation, `&`↔`and`, track-number prefixes. Scored on token coverage plus fuzzy (Jaro-Winkler) similarity. **Title gate:** if the title isn't found, confidence is capped at 40. |
| artist | up to 30 | Any credited artist. Full credit in the filename, 90% in a folder, partial for fuzzy matches. Handles "The" and feat. variations. |
| duration | up to 25 | Full within ±2 s, sliding down to ±5 s, then negative up to the hard limit. Unknown duration: 0 points and the flag `duration_unknown`. |
| album | +5 | Parent folder matches the album. Bonus only. |
| track # | +3 | Prefix agrees. A disagreement is never a penalty. |
| version tokens | −5 … −50 | The candidate has a version token the original lacks, or vice versa (remix, live, acoustic, instrumental, karaoke, a cappella, demo, radio edit, extended, sped up, slowed, reverb, 8d, cover, tribute, mashup, re-recorded, clean …). Remaster, explicit and stereo/mono are mild. A token only in a folder name counts less. A token already in the Spotify title (e.g. "Song (Live)") is expected, not penalized. |
| negative | −40 | sample, preview, snippet; files far too short |
| multi-track | −50 | full album or mix, `.cue`, or a duration estimated from size and bitrate far longer than expected |

Confidence is clamped to 0–100. Each candidate carries its breakdown, its
flags, and its parsed quality.

### Quality policies
Quality only ranks candidates that pass the confidence gate. The sort order is
explicit and tested:

1. **Confidence spread guard.** Only candidates within `confidence_spread`
   (12) points of the best auto-eligible confidence compete on quality.
2. **Quality tier** (the policy's ordered tiers). Within lossless, the
   preferred bit depth and sample rate come first (16-bit/44.1 kHz by
   default).
3. **Album folder:** for grouped album tracks, folders that supply more of the
   album come first (see [Album grouping](#album-grouping)).
4. **Confidence**, compared in `confidence_bucket` (5-point) buckets.
5. **Peer quality:** free upload slot, then upload speed, then shorter queue.

| Preset | Order |
|---|---|
| `lossless-first` (default) | FLAC/ALAC/WAV/AIFF > MP3 320 CBR > MP3 V0/VBR high > MP3 256 > lossy ≥ 192 |
| `lossless-only` | lossless only |
| `mp3-best` | MP3 only: 320 CBR > V0 > 256 > ≥ 192 |
| `any-best` | lossless > lossy by bitrate (≥ 128) |

VBR V0 (≈245 kbps average) ranks below 320 CBR.

#### Custom quality profiles
Make your own ranking from the pre-run screen: `n` creates a profile, `e`
edits the highlighted one, `c` copies it, and `d` (pressed twice) deletes it
(for a built-in, `d` restores the original). The editor shows a live preview
of how sample files would rank. Profiles are saved to the config file:

```toml
# Prefer MP3 320; take FLAC if there is no 320; then any other MP3.
[quality_presets.mp3-first]
rank = ["mp3 320", "flac", "mp3"]
min_bitrate = 192          # optional lossy floor (kbps); lossless is never affected
description = "..."        # optional; defaults to "mp3 320 > flac > mp3"
```

Each `rank` entry is one tier, best first:

| Part | Values |
|---|---|
| format | `lossless`, `lossy`, `any`, or a format: `flac`, `alac`, `wav`, `aiff`, `ape`, `wv`, `mp3`, `aac`, `ogg`, `opus`, `wma`, `mpc`, `m4a` (AAC or ALAC). Join several with `/`, e.g. `flac/alac`. |
| bitrate | minimum kbps, lossy only: `320`, `256+`, `>=256` |
| mode | `cbr` or `vbr`; `v0` means VBR ≥ 220, `v2` means VBR ≥ 170 |

Only formats named in `rank` are downloaded (set `allowed_extensions` to
override). Within a tier, higher bitrate wins, and lossless files follow
`prefer_bit_depth`/`prefer_sample_rate`. Use `slskify -q mp3-first …` or
`quality_preset = "mp3-first"` to make a profile the default.

### Decision
- **Auto-download** the top-ranked candidate when all of these hold:
  - confidence ≥ `high_threshold` (80);
  - duration is known and within `duration_tolerance_s`. Unknown durations
    never auto-download while `require_duration_for_auto = true`;
  - the **ambiguity check** passes. It fails when another confident candidate
    is within `ambiguity_margin` (8) points of the top one and differs
    meaningfully: different version tokens, durations more than
    `ambiguity_duration_s` (3 s) apart, or different albums.
- **Needs review** when the best confidence is between `low_threshold` (50)
  and high, when the best candidate lacks a duration, or when the ambiguity
  check fails.
- **Not found** when the best confidence is below low, or nothing survives the
  hard filters. These tracks are shown in the Needs Review tab with the best
  rejected files for context.

### Tuning
`slskify dry-run <url>` prints every track's decision and its ranked
candidates, with quality, peer info and the full score breakdown. When you
find a case the matcher gets wrong, add it to
[`internal/match/testdata/corpus.toml`](internal/match/testdata/corpus.toml).
Each case is a track, a remote path, and the expected verdict
(match/review/reject). Then run `go test ./internal/match`.

## Pipeline

Each track is an independent state machine:

```
pending → searching → ranked → downloading → done
            ↘ needs_review / not_found      ↘ failed / skipped
```

- Searches and downloads run concurrently under the speed preset's limits.
  Review never blocks the rest of the run.
- slskd's concurrent-search limit is respected: searches wait in a local queue
  instead of failing.
- **Fallback:** if a transfer is rejected, errors, or stays queued longer than
  `queue_timeout`, it is cancelled and the next-ranked candidate is tried.
  After `max_candidate_attempts` failures, the track goes to **needs review**
  with each failure marked. A file you pick by hand in review gets the same
  handling.
- **Queue skip:** if a transfer is still queued after `queue_skip_timeout`
  (default 1m) and at least `queue_skip_min_alternatives` other confident
  (auto-eligible) candidates remain untried, it is cancelled early and the
  next one is tried. These skips don't count toward `max_candidate_attempts`.
  Files picked by hand in review are never skipped early.
- **Pause** (`p`) stops dispatching new work; in-flight work finishes.
- **Resume:** everything lives in one SQLite database. Starting without
  arguments lists past runs. Tracks that were mid-search are re-queued.
  Downloading tracks are reconciled against slskd's current transfer list, so
  nothing is enqueued twice. A transfer slskd no longer lists goes to review.

- **Organizing** (when enabled): after a transfer succeeds, the track shows
  "organizing …" while the file is tagged and moved, then **done** with
  "saved to <path>". If the file can't be organized, the track is still done
  (the download itself worked), the reason is shown, and the file stays where
  slskd put it. Transfers that finished while slskify was closed are
  organized when the run is resumed.

## Organizing downloads (optional)

Soulseek files often have inconsistent tags. When slskd runs **on the same
machine** as slskify (or its download folder is mounted here), slskify can
clean up each finished download:

1. **Find the file** in slskd's downloads directory. slskd normally saves it
   as `<downloads>/<remote folder>/<file name>`. If you changed slskd's
   subfolder pattern, or slskd renamed the file to avoid a clash, slskify
   scans the directory for a file with the same name, extension and exact
   size. It waits up to 30 s for slskd to move the file out of its incomplete
   folder.
2. **Rewrite the tags** from Spotify: title, artist(s), album artist, album,
   track number/total, disc, release date, genre (the first genre Spotify
   lists for the primary artist, often none), ISRC, compilation flag and the
   album cover (embedded as the front cover). Old tags are removed, except
   ReplayGain/R128 loudness tags. Tags are written with
   [TagLib](https://taglib.org) compiled to WebAssembly
   ([go-taglib](https://github.com/sentriz/go-taglib)), so there is still no
   CGO. MP3, FLAC, Ogg Vorbis/Opus, M4A/ALAC, WAV, AIFF, WMA, APE and
   WavPack are supported.
3. **Move it** to `<music dir>/<path template>.<ext>`. The default template
   `{album_artist}/{album}/{title}` gives
   `~/Music/Nightly/THE VOID/haunted house.mp3`. Folder and file names are
   made safe (`/ \ : * ? " < > |` become `_`). An existing file is never
   overwritten: the new one gets ` (2)`, ` (3)`… Emptied slskd folders are
   removed. Moving across disks works (copy, then delete).

Placeholders: `{album_artist}`, `{artist}` (first artist), `{artists}` (all,
comma separated), `{album}`, `{title}`, `{track}` (two digits), `{disc}`,
`{year}`. Text next to an empty placeholder is trimmed, so
`{track} - {title}` becomes just the title when there is no track number.

Enable it in the setup screen (`ctrl+s`) or in the config file. The
directories are checked before a run starts. slskify reports a clear error
if slskd's downloads directory doesn't exist on this machine. If slskd runs in
Docker, use the **host** path of the mounted downloads volume, not the
container path. (The setup screen pre-fills the path slskd reports, which is
the container path in that case.)

With organizing disabled (the default, and the right choice for a remote
slskd), nothing changes: slskify only uses the two web APIs.

### Output mode: library or playlist folder

With organizing on, the pre-run screen has an **Output** choice per run:

- **library** (default): files go where your path template says.
- **playlist**: ignores the path template. Tags are still normalized from
  Spotify (step 2), but every file goes flat into one folder,
  `<playlist folders dir>/<playlist name>/<artist> - <title>.<ext>`, with no
  subfolders. The playlist folders dir is set in setup
  (`organize.playlist_dir`) and defaults to the music directory.

### Playlist files (.m3u8)

The pre-run screen also has a separate **Playlist file** switch. It doesn't
depend on the output mode. When it's on, slskify writes
`<music dir>/<playlist name>.m3u8`, an extended M3U playlist in UTF-8. Entries
follow the Spotify order, and paths are relative to the music directory with
`/` separators, such as `Nightly/THE VOID/haunted house.mp3` or
`My Mix/Nightly - haunted house.mp3`. Because the paths are relative, you can
move the library and the playlist still works. The file is rewritten each time
a track is organized and when a run is resumed. Only tracks that were organized
are listed. Files that were moved or deleted since then are left out. Set
`organize.playlist_file = true` to turn the switch on by default for new runs.
Runs with the same name share a playlist file.

Both options need organizing to be enabled. You can change them when you
resume a run. Files that were already organized stay where they are.

| Speed preset | Searches | Downloads | Delay | Search timeout |
|---|---|---|---|---|
| `slow` | 1 | 2 | 3 s | 20 s |
| `medium` (default) | 2 | 3 | 1 s | 15 s |
| `fast` | 6 | 8 | 0 | 15 s (the UI warns that the network may throttle you) |

## Config reference

The defaults are shown below. Missing keys fall back to them.

```toml
quality_preset = "lossless-first"
speed_preset = "medium"

[slskd]
url = "http://localhost:5030"
api_key = ""
max_concurrent_searches = 6     # slskd's own limit; extra searches queue locally

[spotify]
client_id = ""
client_secret = ""

[download]
queue_timeout = "5m"            # cancel and fall back if still queued remotely
queue_skip_timeout = "1m"       # skip earlier if enough confident alternatives remain ("0s" disables)
queue_skip_min_alternatives = 2 # untried auto-eligible candidates needed to skip early
stall_timeout = "3m"            # in progress with no bytes moving
max_candidate_attempts = 3
poll_interval = "2s"

[organize]                      # needs slskd on this machine; see "Organizing downloads"
enabled = false
downloads_dir = ""              # slskd's downloads directory, as a path on this machine
music_dir = ""                  # library root, e.g. "~/Music"
path_template = "{album_artist}/{album}/{title}"   # extension is added
playlist_dir = ""               # playlist output mode: <dir>/<playlist name>/ (empty = music_dir)
playlist_file = false           # preselect writing <music_dir>/<playlist name>.m3u8 for new runs

[match]
high_threshold = 80
low_threshold = 50
duration_full_s = 2.0
duration_tolerance_s = 5.0
duration_hard_reject_s = 10.0
require_duration_for_auto = true
ambiguity_margin = 8
ambiguity_duration_s = 3.0
confidence_spread = 12
confidence_bucket = 5
min_size_mb = 1.0
max_size_mb = 300.0
# Replaces the built-in version-token list when set:
# [[match.version_tokens]]
# name = "nightcore"
# patterns = ["nightcore"]
# penalty = 50
# dir_weight = 1          # weight when the token only appears in a folder (0 → 0.5)

[quality_presets.lossless-first]
description = "FLAC/ALAC/WAV/AIFF, then MP3 320 > V0 > 256 > ≥192"
allowed_extensions = ["flac", "alac", "wav", "aiff", "aif", "ape", "wv", "mp3"]
min_bitrate = 192
prefer_bit_depth = 16
prefer_sample_rate = 44100
  [[quality_presets.lossless-first.tiers]]
  name = "lossless"
  lossless = true
  [[quality_presets.lossless-first.tiers]]
  name = "mp3 320 cbr"
  formats = ["mp3"]
  min_bitrate = 320
  mode = "cbr"
  [[quality_presets.lossless-first.tiers]]
  name = "mp3 v0/vbr high"
  formats = ["mp3"]
  min_bitrate = 220
  mode = "vbr"
  [[quality_presets.lossless-first.tiers]]
  name = "mp3 256"
  formats = ["mp3"]
  min_bitrate = 256
  [[quality_presets.lossless-first.tiers]]
  name = "lossy ≥ floor"
  lossless = false
# lossless-only, mp3-best and any-best follow the same shape.

[speed_presets.medium]
description = "2 searches, 3 downloads, 1s between searches"
concurrent_searches = 2
concurrent_downloads = 3
search_delay = "1s"
search_timeout = "15s"
response_limit = 100
file_limit = 10000
warning = ""
```

A tier matches a file when every condition it sets holds: `lossless`,
`formats`, `min_bitrate`, and `mode` (`cbr`/`vbr`). The first matching tier
wins. Files that pass the hard filters but match no tier rank last ("other").
A preset sets either `tiers` or the shorter `rank` (see
[Custom quality profiles](#custom-quality-profiles)), not both.

## Known limitations

- **No verification of downloaded files.** The only signal after download is
  the transfer state reported by slskd. Matching before download is the only
  safeguard. (Organizing re-tags files but doesn't inspect the audio.)
- **Organizing needs local access** to slskd's downloads directory. With a
  remote slskd, leave it off. Settings changed in setup apply to the next
  started or resumed run.
- **Fake or upscaled lossless can't be detected** from search metadata. A
  transcoded MP3 saved as FLAC looks exactly like a real FLAC.
- **Public sources only.** Client Credentials can't read private playlists.
  Spotify-owned editorial playlists are blocked for new apps.
- Duration, bitrate and sample rate are only as good as what peers share. Many
  peers report none, and those files can't be auto-downloaded by default.

## Later

- PKCE login (Authorization Code) for private playlists. Auth already sits
  behind a `TokenSource` interface.
- Queue-position-aware waiting, instead of a fixed `queue_timeout`.
- Cross-run dedupe. Tracks are already stored with Spotify ID and ISRC.
- Split-pane layout on wide terminals.
- Downloading a whole album folder as one batch. Album tracks already share
  one search and prefer the same folder, but each file is still queued
  separately.

## Development

```sh
go build ./... && go vet ./... && go test ./...
```

Packages:
- Core, with no TUI dependency: `internal/spotify`, `internal/slskd`,
  `internal/match` (pure, heavily tested), `internal/pipeline`,
  `internal/store`, `internal/config`, `internal/organize` (optional
  post-download tagging and moving).
- `internal/fakeapi`: in-memory Spotify and slskd servers used by tests.
- `internal/tui`: the Bubble Tea front end.
