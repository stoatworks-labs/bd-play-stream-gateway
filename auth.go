package main

// Gating the API behind birdUI's own login.
//
// This is bdts's gate, carried over unchanged in its reasoning: the tab runs
// on the device's web UI origin but talks to this daemon on another port, and
// cookies are scoped by host and not by port, so the BirdDogSession cookie the
// browser already holds reaches us — provided the tab fetches with
// credentials and we answer with a concrete Access-Control-Allow-Origin plus
// Allow-Credentials.
//
// bdcam leaves its settings API open, on the grounds that the PLAY's own API
// on :8080 is. That would be wrong here: this config carries stream keys, and
// it can repoint the HDMI output. So reads of the config and every write need a
// valid birdUI session; only /api/status is open, and it holds no secrets.
//
// A session is validated by asking the thing that issued it: the System page
// is re-requested from the web UI on loopback with the caller's cookie, and
// the answer is either the page or the login screen. The check is
// differential — the page is probed without a cookie too, and if that also
// succeeds the device has no password and the gate refuses rather than
// pretending to have checked anything.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// SessionCookie is birddog-web-ui's session cookie name.
const SessionCookie = "BirdDogSession"

// probePath is the page a session is tested against. The System page gates
// the firmware upload, so whatever protects it is the standard to match.
const probePath = "/settings"

// settingsMarker is stock markup on the rendered System page that the login
// page does not carry; loginMarker is the reverse.
const (
	settingsMarker = "Reset System Default"
	loginMarker    = "div_login_box"
)

type Gate struct {
	UIBase string
	// AllowUnprotected turns the gate off, for a device with no birdUI password
	// on a network its owner trusts. The tab says loudly when it is on.
	AllowUnprotected bool
	HTTP             *http.Client

	mu             sync.Mutex
	unprotected    bool
	unprotectedAt  time.Time
	unprotectedTTL time.Duration
}

func NewGate(uiBase string) *Gate {
	return &Gate{
		UIBase: strings.TrimRight(uiBase, "/"),
		HTTP: &http.Client{
			Timeout: 5 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		unprotectedTTL: 30 * time.Second,
	}
}

type AuthState struct {
	Required        bool   `json:"required"`
	DeviceProtected bool   `json:"device_protected"`
	Authenticated   bool   `json:"authenticated"`
	Reason          string `json:"reason,omitempty"`
}

var (
	errNoSession      = errors.New("log in to birdUI first — this tab uses the same session as the rest of the web UI")
	errBadSession     = errors.New("your birdUI session has expired; reload the page and log in again")
	errUnprotected    = errors.New("this device has no birdUI password set, so streaming settings cannot be protected. Set a password in Password Settings on the System page, or start bdgw with --allow-unprotected")
	errUIUnreachable  = errors.New("cannot reach the birdUI login service to check your session")
	errUIUnexpectedly = errors.New("the birdUI page used to check sessions looked like neither the System page nor the login page — refusing to guess")
)

func (g *Gate) Check(ctx context.Context, r *http.Request) (AuthState, error) {
	if g.AllowUnprotected {
		return AuthState{Required: false, DeviceProtected: false, Authenticated: true}, nil
	}
	protected, err := g.deviceProtected(ctx)
	if err != nil {
		return AuthState{Required: true, Reason: err.Error()}, err
	}
	if !protected {
		return AuthState{Required: true, DeviceProtected: false, Reason: errUnprotected.Error()}, errUnprotected
	}
	cookie, err := r.Cookie(SessionCookie)
	if err != nil || cookie.Value == "" {
		return AuthState{Required: true, DeviceProtected: true, Reason: errNoSession.Error()}, errNoSession
	}
	ok, err := g.probe(ctx, cookie.Value)
	if err != nil {
		return AuthState{Required: true, DeviceProtected: true, Reason: err.Error()}, err
	}
	if !ok {
		return AuthState{Required: true, DeviceProtected: true, Reason: errBadSession.Error()}, errBadSession
	}
	return AuthState{Required: true, DeviceProtected: true, Authenticated: true}, nil
}

// Describe reports the gate's view without failing the request, so the tab
// can grey out controls before anyone clicks.
func (g *Gate) Describe(ctx context.Context, r *http.Request) AuthState {
	st, _ := g.Check(ctx, r)
	return st
}

func (g *Gate) deviceProtected(ctx context.Context) (bool, error) {
	g.mu.Lock()
	if time.Since(g.unprotectedAt) < g.unprotectedTTL {
		res := !g.unprotected
		g.mu.Unlock()
		return res, nil
	}
	g.mu.Unlock()

	authed, err := g.probe(ctx, "")
	if err != nil {
		return false, err
	}
	g.mu.Lock()
	g.unprotected = authed
	g.unprotectedAt = time.Now()
	g.mu.Unlock()
	return !authed, nil
}

func (g *Gate) probe(ctx context.Context, session string) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.UIBase+probePath, nil)
	if err != nil {
		return false, err
	}
	if session != "" {
		req.AddCookie(&http.Cookie{Name: SessionCookie, Value: session})
	}
	resp, err := g.HTTP.Do(req)
	if err != nil {
		return false, fmt.Errorf("%w: %v", errUIUnreachable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return false, nil
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("%w (HTTP %d)", errUIUnreachable, resp.StatusCode)
	}
	body, err := readLimited(resp.Body, 512*1024)
	if err != nil {
		return false, fmt.Errorf("%w: %v", errUIUnreachable, err)
	}
	switch {
	case strings.Contains(body, settingsMarker):
		return true, nil
	case strings.Contains(body, loginMarker):
		return false, nil
	default:
		return false, errUIUnexpectedly
	}
}

func readLimited(r io.Reader, n int64) (string, error) {
	b, err := io.ReadAll(io.LimitReader(r, n))
	return string(b), err
}
