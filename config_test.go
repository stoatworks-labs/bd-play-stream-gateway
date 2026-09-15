package main

import (
	"strings"
	"testing"
)

func TestDefaultConfigValidates(t *testing.T) {
	if err := DefaultConfig().Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestValidateRefusals(t *testing.T) {
	cases := []struct {
		name string
		mut  func(c *Config)
		want string
	}{
		{"bad path name", func(c *Config) { c.Paths[0].Name = "Live Feed" }, "name"},
		{"colon in name", func(c *Config) { c.Paths[0].Name = "a:b" }, "name"},
		{"duplicate", func(c *Config) { c.Paths[1].Name = "live" }, "two paths"},
		{"reserved", func(c *Config) { c.Paths[0].Name = "all_others"; c.CameraPath = "cam" }, "reserves"},
		{"unknown scheme", func(c *Config) { c.Paths[0].Source = "ftp://x/y" }, "source must be"},
		{"no host", func(c *Config) { c.Paths[0].Source = "rtsp:///stream" }, "no host"},
		{"quote in url", func(c *Config) { c.Paths[0].Source = `rtsp://h/"x` }, "quote"},
		{"rtp needs sdp", func(c *Config) { c.Paths[0].Source = "udp+rtp://238.0.0.1:5004" }, "SDP"},
		{"sdp must be sdp", func(c *Config) { c.Paths[0].Source = "udp+rtp://238.0.0.1:5004"; c.Paths[0].SDP = "hello" }, "v=0"},
		{"sdp on publish", func(c *Config) { c.Paths[0].SDP = "v=0" }, "only applies"},
		{"mpegts needs port", func(c *Config) { c.Paths[0].Source = "udp+mpegts://238.0.0.1" }, "ip:port"},
		{"srt needs port", func(c *Config) { c.Paths[0].Source = "srt://encoder" }, "explicit port"},
		{"display unknown", func(c *Config) { c.Display.Path = "nope" }, "display path"},
		{"latency low", func(c *Config) { c.Display.LatencyMS = 10 }, "latency"},
		{"camera unknown", func(c *Config) { c.CameraPath = "nope" }, "camera path"},
		{"forward scheme", func(c *Config) { c.Paths[0].Forward = []Forward{{ID: "a", URL: "http://x/y"}} }, "destination must start"},
		{"forward no id", func(c *Config) { c.Paths[0].Forward = []Forward{{URL: "rtmp://x/y"}} }, "no id"},
		{"forward dup id", func(c *Config) {
			c.Paths[0].Forward = []Forward{{ID: "a", URL: "rtmp://x/y"}, {ID: "a", URL: "rtmp://x/z"}}
		}, "share id"},
		{"forward fingerprint", func(c *Config) { c.Paths[0].Forward = []Forward{{ID: "a", URL: "rtmps://x/y#k", Fingerprint: "zz"}} }, "fingerprint"},
		{"forward srt port", func(c *Config) { c.Paths[0].Forward = []Forward{{ID: "a", URL: "srt://host?streamid=x"}} }, "explicit port"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := DefaultConfig()
			tc.mut(&c)
			err := c.Validate()
			if err == nil {
				t.Fatalf("expected a refusal mentioning %q, got none", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("refusal %q does not mention %q", err, tc.want)
			}
		})
	}
}

func TestValidateAccepts(t *testing.T) {
	c := DefaultConfig()
	c.Paths = append(c.Paths,
		Path{Name: "cam1", Source: "rtsp://user:p%40ss@192.168.1.20:554/stream1", OnDemand: true},
		Path{Name: "hls1", Source: "https://example.com/live/index.m3u8"},
		Path{Name: "srt1", Source: "srt://encoder.local:9000?streamid=abc"},
		Path{Name: "mc", Source: "udp+mpegts://238.0.0.1:1234?interface=eth0"},
		Path{Name: "rtp", Source: "udp+rtp://238.0.0.1:5004", SDP: "v=0\no=- 1 1 IN IP4 10.0.0.1\ns=x\nc=IN IP4 10.0.0.1\nt=0 0\nm=video 5004 RTP/AVP 96\na=rtpmap:96 H264/90000\n"},
		Path{Name: "whep1", Source: "whep://server:8889/stream"},
	)
	c.Paths[1].Forward = []Forward{
		{ID: "a1", URL: "rtmps://a.rtmp.youtube.com/live2#abcd-efgh-ijkl", Fingerprint: strings.Repeat("ab", 32)},
		{ID: "a2", URL: "rtsp://other:8554/copy"},
		{ID: "a3", URL: "srt://other:8890?streamid=publish:copy"},
		{ID: "a4", URL: "whip://other:8889/copy/whip"},
	}
	c.Display.Path = "cam1"
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestMasked(t *testing.T) {
	cases := map[string]string{
		"rtmps://a.rtmp.youtube.com/live2#abcd-efgh-ijkl-mnop": "rtmps://a.rtmp.youtube.com/live2#••••mnop",
		"rtmp://x/y#ab":                "rtmp://x/y#••••ab",
		"rtsp://user:secret@cam:554/s": "rtsp://user:%E2%80%A2%E2%80%A2%E2%80%A2%E2%80%A2@cam:554/s",
		"srt://h:9000?streamid=x&passphrase=hunter22&latency=100": "srt://h:9000?streamid=x&passphrase=••••&latency=100",
		"srt://h:9000?passphrase=hunter22":                        "srt://h:9000?passphrase=••••",
		"rtsp://cam:554/s":                                        "rtsp://cam:554/s",
		"publish":                                                 "publish",
	}
	for in, want := range cases {
		if got := Masked(in); got != want {
			t.Errorf("Masked(%q) = %q, want %q", in, got, want)
		}
	}
	// The stream key must not survive masking in any form.
	if strings.Contains(Masked("rtmps://a/b#thekey123"), "thekey") {
		t.Fatal("stream key leaked through Masked")
	}
}

func TestMergeKeepsSecretsForMaskedValues(t *testing.T) {
	stored := DefaultConfig()
	stored.Paths[0].Source = "rtsp://user:secret@cam:554/s"
	stored.Paths[0].Forward = []Forward{{ID: "k1", URL: "rtmps://a.rtmp.youtube.com/live2#realkey9"}}

	edit := stored.Public()
	// The tab sends back what it was shown, plus one new destination and a
	// changed label.
	edit.Paths[0].Label = "changed"
	edit.Paths[0].Forward = append(edit.Paths[0].Forward, Forward{URL: "rtsp://other:8554/x"})

	got := Merge(stored, edit)
	if got.Paths[0].Source != "rtsp://user:secret@cam:554/s" {
		t.Fatalf("masked source was not restored: %q", got.Paths[0].Source)
	}
	if got.Paths[0].Forward[0].URL != "rtmps://a.rtmp.youtube.com/live2#realkey9" {
		t.Fatalf("masked forward was not restored: %q", got.Paths[0].Forward[0].URL)
	}
	if got.Paths[0].Forward[1].ID == "" || got.Paths[0].Forward[1].URL != "rtsp://other:8554/x" {
		t.Fatalf("new forward not kept with a fresh id: %+v", got.Paths[0].Forward[1])
	}
	if got.Paths[0].Label != "changed" {
		t.Fatal("edit lost")
	}

	// A genuinely new value replaces the stored one.
	edit.Paths[0].Forward[0].URL = "rtmps://a.rtmp.youtube.com/live2#newkey"
	got = Merge(stored, edit)
	if got.Paths[0].Forward[0].URL != "rtmps://a.rtmp.youtube.com/live2#newkey" {
		t.Fatalf("new key was not taken: %q", got.Paths[0].Forward[0].URL)
	}
}

func TestPublicNeverCarriesAKey(t *testing.T) {
	c := DefaultConfig()
	c.Paths[0].Forward = []Forward{{ID: "k1", URL: "rtmps://a/b#SECRETKEY"}}
	c.Paths[1].Source = "rtsp://u:SECRETPASS@h/s"
	pub := c.Public()
	for _, p := range pub.Paths {
		if strings.Contains(p.Source, "SECRET") {
			t.Fatal("source secret leaked")
		}
		for _, f := range p.Forward {
			if strings.Contains(f.URL, "SECRET") {
				t.Fatal("forward secret leaked")
			}
		}
	}
}

func TestSaveAndLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	c := DefaultConfig()
	c.Paths[0].Forward = []Forward{{ID: "k1", URL: "rtmps://a/b#key"}}
	if err := c.Save(dir + "/config.json"); err != nil {
		t.Fatal(err)
	}
	got, err := LoadConfig(dir + "/config.json")
	if err != nil {
		t.Fatal(err)
	}
	if got.Paths[0].Forward[0].URL != "rtmps://a/b#key" {
		t.Fatal("round trip lost the forward")
	}
	if got.Display.LatencyMS != 120 {
		t.Fatal("latency default lost")
	}
}
