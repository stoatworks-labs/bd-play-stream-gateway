/* The Streaming tab on the BirdDog PLAY's AV Setup page.
 *
 * Served from /static/, outside the Go template system, so the patch to
 * videoset.html stays two lines and everything the tab does lives here.
 *
 * Talks to bdgw's API on __BDGW_API_PORT__, sending the birdUI session cookie
 * with every request (credentials: 'include'): the config carries stream keys,
 * so reads and writes are gated on the same login as the rest of the web UI.
 * Secrets never come back to the page — URLs arrive masked, and a masked URL
 * sent back unchanged means "keep what is stored".
 */
(function () {
  'use strict';

  var API_PORT = __BDGW_API_PORT__;
  var API = location.protocol + '//' + location.hostname + ':' + API_PORT;
  var HOST = location.hostname;

  var root = document.getElementById('bdgw_form');
  if (!root) return;

  var status = null;      // last /api/status
  var cfg = null;         // editable copy of the config (masked)
  var camURL = '';
  var busy = false;
  var notice = null;
  var timer = null;
  var dirty = false;      // the form has unsaved edits; polling must not redraw it
  var logOpen = false;

  var SOURCE_KINDS = [
    ['publish', 'Published to this PLAY (OBS, vMix, an encoder, a browser)'],
    ['rtsp', 'Pulled from an RTSP camera or server'],
    ['rtmp', 'Pulled from an RTMP server'],
    ['http', 'Pulled from an HLS playlist URL'],
    ['srt', 'Pulled from an SRT sender in listener mode'],
    ['udp+mpegts', 'Received as MPEG-TS over UDP'],
    ['udp+rtp', 'Received as RTP over UDP (needs an SDP)'],
    ['whep', 'Pulled from a WebRTC (WHEP) server']
  ];

  function esc(s) {
    return String(s == null ? '' : s).replace(/[&<>"']/g, function (c) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c];
    });
  }

  function api(path, opts) {
    opts = opts || {};
    opts.credentials = 'include';
    if (opts.body) opts.headers = { 'Content-Type': 'application/json' };
    return fetch(API + path, opts).then(function (r) {
      return r.json().catch(function () { return {}; }).then(function (body) {
        if (!r.ok) throw new Error(body.error || ('HTTP ' + r.status));
        return body;
      });
    });
  }

  // ------------------------------------------------------------- data flow

  function poll() {
    return api('/api/status')
      .then(function (s) { status = s; status.unreachable = null; })
      .catch(function (e) {
        status = status || {};
        status.unreachable = e.message || 'no response';
      })
      .then(function () {
        var authed = status.auth && (status.auth.authenticated || !status.auth.required);
        if (authed && !cfg && !status.unreachable) return loadConfig();
      })
      .then(render)
      .then(function () {
        clearTimeout(timer);
        timer = setTimeout(poll, 5000);
      });
  }

  function loadConfig() {
    return api('/api/config').then(function (b) {
      cfg = b.config;
      camURL = b.camera_publish_url || '';
      dirty = false;
    }).catch(function (e) {
      notice = { kind: 'err', text: e.message };
    });
  }

  function save() {
    if (busy || !cfg) return;
    busy = true;
    notice = null;
    render();
    api('/api/config', { method: 'POST', body: JSON.stringify(cfg, function (k, v) { return k.charAt(0) === '_' ? undefined : v; }) })
      .then(function (b) {
        cfg = b.config;
        camURL = b.camera_publish_url || camURL;
        dirty = false;
        var text = (b.applied || []).join(' · ');
        if (b.warnings && b.warnings.length) text += ' — ' + b.warnings.join('; ');
        notice = { kind: b.warnings && b.warnings.length ? 'warn' : 'ok', text: text || 'Saved' };
      })
      .catch(function (e) { notice = { kind: 'err', text: e.message }; })
      .then(function () { busy = false; return poll(); });
  }

  function setDisplay(path) {
    if (busy) return;
    busy = true;
    notice = null;
    render();
    api('/api/display', { method: 'POST', body: JSON.stringify({ path: path }) })
      .then(function (b) {
        if (cfg) cfg.display = b.display;
        var text = (b.applied || []).join(' · ');
        if (b.warnings && b.warnings.length) text += ' — ' + b.warnings.join('; ');
        notice = { kind: b.warnings && b.warnings.length ? 'warn' : 'ok', text: text };
      })
      .catch(function (e) { notice = { kind: 'err', text: e.message }; })
      .then(function () { busy = false; return poll(); });
  }

  function showLog() {
    logOpen = true;
    api('/api/log').then(function (b) {
      var pre = document.getElementById('bdgw_log');
      if (pre) pre.textContent = b.log || b.error || '(empty)';
    }).catch(function (e) {
      var pre = document.getElementById('bdgw_log');
      if (pre) pre.textContent = e.message;
    });
  }

  // ----------------------------------------------------------- form state

  // The form is read back into cfg before every render that follows an edit,
  // so a poll cannot stamp on what someone is typing.
  function readForm() {
    if (!cfg) return;
    var en = document.getElementById('bdgw_enabled');
    if (en) cfg.enabled = en.value === '1';
    var cam = document.getElementById('bdgw_camera_path');
    if (cam) cfg.camera_path = cam.value.trim();
    var lat = document.getElementById('bdgw_latency');
    if (lat) cfg.display.latency_ms = parseInt(lat.value, 10) || 120;
    cfg.paths.forEach(function (p, i) {
      var g = function (k) { return document.getElementById('bdgw_p' + i + '_' + k); };
      if (g('name')) p.name = g('name').value.trim();
      if (g('label')) p.label = g('label').value.trim();
      if (g('kind')) {
        var kind = g('kind').value;
        if (kind === 'publish') {
          p.source = 'publish';
        } else {
          var url = g('url') ? g('url').value.trim() : '';
          p.source = url;
        }
      }
      if (g('ondemand')) p.on_demand = g('ondemand').checked;
      if (g('hold')) p.hold_picture = g('hold').checked;
      if (g('sdp')) p.sdp = g('sdp').value;
      (p.forward || []).forEach(function (f, j) {
        var u = document.getElementById('bdgw_p' + i + '_f' + j + '_url');
        if (u) f.url = u.value.trim();
        var fp = document.getElementById('bdgw_p' + i + '_f' + j + '_fp');
        if (fp) f.fingerprint = fp.value.trim();
      });
    });
  }

  // A path's source kind is derived from its URL, unless the person has just
  // chosen a kind in the dropdown and not yet typed the URL — that choice is
  // kept on the object as _kind and stripped before it is sent.
  function pathKind(p) {
    if (p._kind) return p._kind;
    return kindOf(p.source);
  }

  function kindOf(source) {
    if (!source || source === 'publish' || source === 'publisher') return 'publish';
    var m = /^([a-z+]+):\/\//i.exec(source);
    if (!m) return 'rtsp';
    var s = m[1].toLowerCase();
    if (s === 'rtsps') return 'rtsp';
    if (s === 'rtmps') return 'rtmp';
    if (s === 'https') return 'http';
    if (s === 'wheps') return 'whep';
    return s;
  }

  // ------------------------------------------------------------ rendering

  function row(label, controlHTML, extra) {
    return '<div class="row m-0 p-1"' + (extra || '') + '>' +
      '<div class="col-xl-4 m-0 p-0 my-auto">' + label + '</div>' +
      '<div class="col-xl-8 m-0 p-0">' + controlHTML + '</div>' +
      '</div>';
  }

  function full(html) {
    return '<div class="row m-0 p-1"><div class="col-xl-12 m-0 p-0">' + html + '</div></div>';
  }

  function hint(text) {
    return '<div style="font-size:11px;opacity:.7;margin-top:3px;">' + text + '</div>';
  }

  function button(id, text, disabled, cls) {
    return '<button type="button" class="' + (cls || 'restart') + '" id="' + id + '"' +
      (disabled ? ' disabled' : '') + ' style="white-space:nowrap;padding:4px 10px;">' + esc(text) + '</button>';
  }

  function pill(colour, text) {
    return '<span style="display:inline-block;width:.6em;height:.6em;border-radius:50%;background:' +
      colour + ';margin-right:.5em;vertical-align:middle;"></span>' + esc(text);
  }

  function noticeHTML() {
    if (!notice) return '';
    var colour = notice.kind === 'ok' ? '#22b24c' : notice.kind === 'warn' ? '#e67e22' : '#c0392b';
    return '<div class="p-2 mt-2" style="border-left:3px solid ' + colour + ';font-size:12px;">' + esc(notice.text) + '</div>';
  }

  function authBanner(s) {
    var a = s.auth || {};
    if (a.authenticated || !a.required) return '';
    return '<div class="p-2 mb-2" style="border-left:3px solid #e67e22;background:rgba(230,126,34,.08);font-size:12px;">' +
      esc(a.reason || 'Log in to birdUI to change streaming settings.') + '</div>';
  }

  function hubPill(s) {
    if (s.unreachable) return pill('#c0392b', 'Panel service not responding (bd-gw)');
    if (!s.enabled) return pill('#7f8c8d', 'Gateway switched off');
    if (!s.hub_active) return pill('#c0392b', 'Gateway service not running (bd-mtx)');
    if (!s.hub_api) return pill('#e67e22', 'Gateway starting…');
    return pill('#22b24c', 'Gateway running — MediaMTX ' + (s.mediamtx_version || ''));
  }

  function liveByName(s) {
    var m = {};
    (s.paths || []).forEach(function (p) { m[p.name] = p; });
    return m;
  }

  function sourceWord(t) {
    return ({
      rtmpConn: 'RTMP publisher', rtmpsConn: 'RTMPS publisher', rtmpSource: 'RTMP pull',
      rtspSession: 'RTSP publisher', rtspsSession: 'RTSPS publisher', rtspSource: 'RTSP pull',
      srtConn: 'SRT publisher', srtSource: 'SRT pull', hlsSource: 'HLS pull',
      webRTCSession: 'WebRTC publisher', webRTCSource: 'WHEP pull',
      mpegtsSource: 'UDP MPEG-TS', rtpSource: 'UDP RTP'
    })[t] || t || '';
  }

  function readersWord(p) {
    if (!p || !p.readers) return 'no readers';
    var parts = [];
    var names = { srtConn: 'SRT', hlsSession: 'HLS', rtspSession: 'RTSP', rtspsSession: 'RTSPS',
      rtmpConn: 'RTMP', rtmpsConn: 'RTMPS', webRTCSession: 'WebRTC', rtspConn: 'RTSP' };
    Object.keys(p.reader_types || {}).forEach(function (k) {
      parts.push(p.reader_types[k] + ' ' + (names[k] || k));
    });
    return (p.readers === 1 ? '1 reader' : p.readers + ' readers') + (parts.length ? ' (' + parts.join(', ') + ')' : '');
  }

  function statusSection(s) {
    var live = liveByName(s);
    var dec = s.decoder || {};
    var out = full('<b>Status</b>') + full(hubPill(s));
    if (s.error) out += full('<span style="color:#c0392b;font-size:12px;">' + esc(s.error) + '</span>');

    var hdmi;
    if (dec.on_gateway) {
      var lp = live[dec.path];
      hdmi = 'Showing path <b>' + esc(dec.path) + '</b>' +
        (lp ? (lp.online ? ' — ' + esc(sourceWord(lp.source_type)) + ' connected' : ' — waiting for a publisher') : '');
    } else {
      hdmi = 'Stock source (' + esc(dec.selection || 'unknown') + ')' +
        (dec.held && dec.held.took ? ' — the gateway expected to hold the display; someone changed it in AV Setup' : '');
    }
    out += full('<b>HDMI output</b>: ' + hdmi);

    (s.configured || []).forEach(function (c) {
      var p = live[c.name];
      var line;
      if (!p) line = pill('#7f8c8d', c.name + ' — not known to the gateway yet');
      else if (p.online) line = pill('#22b24c', c.name + ' — ' + sourceWord(p.source_type) + ', ' +
        (p.tracks || []).join(' + ') + ', ' + readersWord(p));
      else line = pill('#e67e22', c.name + ' — ' + (c.publish ? 'waiting for a publisher' : (c.on_demand ? 'idle until something reads it' : 'source not connected')) +
        (p.readers ? ', ' + readersWord(p) : ''));
      out += full('<div style="font-size:12px;">' + line + '</div>');
    });
    return out;
  }

  function endpointsSection(s) {
    var camPath = (cfg && cfg.camera_path) || s.camera_path || 'cam';
    var ports = s.ports || {};
    var html = full('<b>Addresses</b>') +
      full('<div style="font-size:12px;line-height:1.7;">' +
        '<b>Publish to this PLAY</b> (replace <i>name</i> with a path below):<br>' +
        code('rtmp://' + HOST + ':' + (ports.rtmp || 1935) + '/<i>name</i>') + '<br>' +
        code('rtsp://' + HOST + ':' + (ports.rtsp || 8554) + '/<i>name</i>') + '<br>' +
        code('srt://' + HOST + ':' + (ports.srt || 8890) + '?streamid=publish:<i>name</i>&amp;pkt_size=1316') + '<br>' +
        code('http://' + HOST + ':' + (ports.webrtc || 8889) + '/<i>name</i>/whip') + ' (WHIP, H.264 only reaches HDMI)<br>' +
        '<b>Watch the camera</b> (path <i>' + esc(camPath) + '</i>):<br>' +
        code('rtsp://' + HOST + ':' + (ports.rtsp || 8554) + '/' + esc(camPath)) + ' · ' +
        code('rtmp://' + HOST + ':' + (ports.rtmp || 1935) + '/' + esc(camPath)) + '<br>' +
        code('http://' + HOST + ':' + (ports.hls || 8888) + '/' + esc(camPath)) + ' (HLS) · ' +
        code('http://' + HOST + ':' + (ports.webrtc || 8889) + '/' + esc(camPath)) + ' (WebRTC)<br>' +
        code('srt://' + HOST + ':' + (ports.srt || 8890) + '?streamid=read:' + esc(camPath)) + '<br>' +
        (camURL ? '<b>bdcam</b> — set its SRT destination to ' + code(esc(camURL)) + ' and it appears at the addresses above.' : '') +
      '</div>');
    return html;
  }

  function code(s) {
    return '<code style="font-size:11px;user-select:all;">' + s + '</code>';
  }

  function pathEditor(p, i, locked) {
    var id = function (k) { return 'bdgw_p' + i + '_' + k; };
    var kind = pathKind(p);
    var isPublish = kind === 'publish';
    var kindOptions = SOURCE_KINDS.map(function (k) {
      return '<option value="' + k[0] + '"' + (k[0] === kind ? ' selected' : '') + '>' + esc(k[1]) + '</option>';
    }).join('');
    var dis = locked ? ' disabled' : '';
    var html =
      '<div style="border:1px solid rgba(127,127,127,.35);padding:6px 8px;margin:0 0 8px 0;">' +
        row('Path name',
            '<input type="text" id="' + id('name') + '" value="' + esc(p.name) + '" maxlength="32"' + dis + ' style="width:100%;">' +
            hint('a–z, 0–9, - and _; this is the <i>name</i> in the addresses')) +
        row('Note', '<input type="text" id="' + id('label') + '" value="' + esc(p.label || '') + '"' + dis + ' style="width:100%;" placeholder="what this is">') +
        row('Source', '<select id="' + id('kind') + '"' + dis + ' style="width:100%;">' + kindOptions + '</select>');
    if (!isPublish) {
      html += row('URL', '<input type="text" id="' + id('url') + '" value="' + esc(p.source) + '"' + dis + ' style="width:100%;" placeholder="' +
        esc(placeholderFor(kind)) + '">' + hint('a URL that came back masked (••••) is kept as stored unless you replace it'));
      html += row('Pull', '<label style="font-weight:normal;"><input type="checkbox" id="' + id('ondemand') + '"' +
        (p.on_demand ? ' checked' : '') + dis + ' style="width:auto;margin-right:6px;vertical-align:middle;">only while something is watching it</label>');
      if (kind === 'udp+rtp') {
        html += row('SDP', '<textarea id="' + id('sdp') + '" rows="5"' + dis + ' style="width:100%;font-family:monospace;font-size:11px;">' + esc(p.sdp || '') + '</textarea>');
      }
    }
    html += row('While empty', '<label style="font-weight:normal;"><input type="checkbox" id="' + id('hold') + '"' +
      (p.hold_picture ? ' checked' : '') + dis + ' style="width:auto;margin-right:6px;vertical-align:middle;">hold a slate so readers stay connected</label>' +
      hint('unverified against the PLAY’s decoder — try it if HDMI drops when a publisher stops'));

    var fwd = p.forward || [];
    var fhtml = fwd.map(function (f, j) {
      var fid = function (k) { return 'bdgw_p' + i + '_f' + j + '_' + k; };
      return '<div style="display:flex;gap:6px;align-items:center;margin:0 0 4px 0;">' +
        '<input type="text" id="' + fid('url') + '" value="' + esc(f.url) + '"' + dis + ' style="flex:1;min-width:0;" placeholder="rtmps://a.rtmp.youtube.com/live2#stream-key">' +
        '<input type="text" id="' + fid('fp') + '" value="' + esc(f.fingerprint || '') + '"' + dis + ' style="width:9em;" placeholder="TLS fingerprint" title="SHA-256 fingerprint, only for a self-signed destination certificate">' +
        button(fid('del'), '×', locked, 'restart') +
      '</div>';
    }).join('');
    html += row('Push to', fhtml + button(id('addfwd'), 'ADD DESTINATION', locked) +
      hint('rtmp(s)://…#streamKey, rtsp(s)://…, srt://host:port?streamid=…, whip(s)://…/whip. YouTube refuses video without audio.'));

    html += '<div class="row m-0 p-1"><div class="col-xl-4 m-0 p-0"></div><div class="col-xl-8 m-0 p-0">' +
      button(id('del'), 'REMOVE PATH', locked) + '</div></div>';
    html += '</div>';
    return html;
  }

  function placeholderFor(kind) {
    return ({
      rtsp: 'rtsp://user:pass@192.168.1.20:554/stream1',
      rtmp: 'rtmp://server:1935/live/key',
      http: 'https://example.com/live/playlist.m3u8',
      srt: 'srt://encoder:9000?streamid=…',
      'udp+mpegts': 'udp+mpegts://238.0.0.1:1234',
      'udp+rtp': 'udp+rtp://238.0.0.1:5004',
      whep: 'whep://server:8889/stream'
    })[kind] || '';
  }

  // The tab is two columns. The left one is the form and is only redrawn on
  // demand — a poll must never rebuild it under someone's cursor; the right one
  // is status and is redrawn on every poll.
  function skeleton() {
    if (document.getElementById('bdgw_left')) return;
    root.innerHTML =
      '<div class="row m-0 pt-2 div_tab_contents">' +
        '<div class="col-xl-6 m-0 p-0 pr-xl-2"><div id="bdgw_left"></div></div>' +
        '<div class="col-xl-6 m-0 p-0 pl-xl-2"><div id="bdgw_right"></div></div>' +
      '</div>';
  }

  function render() {
    skeleton();
    renderRight();
    if (!dirty || busy || !cfg) renderLeft();
    else renderNotice();
  }

  function renderNotice() {
    var n = document.getElementById('bdgw_notice');
    if (n) n.innerHTML = noticeHTML();
  }

  function renderLeft() {
    var s = status || {};
    var authed = s.auth && (s.auth.authenticated || !s.auth.required);
    var locked = !authed || busy || !cfg;
    var left = authBanner(s);
    if (cfg) {
      var dispOptions = '<option value=""' + (!cfg.display.path ? ' selected' : '') + '>Leave the decoder on its stock source</option>';
      cfg.paths.forEach(function (p) {
        dispOptions += '<option value="' + esc(p.name) + '"' + (cfg.display.path === p.name ? ' selected' : '') + '>' + esc(p.name) + (p.label ? ' — ' + esc(p.label) : '') + '</option>';
      });
      left +=
        row('Gateway', '<select id="bdgw_enabled"' + (locked ? ' disabled' : '') + '>' +
            '<option value="0"' + (!cfg.enabled ? ' selected' : '') + '>Off</option>' +
            '<option value="1"' + (cfg.enabled ? ' selected' : '') + '>On</option></select>') +
        row('Show on HDMI', '<div style="display:flex;gap:6px;align-items:center;">' +
            '<select id="bdgw_display" style="flex:1;min-width:0;"' + (locked ? ' disabled' : '') + '>' + dispOptions + '</select>' +
            button('bdgw_switch', 'SWITCH', locked) + '</div>' +
            hint('switches the PLAY’s own decoder to that path over SRT on loopback and restarts it; the OSD, tally and web UI stay as they are')) +
        row('SRT latency', '<input type="number" id="bdgw_latency" min="80" max="8000" value="' + esc(cfg.display.latency_ms) + '"' + (locked ? ' disabled' : '') + ' style="width:6em;"> ms' +
            hint('what the decoder is asked for on loopback; 80 is its floor')) +
        row('Camera path', '<input type="text" id="bdgw_camera_path" value="' + esc(cfg.camera_path || '') + '"' + (locked ? ' disabled' : '') + ' style="width:100%;">' +
            hint('the path bdcam publishes to, so the addresses on the right can name it')) +
        full('<b>Paths</b>') +
        cfg.paths.map(function (p, i) { return pathEditor(p, i, locked); }).join('') +
        '<div class="row m-0 p-1"><div class="col-xl-12 m-0 p-0">' + button('bdgw_addpath', 'ADD PATH', locked) + '</div></div>' +
        '<div class="row m-0 p-1"><div class="col-xl-4 m-0 p-0"></div><div class="col-xl-8 m-0 p-0">' +
          button('bdgw_save', busy ? 'Working…' : 'APPLY', locked) + '</div></div>';
    } else if (authed && !s.unreachable) {
      left += full('Loading settings…');
    }
    left += full('<div id="bdgw_notice">' + noticeHTML() + '</div>');
    document.getElementById('bdgw_left').innerHTML = left;
    wire();
  }

  function renderRight() {
    var s = status || {};
    var authed = s.auth && (s.auth.authenticated || !s.auth.required);
    var right =
      statusSection(s) + endpointsSection(s) +
      full(button('bdgw_showlog', logOpen ? 'REFRESH LOG' : 'SHOW LOG', !authed, 'restart') +
        (logOpen ? '<pre id="bdgw_log" style="max-height:16em;overflow:auto;font-size:11px;white-space:pre-wrap;margin-top:6px;"></pre>' : '')) +
      full('<div style="font-size:11px;opacity:.75;margin-top:6px;">Ports ' +
        Object.keys(s.ports || {}).map(function (k) { return k + ' ' + s.ports[k]; }).join(', ') +
        '. No credentials on any of them: the LAN or the tailnet is the boundary, exactly as for the PLAY’s own API. ' +
        'Gateway by <a href="https://github.com/bluenviron/mediamtx" target="_blank" rel="noopener">MediaMTX</a> (MIT); tab by ' +
        '<a href="https://github.com/stoatworks-labs/bd-play-stream-gateway" target="_blank" rel="noopener">bd-play-stream-gateway</a> ' + esc(s.version || '') + '.</div>');
    document.getElementById('bdgw_right').innerHTML = right;
    var b = document.getElementById('bdgw_showlog');
    if (b) b.addEventListener('click', function () { showLog(); renderRight(); });
    if (logOpen) showLog();
  }

  function wire() {
    var left = document.getElementById('bdgw_left');
    var on = function (id, ev, fn) { var e = document.getElementById(id); if (e) e.addEventListener(ev, fn); };
    left.querySelectorAll('input, select, textarea').forEach(function (e) {
      e.addEventListener('input', function () { dirty = true; });
      e.addEventListener('change', function () { dirty = true; });
    });
    on('bdgw_save', 'click', function () { readForm(); save(); });
    on('bdgw_switch', 'click', function () {
      var sel = document.getElementById('bdgw_display');
      readForm();
      if (dirty) { notice = { kind: 'warn', text: 'Apply the path changes first, then switch.' }; renderNotice(); return; }
      setDisplay(sel ? sel.value : '');
    });
    on('bdgw_addpath', 'click', function () {
      readForm();
      cfg.paths.push({ name: 'path' + (cfg.paths.length + 1), label: '', source: 'publish', forward: [] });
      dirty = true;
      renderLeft();
    });
    if (!cfg) return;
    cfg.paths.forEach(function (p, i) {
      var id = function (k) { return 'bdgw_p' + i + '_' + k; };
      on(id('kind'), 'change', function () {
        readForm();
        var k = document.getElementById(id('kind')).value;
        p._kind = k;
        if (k === 'publish') p.source = 'publish';
        else if (kindOf(p.source) !== k) p.source = '';
        dirty = true;
        renderLeft();
      });
      on(id('del'), 'click', function () { readForm(); cfg.paths.splice(i, 1); dirty = true; renderLeft(); });
      on(id('addfwd'), 'click', function () { readForm(); (p.forward = p.forward || []).push({ id: '', url: '' }); dirty = true; renderLeft(); });
      (p.forward || []).forEach(function (f, j) {
        on('bdgw_p' + i + '_f' + j + '_del', 'click', function () { readForm(); p.forward.splice(j, 1); dirty = true; renderLeft(); });
      });
    });
  }

  poll();
})();
