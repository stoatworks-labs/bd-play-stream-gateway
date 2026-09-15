package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeEtc reproduces the three files as the stock UI leaves them on a unit
// that has been showing an NDI source, with one SRT stream someone added.
func fakeEtc(t *testing.T) (*Decoder, string, *int) {
	t.Helper()
	etc := t.TempDir()
	must := func(name, content string) {
		if err := os.WriteFile(filepath.Join(etc, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must(decSettingsFil, `{"ColorSpace":"YUV","NDIAudio":"NDIAudioDis","ScreenSaverMode":"BlackSS","SourceSelection":"NDI","TallyMode":"TallyOff","ChNum":1}`)
	must(sourceNameFile, "STUDIO (Cam 1)")
	must(srtListFile, `{"streams":[{"name":"desk","uri":"192.168.1.9:9000?mode=caller&latency=120"}],"other":true}`)

	restarts := 0
	d := NewDecoder(etc, filepath.Join(t.TempDir(), "state.json"), "http://127.0.0.1:1")
	d.Restart = func(context.Context) error { restarts++; return nil }
	return d, etc, &restarts
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestTakeWritesTheThreeFilesAndRestartsOnce(t *testing.T) {
	d, etc, restarts := fakeEtc(t)
	if err := d.Take(context.Background(), "live", 120); err != nil {
		t.Fatal(err)
	}
	if *restarts != 1 {
		t.Fatalf("restarts = %d, want 1", *restarts)
	}
	if got := read(t, filepath.Join(etc, sourceNameFile)); got != "SRT:bdgw-live(127.0.0.1:8890?mode=caller&latency=120&streamid=read:live)" {
		t.Fatalf("source name = %q", got)
	}
	var settings map[string]any
	if err := json.Unmarshal([]byte(read(t, filepath.Join(etc, decSettingsFil))), &settings); err != nil {
		t.Fatal(err)
	}
	if settings["SourceSelection"] != "SRT" || settings["TallyMode"] != "TallyOff" || settings["ChNum"] != float64(1) {
		t.Fatalf("settings not updated in place: %v", settings)
	}
	var list map[string]any
	if err := json.Unmarshal([]byte(read(t, filepath.Join(etc, srtListFile))), &list); err != nil {
		t.Fatal(err)
	}
	streams := list["streams"].([]any)
	if len(streams) != 2 || list["other"] != true {
		t.Fatalf("SRT list not preserved + extended: %v", list)
	}
	last := streams[1].(map[string]any)
	if last["name"] != "bdgw-live" || !strings.HasPrefix(last["uri"].(string), "127.0.0.1:8890?") {
		t.Fatalf("our entry wrong: %v", last)
	}

	// Same selection again: nothing happens, no second restart.
	if err := d.Take(context.Background(), "live", 120); err != nil {
		t.Fatal(err)
	}
	if *restarts != 1 {
		t.Fatalf("idempotent take restarted again: %d", *restarts)
	}

	// A different path replaces our entry rather than adding a second one.
	if err := d.Take(context.Background(), "cam", 200); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(read(t, filepath.Join(etc, srtListFile))), &list); err != nil {
		t.Fatal(err)
	}
	if n := len(list["streams"].([]any)); n != 2 {
		t.Fatalf("expected the entry to be replaced, have %d streams", n)
	}
	if *restarts != 2 {
		t.Fatalf("restarts = %d, want 2", *restarts)
	}
	st, _ := d.LoadState()
	if !st.Took || st.Path != "cam" || st.PreviousSourceName != "STUDIO (Cam 1)" || st.PreviousSelection != "NDI" {
		t.Fatalf("state wrong: %+v", st)
	}
}

func TestReleaseRestoresWhatWasThere(t *testing.T) {
	d, etc, restarts := fakeEtc(t)
	if err := d.Take(context.Background(), "live", 120); err != nil {
		t.Fatal(err)
	}
	if err := d.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
	if *restarts != 2 {
		t.Fatalf("restarts = %d, want 2", *restarts)
	}
	if got := read(t, filepath.Join(etc, sourceNameFile)); got != "STUDIO (Cam 1)" {
		t.Fatalf("source name not restored: %q", got)
	}
	var settings map[string]any
	_ = json.Unmarshal([]byte(read(t, filepath.Join(etc, decSettingsFil))), &settings)
	if settings["SourceSelection"] != "NDI" {
		t.Fatalf("selection not restored: %v", settings["SourceSelection"])
	}
	var list map[string]any
	_ = json.Unmarshal([]byte(read(t, filepath.Join(etc, srtListFile))), &list)
	streams := list["streams"].([]any)
	if len(streams) != 1 || streams[0].(map[string]any)["name"] != "desk" {
		t.Fatalf("someone else's SRT entry was not kept alone: %v", streams)
	}
	st, _ := d.LoadState()
	if st.Took {
		t.Fatal("state still says took")
	}
	// Releasing again is a no-op.
	if err := d.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
	if *restarts != 2 {
		t.Fatal("release without a take restarted the decoder")
	}
}

func TestTakeOnABareUnit(t *testing.T) {
	// No files at all, as on a unit that has never had a source selected.
	etc := t.TempDir()
	restarts := 0
	d := NewDecoder(etc, filepath.Join(t.TempDir(), "state.json"), "http://127.0.0.1:1")
	d.Restart = func(context.Context) error { restarts++; return nil }
	if err := d.Take(context.Background(), "live", 80); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{srtListFile, sourceNameFile, decSettingsFil} {
		if _, err := os.Stat(filepath.Join(etc, f)); err != nil {
			t.Fatalf("%s not created: %v", f, err)
		}
	}
	if err := d.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(etc, sourceNameFile)); !os.IsNotExist(err) {
		t.Fatal("a selection file we created should be removed on release")
	}
}

func TestSyncFollowsTheConfig(t *testing.T) {
	d, _, restarts := fakeEtc(t)
	c := DefaultConfig()
	if err := d.Sync(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if *restarts != 0 {
		t.Fatal("no display path, yet the decoder was touched")
	}
	c.Display.Path = "live"
	if err := d.Sync(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if !d.Current().OnGateway || d.Current().Path != "live" {
		t.Fatalf("Current() = %+v", d.Current())
	}
	c.Enabled = false
	if err := d.Sync(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if d.Current().OnGateway {
		t.Fatal("disabling the gateway should release the decoder")
	}
	if *restarts != 2 {
		t.Fatalf("restarts = %d, want 2", *restarts)
	}
}

func TestCurrentNoticesAManualChange(t *testing.T) {
	d, etc, _ := fakeEtc(t)
	if err := d.Take(context.Background(), "live", 120); err != nil {
		t.Fatal(err)
	}
	// Someone picks NDI again in AV Setup.
	if err := os.WriteFile(filepath.Join(etc, decSettingsFil), []byte(`{"SourceSelection":"NDI"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cur := d.Current()
	if cur.OnGateway {
		t.Fatal("selection is NDI, so we are not on the gateway")
	}
	if !cur.Held.Took {
		t.Fatal("state should still record that we took the display")
	}
}
