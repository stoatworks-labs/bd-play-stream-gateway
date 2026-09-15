package main

// The API behind the Streaming tab.
//
// Runs as its own process under bd-gw.service, separate from MediaMTX under
// bd-mtx.service, so a stopped or wedged hub still leaves a page that can say
// so — the same split bdcam and bdts make.
//
// /api/status is open and carries no secrets. Everything else needs a birdUI
// session (auth.go), because the config holds stream keys and can repoint the
// HDMI output.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const requestTimeout = 20 * time.Second

type APIServer struct {
	ConfigPath string
	YMLPath    string
	Gate       *Gate
	MTX        *MTX
	Decoder    *Decoder
	Version    string
	Log        func(format string, args ...any)
}

func (a *APIServer) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/status", a.handleStatus)
	mux.HandleFunc("/api/config", a.gated(a.handleConfig))
	mux.HandleFunc("/api/display", a.gated(a.handleDisplay))
	mux.HandleFunc("/api/log", a.gated(a.handleLog))
	return cors(mux)
}

// cors echoes the caller's origin and allows credentials: a wildcard origin
// may not carry cookies, and the session cookie is what authorises a write.
// Writes are JSON POSTs, so they are never simple requests and always face a
// preflight.
func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Add("Vary", "Origin")
		}
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

// gated wraps a handler with the session check.
func (a *APIServer) gated(h func(http.ResponseWriter, *http.Request)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
		defer cancel()
		if _, err := a.Gate.Check(ctx, r); err != nil {
			// 403 rather than 401: a WWW-Authenticate challenge would make the
			// browser show its own basic-auth dialog.
			writeErr(w, http.StatusForbidden, err)
			return
		}
		h(w, r)
	}
}

type statusResponse struct {
	Version    string         `json:"version"`
	MediaMTX   string         `json:"mediamtx_version"`
	Enabled    bool           `json:"enabled"`
	HubActive  bool           `json:"hub_active"`
	HubAPI     bool           `json:"hub_api"`
	Paths      []PathStatus   `json:"paths"`
	Configured []pathSummary  `json:"configured"`
	Display    Display        `json:"display"`
	CameraPath string         `json:"camera_path"`
	Decoder    DecoderStatus  `json:"decoder"`
	Ports      map[string]int `json:"ports"`
	Auth       AuthState      `json:"auth"`
	Error      string         `json:"error,omitempty"`
}

// pathSummary is the secret-free part of a configured path, so the open
// status call can list what exists without exposing URLs.
type pathSummary struct {
	Name       string `json:"name"`
	Label      string `json:"label,omitempty"`
	Publish    bool   `json:"publish"`
	Forwards   int    `json:"forwards"`
	OnDemand   bool   `json:"on_demand"`
	HoldPic    bool   `json:"hold_picture"`
	SourceKind string `json:"source_kind"`
}

func summarize(p Path) pathSummary {
	kind := "publish"
	if !p.IsPublish() {
		if s, ok := hasScheme(strings.TrimSpace(p.Source), sourceSchemes); ok {
			kind = s
		} else {
			kind = "url"
		}
	}
	return pathSummary{
		Name: p.Name, Label: p.Label, Publish: p.IsPublish(),
		Forwards: len(p.Forward), OnDemand: p.OnDemand, HoldPic: p.HoldPicture,
		SourceKind: kind,
	}
}

func (a *APIServer) handleStatus(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	resp := statusResponse{
		Version:  a.Version,
		MediaMTX: MediaMTXVersion,
		Ports: map[string]int{
			"rtsp": PortRTSP, "rtmp": PortRTMP, "hls": PortHLS,
			"webrtc": PortWebRTC, "srt": PortSRT, "panel": defaultAPIPort,
		},
		Auth: a.Gate.Describe(ctx, r),
	}
	cfg, err := LoadConfig(a.ConfigPath)
	if err != nil {
		resp.Error = err.Error()
		writeJSON(w, http.StatusOK, resp)
		return
	}
	resp.Enabled = cfg.Enabled
	resp.Display = cfg.Display
	resp.CameraPath = cfg.CameraPath
	for _, p := range cfg.Paths {
		resp.Configured = append(resp.Configured, summarize(p))
	}
	resp.HubActive = a.MTX.Active(ctx)
	if paths, err := a.MTX.Paths(ctx); err == nil {
		resp.HubAPI = true
		resp.Paths = paths
	} else if cfg.Enabled {
		resp.Error = "The gateway's control API is not answering: " + err.Error()
	}
	resp.Decoder = a.Decoder.Current()
	writeJSON(w, http.StatusOK, resp)
}

func (a *APIServer) handleConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		cfg, err := LoadConfig(a.ConfigPath)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"config":             cfg.Public(),
			"camera_publish_url": CameraPublishURL(cfg.CameraPath),
		})

	case http.MethodPost:
		var edit Config
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10)).Decode(&edit); err != nil {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("could not read the settings: %w", err))
			return
		}
		stored, err := LoadConfig(a.ConfigPath)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		merged := Merge(stored, edit)
		if err := merged.Validate(); err != nil {
			// 422 rather than 400: the JSON parsed, the settings are the problem.
			writeErr(w, http.StatusUnprocessableEntity, err)
			return
		}
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 60*time.Second)
		defer cancel()
		applied, warnings, err := a.apply(ctx, stored, merged)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"config":             merged.Public(),
			"camera_publish_url": CameraPublishURL(merged.CameraPath),
			"applied":            applied,
			"warnings":           warnings,
		})

	default:
		writeErr(w, http.StatusMethodNotAllowed, errors.New("use GET or POST"))
	}
}

// handleDisplay switches what the decoder shows without resending the rest
// of the config. {"path": "live"} takes it; {"path": ""} gives it back.
func (a *APIServer) handleDisplay(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, errors.New("POST only"))
		return
	}
	var req struct {
		Path      string `json:"path"`
		LatencyMS int    `json:"latency_ms"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("could not read the request: %w", err))
		return
	}
	stored, err := LoadConfig(a.ConfigPath)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	next := stored
	next.Display.Path = strings.TrimSpace(req.Path)
	if req.LatencyMS > 0 {
		next.Display.LatencyMS = req.LatencyMS
	}
	if err := next.Validate(); err != nil {
		writeErr(w, http.StatusUnprocessableEntity, err)
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 60*time.Second)
	defer cancel()
	applied, warnings, err := a.apply(ctx, stored, next)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"display":  next.Display,
		"applied":  applied,
		"warnings": warnings,
	})
}

// apply saves the config, renders the hub's file, and brings the hub and the
// decoder into line with it. It returns what it did, in words the tab shows.
func (a *APIServer) apply(ctx context.Context, before, after Config) (applied []string, warnings []string, err error) {
	if err := after.Save(a.ConfigPath); err != nil {
		return nil, nil, fmt.Errorf("saving the settings: %w", err)
	}
	applied = append(applied, "settings saved")

	if err := writeFileAtomic(a.YMLPath, []byte(Render(after)), 0o644); err != nil {
		return nil, nil, fmt.Errorf("writing mediamtx.yml: %w", err)
	}
	applied = append(applied, "mediamtx.yml rendered")

	switch {
	case after.Enabled && !before.Enabled:
		if err := a.MTX.Restart(ctx); err != nil {
			warnings = append(warnings, err.Error())
		} else {
			applied = append(applied, "gateway started")
		}
	case !after.Enabled && before.Enabled:
		if err := a.MTX.Stop(ctx); err != nil {
			warnings = append(warnings, err.Error())
		} else {
			applied = append(applied, "gateway stopped")
		}
	case after.Enabled:
		// MediaMTX watches its file and reloads without dropping clients.
		applied = append(applied, "gateway reloads the new file itself")
	}

	// The decoder is only touched when the display selection changes: every
	// restart of the runner is one more than the box would like.
	if before.Display != after.Display || before.Enabled != after.Enabled {
		if err := a.Decoder.Sync(ctx, after); err != nil {
			warnings = append(warnings, "decoder: "+err.Error())
		} else if after.Enabled && after.Display.Path != "" {
			applied = append(applied, fmt.Sprintf("HDMI now follows path %q (the decoder restarted)", after.Display.Path))
		} else if before.Display.Path != "" {
			applied = append(applied, "HDMI returned to the previous source (the decoder restarted)")
		}
	}
	return applied, warnings, nil
}

// handleLog returns the tail of the hub's journal.
func (a *APIServer) handleLog(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()
	out, err := a.MTX.Journal(ctx, 150)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"log": out, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"log": out})
}
