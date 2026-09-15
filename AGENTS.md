# AGENTS.md — bd-play-stream-gateway

The Streaming gateway for the **BirdDog PLAY** (Rockchip RK3328, quad A53, Debian 10
aarch64): MediaMTX as the protocol hub, and `bdgw`, this Go program, as the panel around
it. Cross-compiled for the device, no cgo. **Public repo.**

Start with [`README.md`](README.md) — it carries the design, the security argument and
the hardware list that is still outstanding. This file is the operating rules.

## Where this sits

`bd-play-stream-gateway` is the canonical, standalone home of `bdgw`, alongside `bdcam`,
`bdts` and `bd-play-usb-player` as sibling checkouts under `~/Projects/birddog-play`. The
reverse engineering that justifies every claim in the README lives in `birddog-re` — a
**private** research repo whose history carries a recovered vendor AES key and a decrypted
firmware blob. Never copy key material or vendor firmware into this repo.

`birddog-re`'s `tools/fwbuild` finds this repo's binary at
`../bd-play-stream-gateway/dist/bdgw-linux-arm64` (override with `BDGW=`) and fetches the
pinned MediaMTX release itself. The public patcher syncs `dist/` and fetches MediaMTX in
the browser through its Worker proxy. The binary name, the unit prefix and the install
directory are all `bd-gw`; the hub's unit is `bd-mtx`.

## Hard rules

1. **Never replace `PPApp` or take the display.** The picture reaches HDMI by pointing the
   stock decoder at an SRT source on loopback (`decoder.go`). Anything that stops
   `BirdDogRunner` or opens `card0` is a different project.
2. **The template edit must never contain a template action.** `videoset.html` is parsed
   by `birddog-web-ui` at startup and a parse failure takes the web UI down — with the
   firmware upload page, which is the way back. The inserted block is a tab button, an
   empty div and a script tag; `TestPatchAddsNoTemplateActions` and
   `TestPatchedTemplateStillParses` keep it that way. UI changes go in
   `web/streaming.js`, which is outside the template system.
3. **Anchor on what bdcam anchors on.** Both tabs insert directly after the same two
   anchors, so they coexist in either order; `TestPatchCoexistsWithBdcamInEitherOrder`
   reproduces bdcam's markers and breaks if either side moves.
4. **Keep the auth gate differential, and gate reads too.** bdts's reasoning applies
   twice over here: the config holds stream keys. Only `/api/status` is open, and it must
   never carry a URL. `Masked` and `Merge` are what let the tab edit without ever holding
   a key; `TestPublicNeverCarriesAKey` and `TestMergeKeepsSecretsForMaskedValues` are the
   check.
5. **CORS echoes the origin, never `*`.** The tab sends the birdUI cookie cross-origin,
   and a wildcard origin may not carry credentials.
6. **Only emit MediaMTX keys that CI has validated.** The renderer writes MediaMTX's own
   key names and they change between releases. Any new key goes into a fixture in
   `render_test.go`, so CI's `--validate-conf` run against the pinned binary proves it.
   Bump `MediaMTXVersion` in `render.go` and the workflow together.
7. **Restart the decoder only when the selection changes.** Every `BirdDogRunner` restart
   is one bd-play-usb-player measured as destabilising. `apply` in `serve.go` compares
   before and after; keep it that way.
8. **Build only through `./build.sh`**, or with `GOOS=linux` set. Linux-only code.
9. **Never commit BirdDog's firmware HTML.** `testdata/firmware/` is gitignored and holds
   real pages for local testing only; the fixture that ships is hand-written in
   `patch_test.go`.

## Toolchain

```bash
./build.sh            # -> dist/bdgw-linux-arm64 (CGO_ENABLED=0, static)
go test ./...         # everything runs on the host against fake trees and fake servers
GOOS=linux GOARCH=arm64 go vet ./...
```

## Layout

```
main.go        CLI: --serve, --render, --print, --patch-ui/--unpatch-ui/--restore-ui
config.go      the settings model, validation, masking and merge
render.go      config -> mediamtx.yml; the pinned MediaMTX version; the URL helpers
decoder.go     pointing PPApp at a path: the three files, /restart, remember and restore
mtx.go         MediaMTX's control API (paths/list) and its unit
auth.go        the birdUI session gate (bdts's, unchanged in reasoning)
serve.go       the API: /api/status (open), /api/config, /api/display, /api/log (gated)
patch.go       the videoset.html tab; markers, anchors, backup, atomic writes
web/           the tab itself, embedded and written to the web UI's static dir
docs/          NOTES.md (working notes), fwbuild-wiring.md (the installer blocks)
testdata/      gitignored: rendered fixtures (CI validates), real firmware pages (local)
```

## Not yet true

**Nothing here has run on a PLAY.** The README's hardware list is the outstanding work,
and the first item on it — `PPApp` reading MediaMTX's SRT over loopback — is the one the
whole inbound side rests on. Until it is done, every mention of this project says beta.

## Notes

`docs/NOTES.md` carries this repo's working notes — current status, decisions already
made, and the traps that have actually bitten. Read it before changing anything
non-obvious. Cross-cutting fleet knowledge lives in
[fleet-notes](https://github.com/stoatworks-labs/fleet-notes).
