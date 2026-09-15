# Notes

Working notes for this repo: status, decisions, and the traps that have actually bitten.
Written in the first person and dated by when each thing was learned — the date is
usually the useful part.

Cross-cutting notes that are not specific to this repo live in
[fleet-notes](https://github.com/stoatworks-labs/fleet-notes).

*bd-play-stream-gateway (bdgw) — MediaMTX as a protocol hub on the BirdDog PLAY, with a
Streaming tab; PUBLIC repo; built 2026-09-15, NOT YET RUN ON A PLAY*

**Why a hub and not a stack (decided 2026-09-15).** PPApp already receives SRT with its
own TS demuxer, fdk-AAC and Opus decoders and MPP for H.264/HEVC (strings in the 1.0.34
`bin/PPApp`). So every new protocol only has to become SRT on loopback. MediaMTX v1.21.0
does all of RTMP/RTSP/HLS/WebRTC/SRT/UDP-TS/RTP in, pulls from rtsp/rtmp/http/srt/udp/whep
sources, serves every reader kind, and — since a recent release — **natively forwards** to
rtmp(s)/rtsp(s)/srt/whip destinations (YouTube/Twitch documented; YouTube rejects
video-only, and bdcam is video-only until its audio roadmap item lands). Hand-rolling the
same in Go was rejected as a mini-MediaMTX with less coverage; GStreamer straight to
kmssink was rejected because it takes the display (bdplay measured cycling BirdDogRunner
destabilising PPApp) and is kept only as the fallback if PPApp refuses MediaMTX's SRT.

- **Selecting an SRT source is three files + `POST :8080/restart`**, recovered from
  `bin/BirdDogSrvr/BirdDog.js` and `videoset.html`: `/etc/birddog_srt_src.json`
  (`{"streams":[{name,uri}]}`), `/etc/birddog-source1-name` (`SRT:name(uri)`), and
  `SourceSelection:"SRT"` in `/etc/birddog-dec1-settings.json`. The stock caller URL shape
  is `ip:port?mode=caller&latency=N&streamid=S`. Our list entries are `bdgw-<path>` —
  hyphen, not colon, because the stock page builds `$('#' + name)` selectors.
- **MediaMTX facts pinned into the design:** SRT read `streamid=read:<path>`, publish
  `streamid=publish:<path>&pkt_size=1316`; the control API on 127.0.0.1:9997
  (`/v3/paths/list`, `online`/`tracks2`/`readers`); the yml hot-reloads; `--validate-conf=path`
  is a string flag; linux_arm64 tarball 29,438,184 B → 62.2 MB unpacked; GitHub's
  release host sends **no CORS header**, so the public patcher needs a Worker proxy.
- **The tab anchors exactly where bdcam does** (`id="tab1"` Decode Settings button and the
  `pl-3 pr-3 pb-3 pt-0` content div — the id alone appears twice in 1.0.32, the full tag
  once). Verified unique on real 1.0.32 and 1.0.34 pages kept locally under
  `testdata/firmware/` (gitignored); coexistence with bdcam's markers is tested both ways.
- **Reads are gated, not just writes** (unlike bdcam, like bdts): the config carries
  stream keys. `Masked` hides `#key`, `user:pass@` and `passphrase=`; `Merge` treats a
  masked value coming back as "unchanged". The open `/api/status` never carries a URL.
- **The renderer emits MediaMTX's own keys**, written against the v1.21.0 `mediamtx.yml`
  and `api/openapi.yaml`. CI fetches the pinned linux_amd64 release, checks the published
  SHA-256, and runs `--validate-conf` over every fixture the tests render — that is the
  only proof the yml loads. Bump `render.go`, `ci.yml`, fwbuild and the patcher together.

**Hardware list (nothing done):** PPApp ↔ gosrt loopback interop (the load-bearing one;
fallback is PPApp listener + MediaMTX `forward:`), idle-path behaviour (`hold_picture` =
`alwaysAvailable` is in the tab, unverified), audio through the loopback, hot reload on an
atomic rename, CPU with WebRTC/HLS readers, the cookie reaching :8093.

Related: [bdcam](https://github.com/stoatworks-labs/bdcam/blob/main/docs/NOTES.md),
[bdts](https://github.com/stoatworks-labs/bdts/blob/main/docs/NOTES.md),
[birddog play patcher](https://github.com/stoatworks-labs/birddog-play-patcher/blob/main/docs/NOTES.md).
