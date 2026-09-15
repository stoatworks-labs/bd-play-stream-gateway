package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeBirdUI serves the System page to a request carrying the good session,
// and the login page to anything else. protected=false makes it serve the
// System page to everyone, as a device with no password does.
func fakeBirdUI(t *testing.T, protected bool) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(SessionCookie)
		if protected && (err != nil || c.Value != "good") {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`<div id="div_login_box">login</div>`))
			return
		}
		_, _ = w.Write([]byte(`<span>Reset System Default</span>`))
	}))
}

// fakeMTX answers the control API with two paths.
func fakeMTX(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v3/paths/list" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"itemCount":2,"pageCount":1,"items":[
		  {"name":"live","confName":"live","online":true,"onlineTime":"x","source":{"type":"rtmpConn","id":"1"},
		   "tracks2":[{"codec":"H264"},{"codec":"MPEG-4 Audio"}],"readers":[{"type":"srtConn","id":"2"},{"type":"hlsSession","id":"3"}],
		   "inboundBytes":1000,"outboundBytes":2000},
		  {"name":"cam","confName":"cam","online":false,"source":null,"tracks2":[],"readers":[]}]}`))
	}))
}

type harness struct {
	api      *APIServer
	srv      *httptest.Server
	dir      string
	restarts *int
	unitOps  *[]string
}

func newHarness(t *testing.T, protected bool) *harness {
	t.Helper()
	ui := fakeBirdUI(t, protected)
	t.Cleanup(ui.Close)
	mtx := fakeMTX(t)
	t.Cleanup(mtx.Close)

	dir := t.TempDir()
	etc := filepath.Join(dir, "etc")
	_ = os.MkdirAll(etc, 0o755)
	_ = os.WriteFile(filepath.Join(etc, decSettingsFil), []byte(`{"SourceSelection":"NDI"}`), 0o644)

	restarts := 0
	dec := NewDecoder(etc, filepath.Join(dir, "state.json"), "http://127.0.0.1:1")
	dec.Restart = func(context.Context) error { restarts++; return nil }

	var ops []string
	m := NewMTX(mtx.URL, "bd-mtx")
	m.Run = func(_ context.Context, args ...string) (string, error) {
		ops = append(ops, strings.Join(args, " "))
		if args[0] == "is-active" {
			return "active", nil
		}
		return "", nil
	}
	m.Journal = func(context.Context, int) (string, error) { return "line1\nline2", nil }

	api := &APIServer{
		ConfigPath: filepath.Join(dir, "config.json"),
		YMLPath:    filepath.Join(dir, "mediamtx.yml"),
		Gate:       NewGate(ui.URL),
		MTX:        m,
		Decoder:    dec,
		Version:    "test",
		Log:        func(string, ...any) {},
	}
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)
	return &harness{api: api, srv: srv, dir: dir, restarts: &restarts, unitOps: &ops}
}

func (h *harness) do(t *testing.T, method, path, body, session string) (int, map[string]any) {
	t.Helper()
	var req *http.Request
	if body != "" {
		req, _ = http.NewRequest(method, h.srv.URL+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req, _ = http.NewRequest(method, h.srv.URL+path, nil)
	}
	req.Header.Set("Origin", "http://play.local")
	if session != "" {
		req.AddCookie(&http.Cookie{Name: SessionCookie, Value: session})
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if resp.Header.Get("Access-Control-Allow-Origin") != "http://play.local" ||
		resp.Header.Get("Access-Control-Allow-Credentials") != "true" {
		t.Fatalf("CORS must echo the origin with credentials, got %q / %q",
			resp.Header.Get("Access-Control-Allow-Origin"), resp.Header.Get("Access-Control-Allow-Credentials"))
	}
	return resp.StatusCode, out
}

func TestStatusIsOpenAndCarriesNoSecrets(t *testing.T) {
	h := newHarness(t, true)
	c := DefaultConfig()
	c.Paths[0].Forward = []Forward{{ID: "k", URL: "rtmps://a/b#SECRETKEY"}}
	_ = c.Save(h.api.ConfigPath)

	code, out := h.do(t, "GET", "/api/status", "", "")
	if code != 200 {
		t.Fatalf("status = %d", code)
	}
	raw, _ := json.Marshal(out)
	if strings.Contains(string(raw), "SECRET") {
		t.Fatal("status leaked a stream key")
	}
	if out["hub_api"] != true || out["hub_active"] != true {
		t.Fatalf("hub not reported as up: %v", out)
	}
	paths := out["paths"].([]any)
	live := paths[0].(map[string]any)
	if live["name"] != "live" || live["online"] != true || live["source_type"] != "rtmpConn" || live["readers"] != float64(2) {
		t.Fatalf("live path status wrong: %v", live)
	}
	auth := out["auth"].(map[string]any)
	if auth["authenticated"] == true {
		t.Fatal("no cookie, yet reported authenticated")
	}
}

func TestConfigNeedsASession(t *testing.T) {
	h := newHarness(t, true)
	if code, _ := h.do(t, "GET", "/api/config", "", ""); code != 403 {
		t.Fatalf("no cookie: %d, want 403", code)
	}
	if code, _ := h.do(t, "GET", "/api/config", "", "stale"); code != 403 {
		t.Fatalf("bad cookie: %d, want 403", code)
	}
	code, out := h.do(t, "GET", "/api/config", "", "good")
	if code != 200 {
		t.Fatalf("good cookie: %d", code)
	}
	if out["camera_publish_url"] != "srt://127.0.0.1:8890?streamid=publish:cam&pkt_size=1316" {
		t.Fatalf("camera url: %v", out["camera_publish_url"])
	}
}

func TestGateRefusesWhenDeviceHasNoPassword(t *testing.T) {
	h := newHarness(t, false)
	code, out := h.do(t, "GET", "/api/config", "", "good")
	if code != 403 || !strings.Contains(out["error"].(string), "no birdUI password") {
		t.Fatalf("unprotected device: %d %v", code, out)
	}
	h.api.Gate.AllowUnprotected = true
	if code, _ := h.do(t, "GET", "/api/config", "", ""); code != 200 {
		t.Fatalf("--allow-unprotected should open it: %d", code)
	}
}

func TestSaveRendersAndAppliesOnlyWhatChanged(t *testing.T) {
	h := newHarness(t, true)
	c := DefaultConfig()
	c.Paths[0].Forward = []Forward{{ID: "k1", URL: "rtmps://a.rtmp.youtube.com/live2#realkey"}}
	_ = c.Save(h.api.ConfigPath)

	// Edit as the tab would: masked values back, plus a new pull path, and
	// the display switched to it.
	pub := c.Public()
	pub.Paths = append(pub.Paths, Path{Name: "cam1", Source: "rtsp://u:p@10.0.0.9/s", OnDemand: true})
	pub.Display.Path = "cam1"
	body, _ := json.Marshal(pub)

	code, out := h.do(t, "POST", "/api/config", string(body), "good")
	if code != 200 {
		t.Fatalf("save: %d %v", code, out)
	}
	stored, _ := LoadConfig(h.api.ConfigPath)
	if stored.Paths[0].Forward[0].URL != "rtmps://a.rtmp.youtube.com/live2#realkey" {
		t.Fatal("the masked key sent back replaced the stored one")
	}
	yml, _ := os.ReadFile(h.api.YMLPath)
	if !strings.Contains(string(yml), `dest: "rtmps://a.rtmp.youtube.com/live2#realkey"`) ||
		!strings.Contains(string(yml), `source: "rtsp://u:p@10.0.0.9/s"`) {
		t.Fatalf("yml not rendered from the merged config:\n%s", yml)
	}
	if *h.restarts != 1 {
		t.Fatalf("decoder restarts = %d, want 1 (display changed)", *h.restarts)
	}
	if strings.Join(*h.unitOps, ",") != "" && strings.Contains(strings.Join(*h.unitOps, ","), "restart") {
		t.Fatalf("gateway restarted on a plain save: %v", *h.unitOps)
	}
	applied := out["applied"].([]any)
	joined := make([]string, len(applied))
	for i, a := range applied {
		joined[i] = a.(string)
	}
	if !strings.Contains(strings.Join(joined, "|"), "HDMI now follows path \"cam1\"") {
		t.Fatalf("applied: %v", joined)
	}
	// The response is masked too.
	raw, _ := json.Marshal(out)
	if strings.Contains(string(raw), "realkey") {
		t.Fatal("response leaked the key")
	}

	// Saving again with no display change does not touch the decoder.
	code, _ = h.do(t, "POST", "/api/config", string(body), "good")
	if code != 200 || *h.restarts != 1 {
		t.Fatalf("second save: %d, restarts %d", code, *h.restarts)
	}

	// Switching the gateway off stops the unit and gives the display back.
	pub.Enabled = false
	body, _ = json.Marshal(pub)
	code, _ = h.do(t, "POST", "/api/config", string(body), "good")
	if code != 200 {
		t.Fatalf("disable: %d", code)
	}
	if !strings.Contains(strings.Join(*h.unitOps, ","), "stop bd-mtx") {
		t.Fatalf("unit not stopped: %v", *h.unitOps)
	}
	if *h.restarts != 2 {
		t.Fatalf("decoder not released: restarts %d", *h.restarts)
	}
}

func TestSaveRefusesBadSettingsWithoutTouchingAnything(t *testing.T) {
	h := newHarness(t, true)
	c := DefaultConfig()
	c.Paths[0].Name = "bad name"
	body, _ := json.Marshal(c)
	code, out := h.do(t, "POST", "/api/config", string(body), "good")
	if code != 422 {
		t.Fatalf("code = %d, want 422 (%v)", code, out)
	}
	if _, err := os.Stat(h.api.YMLPath); !os.IsNotExist(err) {
		t.Fatal("yml written despite the refusal")
	}
	if code, _ := h.do(t, "POST", "/api/config", "{not json", "good"); code != 400 {
		t.Fatalf("malformed JSON: %d, want 400", code)
	}
}

func TestDisplayEndpoint(t *testing.T) {
	h := newHarness(t, true)
	_ = DefaultConfig().Save(h.api.ConfigPath)
	code, out := h.do(t, "POST", "/api/display", `{"path":"live"}`, "good")
	if code != 200 {
		t.Fatalf("switch: %d %v", code, out)
	}
	if *h.restarts != 1 {
		t.Fatalf("restarts = %d", *h.restarts)
	}
	cur := h.api.Decoder.Current()
	if !cur.OnGateway || cur.Path != "live" {
		t.Fatalf("decoder not on live: %+v", cur)
	}
	if code, _ := h.do(t, "POST", "/api/display", `{"path":"nope"}`, "good"); code != 422 {
		t.Fatalf("unknown path: %d", code)
	}
	code, _ = h.do(t, "POST", "/api/display", `{"path":""}`, "good")
	if code != 200 || *h.restarts != 2 || h.api.Decoder.Current().OnGateway {
		t.Fatalf("release: %d restarts=%d cur=%+v", code, *h.restarts, h.api.Decoder.Current())
	}
	if code, _ := h.do(t, "POST", "/api/display", `{"path":"live"}`, ""); code != 403 {
		t.Fatalf("no session: %d", code)
	}
}

func TestLogNeedsASession(t *testing.T) {
	h := newHarness(t, true)
	if code, _ := h.do(t, "GET", "/api/log", "", ""); code != 403 {
		t.Fatalf("no session: %d", code)
	}
	code, out := h.do(t, "GET", "/api/log", "", "good")
	if code != 200 || out["log"] != "line1\nline2" {
		t.Fatalf("log: %d %v", code, out)
	}
}
