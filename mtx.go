package main

// Reading MediaMTX's state through its control API, and driving its unit.
//
// The API is on loopback only (see Render). This asks it one question — which
// paths exist, whether each has a source and what codecs it carries — so the
// tab can show "OBS is connected, H264 + AAC, two readers" rather than making
// people read a log.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"time"
)

type MTX struct {
	// API is the control API's base URL.
	API string
	// Unit is the systemd unit running MediaMTX.
	Unit string
	HTTP *http.Client
	// Run executes a systemctl command; tests replace it.
	Run func(ctx context.Context, args ...string) (string, error)
	// Journal returns the last n lines of the unit's journal; tests replace it.
	Journal func(ctx context.Context, n int) (string, error)
}

func NewMTX(api, unit string) *MTX {
	return &MTX{
		API:  strings.TrimRight(api, "/"),
		Unit: unit,
		HTTP: &http.Client{Timeout: 4 * time.Second},
		Run: func(ctx context.Context, args ...string) (string, error) {
			out, err := exec.CommandContext(ctx, "systemctl", args...).CombinedOutput()
			return strings.TrimSpace(string(out)), err
		},
		Journal: func(ctx context.Context, n int) (string, error) {
			out, err := exec.CommandContext(ctx, "journalctl", "--no-pager", "-o", "cat",
				"-n", fmt.Sprint(n), "-u", unit).CombinedOutput()
			return strings.TrimSpace(string(out)), err
		},
	}
}

// PathStatus is the subset of MediaMTX's path record the tab shows.
type PathStatus struct {
	Name string `json:"name"`
	// Online is true once a source is delivering.
	Online bool `json:"online"`
	// SourceType names what is feeding it: rtmpConn, rtspSource, srtConn…
	SourceType string   `json:"source_type,omitempty"`
	Tracks     []string `json:"tracks,omitempty"`
	Readers    int      `json:"readers"`
	// ReaderTypes counts readers by kind, e.g. {"srtConn": 1, "hlsSession": 2}.
	ReaderTypes   map[string]int `json:"reader_types,omitempty"`
	InboundBytes  uint64         `json:"inbound_bytes"`
	OutboundBytes uint64         `json:"outbound_bytes"`
}

// wire is MediaMTX's own shape (api/openapi.yaml, schema Path). Deprecated
// fields are still populated by this version and are read as a fallback so an
// older or newer binary degrades to "less detail" rather than "no status".
type wirePathList struct {
	Items []struct {
		Name   string `json:"name"`
		Online *bool  `json:"online"`
		Ready  *bool  `json:"ready"`
		Source *struct {
			Type string `json:"type"`
		} `json:"source"`
		Tracks  []string `json:"tracks"`
		Tracks2 []struct {
			Codec string `json:"codec"`
		} `json:"tracks2"`
		Readers []struct {
			Type string `json:"type"`
		} `json:"readers"`
		InboundBytes  uint64 `json:"inboundBytes"`
		OutboundBytes uint64 `json:"outboundBytes"`
		BytesReceived uint64 `json:"bytesReceived"`
		BytesSent     uint64 `json:"bytesSent"`
	} `json:"items"`
}

// Paths lists what MediaMTX knows. An error means the API is not answering,
// which the tab reports as "gateway not running".
func (m *MTX) Paths(ctx context.Context) ([]PathStatus, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.API+"/v3/paths/list?itemsPerPage=200", nil)
	if err != nil {
		return nil, err
	}
	resp, err := m.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("control API returned %s", resp.Status)
	}
	var w wirePathList
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&w); err != nil {
		return nil, fmt.Errorf("control API: %w", err)
	}
	out := make([]PathStatus, 0, len(w.Items))
	for _, it := range w.Items {
		p := PathStatus{Name: it.Name}
		switch {
		case it.Online != nil:
			p.Online = *it.Online
		case it.Ready != nil:
			p.Online = *it.Ready
		}
		if it.Source != nil {
			p.SourceType = it.Source.Type
		}
		for _, t := range it.Tracks2 {
			p.Tracks = append(p.Tracks, t.Codec)
		}
		if len(p.Tracks) == 0 {
			p.Tracks = it.Tracks
		}
		p.Readers = len(it.Readers)
		if p.Readers > 0 {
			p.ReaderTypes = map[string]int{}
			for _, r := range it.Readers {
				p.ReaderTypes[r.Type]++
			}
		}
		p.InboundBytes = it.InboundBytes
		if p.InboundBytes == 0 {
			p.InboundBytes = it.BytesReceived
		}
		p.OutboundBytes = it.OutboundBytes
		if p.OutboundBytes == 0 {
			p.OutboundBytes = it.BytesSent
		}
		out = append(out, p)
	}
	return out, nil
}

// Active reports whether the unit is running.
func (m *MTX) Active(ctx context.Context) bool {
	out, err := m.Run(ctx, "is-active", m.Unit)
	return err == nil && strings.TrimSpace(out) == "active"
}

// Restart bounces the unit. Used only when the hub is switched on or off:
// a config change while it runs is picked up by MediaMTX's own hot reload
// without dropping anyone.
func (m *MTX) Restart(ctx context.Context) error {
	out, err := m.Run(ctx, "restart", m.Unit)
	if err != nil {
		return fmt.Errorf("systemctl restart %s: %s (%v)", m.Unit, out, err)
	}
	return nil
}

func (m *MTX) Stop(ctx context.Context) error {
	out, err := m.Run(ctx, "stop", m.Unit)
	if err != nil {
		return fmt.Errorf("systemctl stop %s: %s (%v)", m.Unit, out, err)
	}
	return nil
}
