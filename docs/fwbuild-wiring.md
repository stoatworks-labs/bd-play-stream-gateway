# Wiring the gateway into `birddog-re/tools/fwbuild`

Two payload files, two units, one UI patch. Presented as blocks to insert rather than a
diff, for the reason bdts gives: `build.sh` and `payload/update` move often, and a context
diff against a moving file fails in ways that look like a broken patch.

The public patcher implements the same contract in the browser (`public/app.js`) and
syncs `payload/update` from here, so **edit here first**.

## 0. Layout on the device

```
/userdata/bd-gw/mediamtx     from the MediaMTX release tarball (pinned version)
/userdata/bd-gw/bdgw         ../bd-play-stream-gateway/dist/bdgw-linux-arm64
/userdata/bd-gw/run.sh       payload/gw-run.sh      (bd-mtx.service)
/userdata/bd-gw/api-run.sh   payload/gw-api-run.sh  (bd-gw.service)
/userdata/bd-gw/config.json  written by bdgw on first start; preserved across reinstalls
```

## 1. `tools/fwbuild/payload/gw-run.sh`

```bash
#!/bin/bash
# Entry point for the streaming gateway, started by bd-mtx.service.
#
# bdgw renders mediamtx.yml from config.json first, so what MediaMTX runs is
# always what the Streaming tab shows. Exit 3 from the render means the gateway
# is switched off in the config: exit 0 and let Restart=always poll for it
# being switched on, the same trick bd-cam uses for a missing camera.
cd "$(dirname "$0")" || exit 1

./bdgw --dir /userdata/bd-gw --render
rc=$?
if [ "$rc" = 3 ]; then
  exit 0
fi
[ "$rc" = 0 ] || exit "$rc"

exec ./mediamtx /userdata/bd-gw/mediamtx.yml
```

## 2. `tools/fwbuild/payload/gw-api-run.sh`

```bash
#!/bin/bash
# Entry point for the Streaming tab's API, started by bd-gw.service.
#
# A separate unit from the hub on purpose: a stopped or broken MediaMTX must
# still leave a page that can say so.
cd "$(dirname "$0")" || exit 1

exec ./bdgw --dir /userdata/bd-gw --serve :8093
```

## 3. `tools/fwbuild/build.sh`

**Near the other defaults:**

```bash
WITH_GATEWAY=0
WITH_GATEWAY_UI=0
# The MediaMTX release the gateway is written against. bd-play-stream-gateway
# validates its rendered configuration against exactly this version in CI, and
# the public patcher fetches exactly this version in the browser; bump all
# three together.
MEDIAMTX_VERSION=v1.21.0
MEDIAMTX_SHA256=a8113b5928ba1a934b81557b61b8a07954b76921a4b567d54c7f086f8b39d9a2
```

**In `usage()`:**

```
  --with-gateway      (beta) include the streaming gateway: MediaMTX + bdgw.
                      RTMP/RTSP/HLS/WebRTC/UDP in, shown on HDMI through the
                      PLAY's own decoder over SRT on loopback; the UVC camera
                      served and pushed out. (sibling ../bd-play-stream-gateway
                      must be built; the MediaMTX tarball is fetched)
  --with-gateway-ui   (beta) as --with-gateway, plus the "Streaming" tab in the
                      stock web UI, beside bdcam's. Same edit of videoset.html,
                      same health check and rollback.
```

**In the argument loop:**

```bash
    --with-gateway) WITH_GATEWAY=1; shift ;;
    --with-gateway-ui) WITH_GATEWAY=1; WITH_GATEWAY_UI=1; shift ;;
```

**In the `build.conf` heredoc:**

```bash
WITH_GATEWAY=$WITH_GATEWAY
WITH_GATEWAY_UI=$WITH_GATEWAY_UI
```

**A new staging block, after the bdplay one:**

```bash
if [ "$WITH_GATEWAY" = 1 ]; then
  # bdgw lives in its own public repo, like bdts. Expected as a sibling
  # checkout; override with BDGW=/path/to/bdgw-linux-arm64.
  BDGW="${BDGW:-}"
  if [ -z "$BDGW" ]; then
    for cand in "$REPO/../bd-play-stream-gateway/dist/bdgw-linux-arm64" \
                "$HOME/Projects/birddog-play/bd-play-stream-gateway/dist/bdgw-linux-arm64"; do
      [ -f "$cand" ] && { BDGW="$cand"; break; }
    done
  fi
  [ -n "$BDGW" ] && [ -f "$BDGW" ] || {
    echo "error: bdgw binary not found" >&2
    echo "  clone github.com/stoatworks-labs/bd-play-stream-gateway beside this repo and run its build.sh," >&2
    echo "  or set BDGW=/path/to/bdgw-linux-arm64" >&2
    exit 1; }
  file "$BDGW" | grep -q aarch64 || { echo "error: bdgw is not aarch64" >&2; exit 1; }

  # MediaMTX itself: the official static arm64 release, checked against the
  # SHA-256 published beside it. Redistributed in the package under MIT.
  MTX_TGZ="$REPO/work/mediamtx/mediamtx_${MEDIAMTX_VERSION}_linux_arm64.tar.gz"
  if [ ! -f "$MTX_TGZ" ]; then
    mkdir -p "$(dirname "$MTX_TGZ")"
    echo "fetching MediaMTX ${MEDIAMTX_VERSION}"
    curl -fsSL -o "$MTX_TGZ" \
      "https://github.com/bluenviron/mediamtx/releases/download/${MEDIAMTX_VERSION}/mediamtx_${MEDIAMTX_VERSION}_linux_arm64.tar.gz"
  fi
  GOT="$(shasum -a 256 "$MTX_TGZ" | cut -d' ' -f1)"
  [ "$GOT" = "$MEDIAMTX_SHA256" ] || {
    echo "error: $MTX_TGZ sha256 $GOT != expected $MEDIAMTX_SHA256 — delete it and retry, or update the pin" >&2
    exit 1; }
  mkdir -p "$STAGE/userdata/bd-gw"
  tar xzf "$MTX_TGZ" -C "$STAGE/userdata/bd-gw" mediamtx
  file "$STAGE/userdata/bd-gw/mediamtx" | grep -q aarch64 || { echo "error: mediamtx is not aarch64" >&2; exit 1; }
  cp "$BDGW" "$STAGE/userdata/bd-gw/bdgw"
  cp "$PAYLOAD/gw-run.sh" "$STAGE/userdata/bd-gw/run.sh"
  cp "$PAYLOAD/gw-api-run.sh" "$STAGE/userdata/bd-gw/api-run.sh"
  chmod 755 "$STAGE/userdata/bd-gw/mediamtx" "$STAGE/userdata/bd-gw/bdgw" \
            "$STAGE/userdata/bd-gw/run.sh" "$STAGE/userdata/bd-gw/api-run.sh"
  echo "including the streaming gateway (MediaMTX ${MEDIAMTX_VERSION} + bdgw)"
fi
```

**In the module name collision list**, add `gateway`, `gw` and `mtx`. The browser's
`readModule` has the same list; keep them equal.

## 4. `tools/fwbuild/payload/update`

**With the other defaults:**

```bash
WITH_GATEWAY="${WITH_GATEWAY:-0}"
WITH_GATEWAY_UI="${WITH_GATEWAY_UI:-0}"
```

**A new section, after the bdplay block:**

```bash
# ------------------------------------------------------- streaming gateway
# MediaMTX as a protocol hub (RTMP, RTSP, HLS, WebRTC, SRT, UDP in and out),
# with bdgw rendering its configuration and adding a Streaming tab. The picture
# reaches HDMI through PPApp's own SRT receiver on loopback — nothing takes the
# display, and the OSD, tally and web UI stay in charge. Same rules as every
# other payload: everything under /userdata, self-supervised, nothing on the
# recovery path touched.
if [ "$WITH_GATEWAY" = 1 ] && [ -d "$SDIR/userdata/bd-gw" ]; then
  NEED_KB=$(du -sk "$SDIR/userdata/bd-gw" | awk '{print $1}')
  if [ -n "$FREE_KB" ] && [ "$FREE_KB" -lt $((NEED_KB + 51200)) ]; then
    log "SKIPPING the streaming gateway: needs ${NEED_KB} KB + 50 MB headroom, only ${FREE_KB} KB free"
  else
    log "installing the streaming gateway (${NEED_KB} KB) to /userdata/bd-gw"
    mkdir -p /userdata/bd-gw
    cp -f "$SDIR/userdata/bd-gw/mediamtx"   /userdata/bd-gw/
    cp -f "$SDIR/userdata/bd-gw/bdgw"       /userdata/bd-gw/
    cp -f "$SDIR/userdata/bd-gw/run.sh"     /userdata/bd-gw/
    cp -f "$SDIR/userdata/bd-gw/api-run.sh" /userdata/bd-gw/
    chmod 755 /userdata/bd-gw/mediamtx /userdata/bd-gw/bdgw /userdata/bd-gw/run.sh /userdata/bd-gw/api-run.sh
    chown -R root:root /userdata/bd-gw
    # config.json is never shipped: bdgw writes the defaults on first start,
    # and an existing one survives a reinstall — it is how the box is set up.

    cat > /etc/systemd/system/bd-mtx.service <<'EOF'
[Unit]
Description=BirdDog PLAY streaming gateway — MediaMTX (custom)
After=network-online.target
Wants=network-online.target

[Service]
WorkingDirectory=/userdata/bd-gw
ExecStart=/userdata/bd-gw/run.sh
# run.sh exits 0 when the gateway is switched off in config.json; restarting
# on a timer is how switching it on is picked up without a udev rule or a
# second daemon.
Restart=always
RestartSec=10
# MediaMTX closes its listeners cleanly on SIGTERM.
KillSignal=SIGTERM
TimeoutStopSec=20

[Install]
WantedBy=multi-user.target
EOF

    # 8093 because 8080 is BirdDog's API, 8090 is bd-cam-api, 8091 is bd-play
    # and 8092 is bd-tailscale-ui. A collision is silent: the second service
    # to start fails to bind and its tab simply never answers.
    cat > /etc/systemd/system/bd-gw.service <<'EOF'
[Unit]
Description=BirdDog PLAY streaming gateway panel (custom)
After=network.target BirdDogWebUI.service bd-mtx.service

[Service]
WorkingDirectory=/userdata/bd-gw
ExecStart=/userdata/bd-gw/api-run.sh
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF
    log "gateway units written (bd-mtx, bd-gw)"
    GW_INSTALLED=1
  fi
fi

# ------------------------------------------------- web UI "Streaming" tab
# videoset.html again — bdcam's UVC tab and bdplay's USB entry patch the same
# file, each with its own markers, and bdgw anchors where bdcam does so the
# tabs sit side by side whichever went first. Same discipline: the patch is
# unit tested in bd-play-stream-gateway, refuses unfamiliar markup and is
# exactly reversible; the health check and rollback here are the second line.
if [ "${GW_INSTALLED:-0}" = 1 ] && [ "$WITH_GATEWAY_UI" = 1 ] && [ -x /userdata/bd-gw/bdgw ]; then
  WEBUI_UNIT=BirdDogWebUI
  if ! systemctl cat "$WEBUI_UNIT" >/dev/null 2>&1; then
    log "WARNING: no $WEBUI_UNIT unit on this firmware — skipping the Streaming tab"
  elif /userdata/bd-gw/bdgw --patch-ui --ui-dir /srv/birddog-web-ui --api-port 8093 >> /tmp/bd-custom-install.log 2>&1; then
    PID_BEFORE=$(pidof birddog-web-ui 2>/dev/null | awk '{print $1}')
    log "AV Setup page patched; restarting $WEBUI_UNIT (was pid ${PID_BEFORE:-none})"
    systemctl restart "$WEBUI_UNIT" 2>/dev/null
    sleep 5
    PID_AFTER=$(pidof birddog-web-ui 2>/dev/null | awk '{print $1}')
    CODE=$(curl -s -o /dev/null -m 8 -w '%{http_code}' http://127.0.0.1/login 2>/dev/null || echo 000)
    # It must answer AND be a different process: an unchanged pid means the
    # template was never re-read, so an HTTP 200 is answered by the process
    # that never reloaded.
    if [ -n "$PID_AFTER" ] && [ "$PID_AFTER" != "$PID_BEFORE" ] && { [ "$CODE" = 200 ] || [ "$CODE" = 302 ] || [ "$CODE" = 301 ]; }; then
      log "web UI healthy after patch (pid ${PID_BEFORE:-none} -> $PID_AFTER, HTTP $CODE) — Streaming tab is live"
    else
      log "WARNING: web UI unhealthy after patching (pid ${PID_BEFORE:-none} -> ${PID_AFTER:-none}, HTTP $CODE) — ROLLING BACK"
      /userdata/bd-gw/bdgw --unpatch-ui --ui-dir /srv/birddog-web-ui >> /tmp/bd-custom-install.log 2>&1
      systemctl restart "$WEBUI_UNIT" 2>/dev/null
      sleep 5
      log "after rollback: HTTP $(curl -s -o /dev/null -m 8 -w '%{http_code}' http://127.0.0.1/login 2>/dev/null || echo 000); page as found also at /srv/birddog-web-ui/videoset.html.bdgw-stock"
    fi
  else
    log "WARNING: could not patch the AV Setup page — left untouched"
  fi
fi
```

**In the service-enabling section:**

```bash
if [ -f /etc/systemd/system/bd-mtx.service ]; then
  systemctl enable bd-mtx.service 2>/dev/null
  systemctl restart bd-mtx.service 2>/dev/null && log "bd-mtx started (MediaMTX)" \
    || log "WARNING: bd-mtx failed to start — check 'journalctl -u bd-mtx'"
fi
if [ -f /etc/systemd/system/bd-gw.service ]; then
  systemctl enable bd-gw.service 2>/dev/null
  systemctl restart bd-gw.service 2>/dev/null && log "bd-gw started on :8093" \
    || log "WARNING: bd-gw failed to start — check 'journalctl -u bd-gw'"
fi
```

**In the closing messages:**

```bash
if [ "${GW_INSTALLED:-0}" = 1 ]; then
  log "streaming gateway: publish to rtmp://<this-device>:1935/live, then pick it on the Streaming tab"
  log "  ports 1935 8554 8888 8889 8890/udp are open with no credentials — the LAN is the boundary"
fi
```

## Removing it

```bash
systemctl disable --now bd-mtx bd-gw
/userdata/bd-gw/bdgw --unpatch-ui --ui-dir /srv/birddog-web-ui && systemctl restart BirdDogWebUI
rm -rf /userdata/bd-gw /etc/systemd/system/bd-{mtx,gw}.service
```

If the decoder was left on a gateway path, pick a source in AV Setup afterwards — or run
`bdgw --dir /userdata/bd-gw --print` first and clear the display path in the tab before
removing anything.
