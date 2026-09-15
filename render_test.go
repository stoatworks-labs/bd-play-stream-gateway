package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixtures are the configs whose rendered yml CI hands to the real MediaMTX
// binary's --validate-conf. Each is written to testdata/rendered/<name>.yml
// by TestRenderFixtures, so the validation step needs no Go at all.
func fixtures() map[string]Config {
	def := DefaultConfig()

	pulls := DefaultConfig()
	pulls.Paths = append(pulls.Paths,
		Path{Name: "cam1", Label: "lobby camera", Source: "rtsp://user:p%40ss@192.168.1.20:554/stream1", OnDemand: true},
		Path{Name: "hls1", Source: "https://example.com/live/index.m3u8", OnDemand: true},
		Path{Name: "srt1", Source: "srt://encoder.local:9000?streamid=abc"},
		Path{Name: "mc", Source: "udp+mpegts://238.0.0.1:1234?interface=eth0"},
		Path{Name: "rtp", Source: "udp+rtp://238.0.0.1:5004",
			SDP: "v=0\no=- 123 123 IN IP4 192.168.1.100\ns=H264\nc=IN IP4 192.168.1.100\nt=0 0\nm=video 5004 RTP/AVP 96\na=rtpmap:96 H264/90000\n"},
		Path{Name: "whep1", Source: "whep://server:8889/stream"},
	)
	pulls.Display = Display{Path: "cam1", LatencyMS: 120}

	push := DefaultConfig()
	push.Paths[1].Forward = []Forward{
		{ID: "a1", URL: "rtmps://a.rtmp.youtube.com/live2#abcd-efgh-ijkl", Fingerprint: "131409734af825d8e994cce4cb205bc5de6c657271673650aabd8c504c27e891"},
		{ID: "a2", URL: "rtsp://other:8554/copy"},
		{ID: "a3", URL: "srt://other:8890?streamid=publish:copy"},
		{ID: "a4", URL: "whip://other:8889/copy/whip"},
	}
	push.Paths[0].HoldPicture = true

	empty := Config{Enabled: true, Display: Display{LatencyMS: 120}}

	return map[string]Config{
		"default": def,
		"pulls":   pulls,
		"push":    push,
		"empty":   empty,
	}
}

func TestRenderFixtures(t *testing.T) {
	dir := filepath.Join("testdata", "rendered")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, c := range fixtures() {
		if err := c.Validate(); err != nil {
			t.Fatalf("fixture %s: %v", name, err)
		}
		out := Render(c)
		if err := os.WriteFile(filepath.Join(dir, name+".yml"), []byte(out), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRenderShape(t *testing.T) {
	out := Render(fixtures()["pulls"])
	for _, want := range []string{
		"api: yes\napiAddress: 127.0.0.1:9997\n",
		"rtspAddress: :8554\n",
		"rtmpAddress: :1935\n",
		"hlsAddress: :8888\n",
		"webrtcAddress: :8889\n",
		"srtAddress: :8890\n",
		"moq: no\n",
		"  live:\n",
		"    source: publisher\n",
		`    source: "rtsp://user:p%40ss@192.168.1.20:554/stream1"` + "\n    sourceOnDemand: yes\n",
		"    rtpSDP: |\n      v=0\n",
		"      a=rtpmap:96 H264/90000\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered yml lacks %q\n%s", want, out)
		}
	}
	// Nothing user-supplied may appear unquoted.
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "source: ") && !strings.Contains(line, "publisher") && !strings.Contains(line, `"`) {
			t.Errorf("unquoted source line: %q", line)
		}
	}
}

func TestRenderForwards(t *testing.T) {
	out := Render(fixtures()["push"])
	for _, want := range []string{
		"    forward:\n",
		`      - dest: "rtmps://a.rtmp.youtube.com/live2#abcd-efgh-ijkl"` + "\n",
		`        destFingerprint: "131409734af825d8e994cce4cb205bc5de6c657271673650aabd8c504c27e891"` + "\n",
		`      - dest: "whip://other:8889/copy/whip"` + "\n",
		"    alwaysAvailable: yes\n    alwaysAvailableTracks:\n      - codec: H264\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered yml lacks %q\n%s", want, out)
		}
	}
}

func TestRenderEmptyPaths(t *testing.T) {
	out := Render(fixtures()["empty"])
	if !strings.Contains(out, "paths:\n  {}\n") {
		t.Fatalf("empty path map not rendered as {}:\n%s", out)
	}
}

func TestYQ(t *testing.T) {
	cases := map[string]string{
		`plain`:     `"plain"`,
		`a"b`:       `"a\"b"`,
		`a\b`:       `"a\\b"`,
		"a\nb":      `"a\nb"`,
		"tab\there": `"tab\there"`,
		"\x01":      `"\x01"`,
		"üñí":       `"üñí"`,
	}
	for in, want := range cases {
		if got := yq(in); got != want {
			t.Errorf("yq(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestDecoderURI(t *testing.T) {
	got := DecoderURI("live", 120)
	want := "127.0.0.1:8890?mode=caller&latency=120&streamid=read:live"
	if got != want {
		t.Fatalf("DecoderURI = %q, want %q", got, want)
	}
}

func TestEndpoints(t *testing.T) {
	e := EndpointsFor("10.0.0.5", "cam")
	if e.ReadRTSP != "rtsp://10.0.0.5:8554/cam" || e.PublishWHIP != "http://10.0.0.5:8889/cam/whip" ||
		e.ReadSRT != "srt://10.0.0.5:8890?streamid=read:cam" {
		t.Fatalf("unexpected endpoints: %+v", e)
	}
	if CameraPublishURL("cam") != "srt://127.0.0.1:8890?streamid=publish:cam&pkt_size=1316" {
		t.Fatal(CameraPublishURL("cam"))
	}
}
