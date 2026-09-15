# bd-play-stream-gateway — RTMP, RTSP, HLS, WebRTC and UDP on a BirdDog PLAY

> **AI-assisted project.** This codebase was created with [Claude Code](https://claude.com/claude-code)
> (Anthropic), directed and reviewed by a human author. **Nothing here has yet run on a
> PLAY** — every claim about the device below is read out of its firmware or measured by
> the sibling projects, and the list at the end is what still has to be proved on hardware.

`bdgw` turns a BirdDog PLAY into a streaming endpoint in both directions:

- **In:** push RTMP from OBS, vMix or a hardware encoder; pull RTSP from IP cameras and
  NVRs; pull an HLS playlist; publish WebRTC/WHIP from a browser; receive MPEG-TS or RTP
  over UDP — and put any of it on the HDMI output.
- **Out:** serve the USB camera that [bdcam](https://github.com/stoatworks-labs/bdcam)
  captures over RTSP, RTMP, HLS, WebRTC and SRT, and push it to YouTube, Twitch or any
  RTMP(S), RTSP, SRT or WHIP destination.

It does this **without writing a streaming stack**: the box runs
[MediaMTX](https://github.com/bluenviron/mediamtx) (MIT, one static binary) as a protocol
hub, and `bdgw` is the small program around it — it renders MediaMTX's configuration from a
settings file, adds a *Streaming* tab to the PLAY's own web UI, and points the PLAY's own
decoder at whichever path should be on screen.

Installed by the [BirdDog PLAY Patcher](https://birddog-play-patcher.stoatworks-labs.com)
as its *Streaming gateway* option, or by `birddog-re`'s `fwbuild --with-gateway`.

## How the picture reaches HDMI

The PLAY's decoder, `PPApp`, is closed source and holds the display. Replacing it would
cost the OSD, tally, the web UI's video integration and CloudConnect. So it is not replaced:

```
OBS ──rtmp──▶ ┌──────────┐  srt://127.0.0.1:8890?streamid=read:live  ┌────────┐
IP cam ─rtsp▶ │ MediaMTX │ ─────────────────────────────────────────▶ │ PPApp  │ ─▶ HDMI
browser ─whip▶│  (hub)   │                                            │ (stock)│
              └──────────┘                                            └────────┘
                   ▲  srt://127.0.0.1:8890?streamid=publish:cam
              bdcam (USB camera, H.264 from the VEPU)
                   │
                   └──▶ readers: rtsp:// rtmp:// http:// (HLS, WebRTC) srt://
                   └──▶ forward: rtmps://a.rtmp.youtube.com/live2#key, rtsp://, srt://, whip://
```

`PPApp` already receives SRT: it links libsrt, carries its own MPEG-TS demuxer, decodes
H.264 and HEVC in hardware and has fdk-AAC and Opus decoders. Selecting an SRT source is
what the stock AV Setup page does, and it is three files plus a restart — all recovered
from the firmware's own Express server and page:

| File | Written as |
|---|---|
| `/etc/birddog_srt_src.json` | `{"streams":[{"name":"bdgw-live","uri":"127.0.0.1:8890?mode=caller&latency=120&streamid=read:live"}]}` (our entry added, everyone else's kept) |
| `/etc/birddog-source1-name` | `SRT:bdgw-live(127.0.0.1:8890?mode=caller&latency=120&streamid=read:live)` |
| `/etc/birddog-dec1-settings.json` | `"SourceSelection":"SRT"`, other keys untouched |
| `POST :8080/restart` | the vendor's own way to make the decoder act on a change |

`bdgw` remembers what was selected before and puts it back when the display path is
cleared or the gateway is switched off. The runner is restarted only when the selection
actually changes — bd-play-usb-player measured that cycling it often destabilises `PPApp`.

The alternative — drawing to HDMI ourselves with `gst-launch rtspsrc ! mppvideodec !
kmssink` — is what bdcam and bdplay do, and both record what it costs: `PPApp` stopped, a
dark HDMI output on any failure, no OSD or tally. It is kept in reserve for the one case
that would force it (see *Not yet true*), not built.

## What is installed

```
/userdata/bd-gw/mediamtx        the hub, MediaMTX linux/arm64 (62 MB, from the release tarball)
/userdata/bd-gw/bdgw            this program (6 MB)
/userdata/bd-gw/config.json     the settings the tab edits; preserved across reinstalls
/userdata/bd-gw/mediamtx.yml    rendered from config.json on every save and every start
/userdata/bd-gw/state.json      what the decoder showed before the gateway took it
/userdata/bd-gw/run.sh          bd-mtx.service: render, then exec mediamtx
/userdata/bd-gw/api-run.sh      bd-gw.service: bdgw --serve :8093
```

Two units, deliberately: `bd-mtx` runs MediaMTX and `bd-gw` runs the panel, so a stopped
hub still leaves a page that can say so. MediaMTX hot-reloads `mediamtx.yml` when it
changes, so a save while streams are running drops nobody; the unit is only restarted when
the gateway is switched on or off.

The *Streaming* tab is added to `/srv/birddog-web-ui/videoset.html` — the AV Setup page,
beside bdcam's *UVC Converter* tab — using the same anchors bdcam uses, so the two tabs
coexist in whichever order they were installed. The template edit is two marker-wrapped
lines with no template action in them; everything else is in `web/streaming.js`, served
from `/static/`. The installer restarts `BirdDogWebUI` afterwards and rolls the patch back
if the web UI does not come back, exactly as for bdcam and bdts.

## Ports

| Port | What | Also |
|---|---|---|
| 1935/tcp | RTMP publish and read | |
| 8554/tcp | RTSP publish and read | 8000–8001/udp RTP/RTCP |
| 8888/tcp | HLS | |
| 8889/tcp | WebRTC (WHIP publish, WHEP read, browser page) | 8189/udp ICE |
| 8890/udp | SRT publish and read | |
| 8093/tcp | this panel's API | |
| 9997/tcp | MediaMTX's control API, **loopback only** | |

None collide with BirdDog's 80/8080, NDI's 5960+, or the fleet's 8090 (bd-cam-api), 8091
(bd-play) and 8092 (bdts). Media-over-QUIC is switched off: one more listener nobody on a
PLAY has asked for.

**There are no credentials on the streaming ports.** Anyone who can reach the box can
publish to it or read from it, exactly as anyone who can reach `:8080` can reconfigure it —
the LAN or the tailnet is the boundary, and the patcher's page already says so about the
stock API. Scope an ACL before putting a PLAY on a tailnet.

The **panel** is different: its config carries stream keys and it can repoint the HDMI
output, so `bdgw` gates every call except `/api/status` on a valid birdUI session — the
same gate bdts uses, with the same differential check (a device with no birdUI password
cannot be gated, and the panel refuses rather than pretending, unless started with
`--allow-unprotected`). Secrets never reach the page: URLs are masked on the way out
(`rtmps://a.rtmp.youtube.com/live2#••••mnop`) and a masked value sent back means "keep
what is stored".

## The tab

- **Gateway** on/off.
- **Show on HDMI** — pick a path, press SWITCH. Leaving it on *stock source* hands the
  decoder back to whatever AV Setup says.
- **Paths** — each is a name (`[a-z0-9_-]`, 1–32) with a source: *published to this PLAY*,
  or a URL the hub pulls (`rtsp(s)://`, `rtmp(s)://`, `http(s)://` HLS, `srt://`,
  `udp+mpegts://`, `udp+rtp://` + SDP, `whep(s)://`), optionally only while something is
  watching. Each can be pushed on to any number of `rtmp(s)://…#streamKey`, `rtsp(s)://`,
  `srt://…?streamid=…` or `whip(s)://…/whip` destinations.
- **Addresses** — the exact URLs to publish to and to watch the camera at, with the
  device's own hostname filled in, and the SRT URL to paste into bdcam's *UVC Converter*
  tab: `srt://127.0.0.1:8890?streamid=publish:cam&pkt_size=1316`.
- **Status** — per path: which kind of publisher is connected, the codecs it carries, how
  many readers of which kind; and what the decoder is showing.

## What a PLAY can and cannot show

The decode ceilings are the RK3328's: **H.264 to 1080p60 or 2160p30, HEVC to 2160p60.**
A 4K60 H.264 feed will not play, here or over stock SRT. Audio arrives as AAC (or Opus)
in the loopback TS; whether `PPApp` plays it is on the hardware list below.

WebRTC publishers must use **H.264**: MPEG-TS cannot carry VP8, VP9 or AV1, and the SoC
could not decode them anyway. MJPEG RTSP cameras cannot reach the display for the same
reason. Everything else the hub accepts is remuxed, never transcoded — the VEPU and the
MPP decoder keep doing the codec work, and the hub costs a few percent of one core.

YouTube refuses video-only streams. bdcam does not carry audio yet, so *push the camera to
YouTube* will not work until it does; Twitch, RTSP, SRT and WHIP destinations are not
affected.

## Build

```bash
./build.sh            # -> dist/bdgw-linux-arm64 (CGO_ENABLED=0, static, ~6 MB)
go test ./...         # config, renderer, decoder files, birdUI patch, API — all on the host
```

CI also fetches the pinned MediaMTX release (checksum-verified) and runs its
`--validate-conf` over every configuration the renderer produces, because the key names
are MediaMTX's and change between releases. The pin lives in `render.go`
(`MediaMTXVersion`) and `.github/workflows/ci.yml`; the patcher pins the same version.

To test the patch against the genuine page, copy a device's
`/srv/birddog-web-ui/videoset.html` to `testdata/firmware/videoset.<version>.html`
(gitignored — it is BirdDog's copyrighted UI source) and it is picked up automatically.

## Running it by hand

```bash
bdgw --print                              # the mediamtx.yml the current config renders
bdgw --render                             # write it (exit 3 when the gateway is switched off)
bdgw --serve :8093                        # the panel API
bdgw --patch-ui / --unpatch-ui / --restore-ui   # the Streaming tab
```

Every path is a flag (`--dir`, `--config`, `--mtx-yml`, `--state`, `--etc`, `--play-api`,
`--mtx-api`, `--mtx-unit`, `--ui-dir`, `--ui-base`) so the whole thing runs against a fake
tree on a laptop; the tests do exactly that.

## Not yet true — the hardware list

In the order they would change the design if the answer is bad:

1. **`PPApp` reads MediaMTX's SRT.** The inbound side rests on gosrt (MediaMTX) talking to
   `PPApp`'s libsrt, `streamid=read:…` being accepted, and its demuxer liking MediaMTX's
   TS muxer. bdcam's own SRT output was only ever validated by an independent demuxer,
   never by `PPApp`. *Test:* OBS → RTMP → PLAY, picture on HDMI. *If bad:* reverse the
   direction — `PPApp` in listener mode, MediaMTX `forward: srt://127.0.0.1:<port>`.
2. **An idle path.** Whether MediaMTX holds or refuses the decoder while nobody is
   publishing, and whether `PPApp` retries cleanly. *If bad:* the *hold a slate* option
   (`alwaysAvailable`) is already in the tab, unverified; or point the decoder only once
   the path is online.
3. **Audio through the loopback** — AAC from an RTMP source, Opus from a WHIP one.
4. **Hot reload on an atomic write.** `bdgw` replaces `mediamtx.yml` by rename; MediaMTX's
   watcher should see it. *If bad:* restart the unit on save (drops readers).
5. **CPU with several readers** — WebRTC and HLS packetising cost more than remuxing.
6. **The session cookie reaches port 8093** — the same assumption bdts makes, and for the
   same reason should hold (cookies are scoped by host, not port).

## Licence

MIT — see [LICENSE](LICENSE). Not affiliated with, endorsed by, or supported by BirdDog.

MediaMTX is © its authors, MIT, and is not part of this repository: the patcher and
`fwbuild` fetch the pinned release tarball at build time and place its binary in the
firmware package. That is a redistribution, so its notice travels with it.
