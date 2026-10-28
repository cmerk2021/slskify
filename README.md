# slskify

Paste a Spotify playlist, album or track link. slskify finds each song on
Soulseek through your own [slskd](https://github.com/slskd/slskd) instance,
picks the right file, and downloads it. Matches it is confident about are
downloaded automatically. Uncertain ones go to a review queue, and the rest of
the run keeps going while they wait.

- Pure Go terminal app (Bubble Tea). No CGO, no Python, no spotdl, no ffprobe.
- **Remote only.** slskify talks to the Spotify Web API and the slskd REST API,
  nothing else. It never reads slskd's download folder, and it does no tagging
  or moving of files.
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

If credentials are missing, the setup screen opens. It asks for the slskd URL
and API key, and the Spotify client ID and secret. It **tests both connections
live** and saves the config (mode `0600`) only when both tests pass. Re-open
the setup screen with `slskify setup` or **ctrl+s**. Secrets are masked and
never logged.

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
**pre-run screen** (choose quality and speed presets) → main view.

| Screen | Keys |
|---|---|
| everywhere | `ctrl+c` quit (state is saved; in-flight work is stopped cleanly) |
| run picker | type/paste URL, `enter` load (empty URL = resume highlighted run), `↑↓`, `ctrl+s` setup |
| pre-run | `tab` switch list, `↑↓` choose, `enter` start, `n` new / `e` edit / `c` copy / `d` delete (twice) quality profile, `esc` back |
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
3. **Confidence**, compared in `confidence_bucket` (5-point) buckets.
4. **Peer quality:** free upload slot, then upload speed, then shorter queue.

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
  safeguard.
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
- Grabbing a whole album folder from one peer.

## Development

```sh
go build ./... && go vet ./... && go test ./...
```

Packages:
- Core, with no TUI dependency: `internal/spotify`, `internal/slskd`,
  `internal/match` (pure, heavily tested), `internal/pipeline`,
  `internal/store`, `internal/config`.
- `internal/fakeapi`: in-memory Spotify and slskd servers used by tests.
- `internal/tui`: the Bubble Tea front end.
