package main

// Pointing the PLAY's own decoder at a path on the hub.
//
// The whole design rests on not replacing PPApp: it keeps the OSD, tally, the
// web UI's video integration and CloudConnect, and if the hub dies the PLAY
// simply behaves like a PLAY. So the picture reaches HDMI by having PPApp
// receive the chosen path as an ordinary SRT source over loopback.
//
// Selecting an SRT source is what the stock AV Setup page does, and it is
// three files plus a restart — all recovered from the firmware's own Express
// server (bin/BirdDogSrvr/BirdDog.js) and videoset.html:
//
//   /etc/birddog_srt_src.json      {"streams":[{"name":…,"uri":…}]} — the OSD list
//   /etc/birddog-source1-name      SRT:<name>(<uri>)               — the selection
//   /etc/birddog-dec1-settings.json  "SourceSelection":"SRT"        — the mode
//   POST :8080/restart              systemctl restart BirdDogRunner
//
// PPApp reads the files when it starts and otherwise carries on with what it
// had, hence the restart — the same thing the stock page does, and the same
// thing bdcam's decoder route does for NDI. Restarting the runner is not free
// (bd-play-usb-player measured it destabilising PPApp when cycled often), so
// it happens only when the selection actually changes, never on every save.
//
// Whatever was selected before is remembered, and put back when the display
// path is cleared, so switching the gateway off returns the box to the NDI
// source it was showing.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	srtListFile    = "birddog_srt_src.json"
	sourceNameFile = "birddog-source1-name"
	decSettingsFil = "birddog-dec1-settings.json"

	// entryPrefix names our entries in the OSD's SRT list. Hyphenated, not
	// colon-separated: the stock page builds `$('#' + name)` selectors from
	// these names and a colon would turn ours into a pseudo-selector.
	entryPrefix = "bdgw-"
)

// Decoder drives PPApp's source selection.
type Decoder struct {
	// Etc is where the three files live; /etc on the device, a temp dir in tests.
	Etc string
	// StatePath remembers what was selected before we took over.
	StatePath string
	// API is the device's own REST API, for /restart.
	API string
	// Restart replaces the real restart in tests.
	Restart func(ctx context.Context) error
	// Log is where progress goes.
	Log func(format string, args ...any)
}

// DecoderState is persisted so a restart of the panel does not forget what
// to put back.
type DecoderState struct {
	// Took is true while the decoder is pointed at one of our paths.
	Took bool `json:"took"`
	// Path and LatencyMS are what it is pointed at.
	Path      string `json:"path,omitempty"`
	LatencyMS int    `json:"latency_ms,omitempty"`
	// PreviousSourceName is birddog-source1-name as found before we took over
	// (empty if the file did not exist); PreviousSelection is SourceSelection.
	PreviousSourceName string `json:"previous_source_name,omitempty"`
	PreviousSelection  string `json:"previous_selection,omitempty"`
	TakenAt            string `json:"taken_at,omitempty"`
}

func NewDecoder(etc, statePath, api string) *Decoder {
	d := &Decoder{Etc: etc, StatePath: statePath, API: api, Log: func(string, ...any) {}}
	d.Restart = d.restartViaAPI
	return d
}

func (d *Decoder) path(name string) string { return filepath.Join(d.Etc, name) }

func (d *Decoder) LoadState() (DecoderState, error) {
	var s DecoderState
	b, err := os.ReadFile(d.StatePath)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return s, err
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return DecoderState{}, fmt.Errorf("%s: %w", d.StatePath, err)
	}
	return s, nil
}

func (d *Decoder) saveState(s DecoderState) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(d.StatePath, append(b, '\n'), 0o600)
}

// srtList is the shape of birddog_srt_src.json. Unknown keys are kept.
type srtList struct {
	Streams []srtEntry `json:"streams"`
	extra   map[string]json.RawMessage
}

type srtEntry struct {
	Name string `json:"name"`
	URI  string `json:"uri"`
}

func (d *Decoder) readList() (srtList, error) {
	var l srtList
	b, err := os.ReadFile(d.path(srtListFile))
	if err != nil {
		if os.IsNotExist(err) {
			return l, nil
		}
		return l, err
	}
	raw := map[string]json.RawMessage{}
	if len(bytes.TrimSpace(b)) > 0 {
		if err := json.Unmarshal(b, &raw); err != nil {
			// A file the stock UI could not read either. Start over rather
			// than refuse: the list is a convenience for the OSD, and the
			// selection file is what the decoder actually connects with.
			d.Log("WARNING: %s is not valid JSON (%v) — rewriting it", srtListFile, err)
			return l, nil
		}
	}
	if s, ok := raw["streams"]; ok {
		_ = json.Unmarshal(s, &l.Streams)
		delete(raw, "streams")
	}
	l.extra = raw
	return l, nil
}

func (d *Decoder) writeList(l srtList) error {
	out := map[string]any{}
	for k, v := range l.extra {
		out[k] = v
	}
	if l.Streams == nil {
		l.Streams = []srtEntry{}
	}
	out["streams"] = l.Streams
	b, err := json.Marshal(out)
	if err != nil {
		return err
	}
	return writeFileAtomic(d.path(srtListFile), b, 0o644)
}

func (d *Decoder) readSettings() (map[string]any, error) {
	m := map[string]any{}
	b, err := os.ReadFile(d.path(decSettingsFil))
	if err != nil {
		if os.IsNotExist(err) {
			return m, nil
		}
		return nil, err
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return m, nil
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("%s: %w", decSettingsFil, err)
	}
	return m, nil
}

func (d *Decoder) writeSettings(m map[string]any) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return writeFileAtomic(d.path(decSettingsFil), b, 0o644)
}

func (d *Decoder) readSourceName() string {
	b, err := os.ReadFile(d.path(sourceNameFile))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// EntryName is the OSD list name for a path.
func EntryName(path string) string { return entryPrefix + path }

// Take points the decoder at path. It is idempotent for an unchanged
// selection, and remembers the previous selection the first time.
func (d *Decoder) Take(ctx context.Context, path string, latencyMS int) error {
	st, err := d.LoadState()
	if err != nil {
		return err
	}
	if st.Took && st.Path == path && st.LatencyMS == latencyMS {
		return nil
	}
	name := EntryName(path)
	uri := DecoderURI(path, latencyMS)

	if !st.Took {
		st.PreviousSourceName = d.readSourceName()
		settings, err := d.readSettings()
		if err != nil {
			return err
		}
		if v, ok := settings["SourceSelection"].(string); ok {
			st.PreviousSelection = v
		}
	}

	// The OSD list: replace any entry of ours, keep everyone else's.
	l, err := d.readList()
	if err != nil {
		return err
	}
	kept := l.Streams[:0:0]
	for _, e := range l.Streams {
		if !strings.HasPrefix(e.Name, entryPrefix) {
			kept = append(kept, e)
		}
	}
	l.Streams = append(kept, srtEntry{Name: name, URI: uri})
	if err := d.writeList(l); err != nil {
		return err
	}

	settings, err := d.readSettings()
	if err != nil {
		return err
	}
	settings["SourceSelection"] = "SRT"
	if err := d.writeSettings(settings); err != nil {
		return err
	}
	if err := writeFileAtomic(d.path(sourceNameFile), []byte("SRT:"+name+"("+uri+")"), 0o644); err != nil {
		return err
	}

	st.Took = true
	st.Path = path
	st.LatencyMS = latencyMS
	st.TakenAt = time.Now().UTC().Format(time.RFC3339)
	if err := d.saveState(st); err != nil {
		return err
	}
	d.Log("decoder pointed at path %q (%s); restarting PPApp", path, uri)
	if err := d.Restart(ctx); err != nil {
		return fmt.Errorf("selection written but the decoder did not restart: %w", err)
	}
	return nil
}

// Release puts back whatever was selected before Take, and removes our entry
// from the OSD list. A no-op when we never took the display.
func (d *Decoder) Release(ctx context.Context) error {
	st, err := d.LoadState()
	if err != nil {
		return err
	}
	if !st.Took {
		return nil
	}
	l, err := d.readList()
	if err != nil {
		return err
	}
	kept := l.Streams[:0:0]
	for _, e := range l.Streams {
		if !strings.HasPrefix(e.Name, entryPrefix) {
			kept = append(kept, e)
		}
	}
	l.Streams = kept
	if err := d.writeList(l); err != nil {
		return err
	}

	settings, err := d.readSettings()
	if err != nil {
		return err
	}
	if st.PreviousSelection != "" {
		settings["SourceSelection"] = st.PreviousSelection
	} else {
		settings["SourceSelection"] = "NDI"
	}
	if err := d.writeSettings(settings); err != nil {
		return err
	}
	if st.PreviousSourceName != "" {
		if err := writeFileAtomic(d.path(sourceNameFile), []byte(st.PreviousSourceName), 0o644); err != nil {
			return err
		}
	} else {
		_ = os.Remove(d.path(sourceNameFile))
	}

	if err := d.saveState(DecoderState{}); err != nil {
		return err
	}
	d.Log("decoder restored to %q (%s); restarting PPApp",
		st.PreviousSourceName, settings["SourceSelection"])
	if err := d.Restart(ctx); err != nil {
		return fmt.Errorf("selection restored but the decoder did not restart: %w", err)
	}
	return nil
}

// Sync makes the decoder match the config: take the display path if one is
// set and it differs from what we hold, release it if none is set.
func (d *Decoder) Sync(ctx context.Context, c Config) error {
	if !c.Enabled || c.Display.Path == "" {
		return d.Release(ctx)
	}
	return d.Take(ctx, c.Display.Path, c.Display.LatencyMS)
}

// Current reports what the selection files say now, for the tab: whether the
// decoder is on one of our paths, and what the stock selection is otherwise.
type DecoderStatus struct {
	// Selection is SourceSelection from the decoder settings: NDI, SRT, CloudConnect.
	Selection string `json:"selection"`
	// SourceName is the raw selection line, e.g. "SRT:bdgw-live(127.0.0.1:8890?…)".
	SourceName string `json:"source_name"`
	// OnGateway is true when the selection names one of our entries.
	OnGateway bool `json:"on_gateway"`
	// Path is the gateway path the decoder is on, when OnGateway.
	Path string `json:"path,omitempty"`
	// Held is the persisted state: what we believe we took.
	Held DecoderState `json:"held"`
}

func (d *Decoder) Current() DecoderStatus {
	var s DecoderStatus
	settings, err := d.readSettings()
	if err == nil {
		if v, ok := settings["SourceSelection"].(string); ok {
			s.Selection = v
		}
	}
	s.SourceName = d.readSourceName()
	if strings.HasPrefix(s.SourceName, "SRT:"+entryPrefix) {
		rest := strings.TrimPrefix(s.SourceName, "SRT:"+entryPrefix)
		if i := strings.Index(rest, "("); i > 0 {
			s.Path = rest[:i]
			s.OnGateway = s.Selection == "SRT"
		}
	}
	s.Held, _ = d.LoadState()
	return s
}

// restartViaAPI asks the device's own API to restart the runner, the
// documented way to make a source change take, and falls back to systemctl
// when the API is not answering.
func (d *Decoder) restartViaAPI(ctx context.Context) error {
	client := &http.Client{Timeout: 15 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.API+"/restart", bytes.NewReader([]byte("{}")))
	if err == nil {
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode < 300 {
				// It takes a moment to come back and connect.
				time.Sleep(3 * time.Second)
				return nil
			}
			err = fmt.Errorf("/restart returned %s", resp.Status)
		}
		d.Log("the device API did not restart the decoder (%v); using systemctl", err)
	}
	out, err := exec.CommandContext(ctx, "systemctl", "restart", "BirdDogRunner").CombinedOutput()
	if err != nil {
		return errors.New(strings.TrimSpace(string(out)) + " (" + err.Error() + ")")
	}
	time.Sleep(3 * time.Second)
	return nil
}
