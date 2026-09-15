package main

// The gateway's configuration: what the Streaming tab edits, and what
// mediamtx.yml is rendered from.
//
// It lives beside the binaries in /userdata/bd-gw/config.json. The panel
// process writes it; every save re-renders mediamtx.yml, which MediaMTX
// hot-reloads. Keeping our own model rather than editing the yml directly
// means the tab only ever exposes the handful of settings that make sense on a
// PLAY, and a typo in a URL is refused here with a message rather than by
// MediaMTX with a log line nobody will see.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Ports the hub listens on. Fixed rather than configurable: the birdUI tab
// prints them as the addresses to publish to, the installer documents them,
// and the patcher's page warns about them. None collide with BirdDog's own
// 80/8080, NDI's 5960+, or the fleet's 8090 (bd-cam-api), 8091 (bd-play) and
// 8092 (bdts). The panel itself takes 8093.
const (
	PortRTSP   = 8554
	PortRTMP   = 1935
	PortHLS    = 8888
	PortWebRTC = 8889
	PortSRT    = 8890
	// The control API stays on loopback; only bdgw talks to it.
	PortAPI = 9997

	defaultAPIPort = 8093
)

// pathNameRE is what MediaMTX accepts as a path name, narrowed to what is
// also safe as a DOM id in birdUI's stock JavaScript, which builds
// `$('#' + name)` selectors — so no colons, dots or slashes.
var pathNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// Path is one MediaMTX path: a name people publish to or that the hub pulls
// from, plus where it is forwarded.
type Path struct {
	Name string `json:"name"`
	// Label is free text for the tab ("OBS from the desk").
	Label string `json:"label,omitempty"`
	// Source is "publish" — anyone pushes to it over RTMP, RTSP, SRT or WHIP —
	// or a URL the hub pulls from. See sourceSchemes for what is accepted.
	Source string `json:"source"`
	// OnDemand pulls a URL source only while something is reading it — the
	// PLAY's decoder, or a client asking for it — so an idle camera costs no
	// bandwidth. Ignored for publish paths.
	OnDemand bool `json:"on_demand,omitempty"`
	// SDP is required for a udp+rtp:// source: MediaMTX cannot know the codec
	// of bare RTP without it.
	SDP string `json:"sdp,omitempty"`
	// HoldPicture keeps the path available while nothing is publishing, playing
	// an offline slate on repeat, so a reader pointed at an empty path gets a
	// picture rather than a refusal. Unverified against PPApp; see the README.
	HoldPicture bool `json:"hold_picture,omitempty"`
	// Forward pushes the stream on to other servers.
	Forward []Forward `json:"forward,omitempty"`
}

// Forward is one destination the hub pushes a path to.
type Forward struct {
	// ID is a short random token so the panel can name an entry without
	// carrying its URL back, which may hold a stream key.
	ID string `json:"id"`
	// URL is rtmp(s)://…#streamKey, rtsp(s)://…, srt://…?streamid=… or
	// whip(s)://…/whip. Stored in full; masked on the way out (see Masked).
	URL string `json:"url"`
	// Fingerprint validates a self-signed TLS certificate at the destination,
	// as a SHA-256 hex string.
	Fingerprint string `json:"fingerprint,omitempty"`
}

// Display is which path the PLAY's own decoder is pointed at.
type Display struct {
	// Path is a path name from Paths, or empty to leave the decoder alone.
	Path string `json:"path"`
	// LatencyMS is the SRT latency PPApp is asked for. The stock form's slider
	// runs 80–8000; on loopback the floor is plenty.
	LatencyMS int `json:"latency_ms"`
}

type Config struct {
	// Enabled starts MediaMTX at all. Off, and the unit exits without listening.
	Enabled bool    `json:"enabled"`
	Paths   []Path  `json:"paths"`
	Display Display `json:"display"`
	// CameraPath is the path bdcam is expected to publish to. It is just a
	// publish path like any other; naming it lets the tab print the exact SRT
	// URL for bdcam's settings and label the readers' URLs accordingly.
	CameraPath string `json:"camera_path,omitempty"`
}

// DefaultConfig is what a fresh install runs: two publish paths — one for
// anything pushed at the box, one for bdcam — and the decoder left alone.
func DefaultConfig() Config {
	return Config{
		Enabled: true,
		Paths: []Path{
			{Name: "live", Label: "Push here from OBS, vMix or an encoder", Source: "publish"},
			{Name: "cam", Label: "The UVC camera, published by bdcam", Source: "publish"},
		},
		Display:    Display{Path: "", LatencyMS: 120},
		CameraPath: "cam",
	}
}

func LoadConfig(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return DefaultConfig(), nil
		}
		return Config{}, err
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return Config{}, fmt.Errorf("%s is not valid JSON: %w", path, err)
	}
	if c.Display.LatencyMS == 0 {
		c.Display.LatencyMS = 120
	}
	return c, nil
}

// Save writes atomically. A half-written config read by the next render would
// take every path down at once.
func (c Config) Save(path string) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return writeFileAtomic(path, b, 0o600)
}

// sourceSchemes are the pull sources the tab offers. MediaMTX accepts more
// (rtsp+http, unix+mpegts, moqt, rpiCamera) but none of them apply to a PLAY.
var sourceSchemes = []string{
	"rtsp", "rtsps", "rtmp", "rtmps", "http", "https", "srt",
	"udp+mpegts", "udp+rtp", "whep", "wheps",
}

var forwardSchemes = []string{"rtmp", "rtmps", "rtsp", "rtsps", "srt", "whip", "whips"}

func hasScheme(raw string, schemes []string) (string, bool) {
	lower := strings.ToLower(raw)
	for _, s := range schemes {
		if strings.HasPrefix(lower, s+"://") {
			return s, true
		}
	}
	return "", false
}

// Validate refuses anything MediaMTX would refuse, and a few things it would
// accept but that make no sense here, so the tab can say why before saving.
func (c Config) Validate() error {
	seen := map[string]bool{}
	for i, p := range c.Paths {
		if !pathNameRE.MatchString(p.Name) {
			return fmt.Errorf("path %d: name %q must be 1–32 of a–z, 0–9, _ or -, starting with a letter or digit", i+1, p.Name)
		}
		if seen[p.Name] {
			return fmt.Errorf("two paths are named %q", p.Name)
		}
		seen[p.Name] = true
		if p.Name == "all_others" || p.Name == "all" {
			return fmt.Errorf("path %q is a name MediaMTX reserves", p.Name)
		}
		if err := validateSource(p); err != nil {
			return fmt.Errorf("path %q: %w", p.Name, err)
		}
		fids := map[string]bool{}
		for j, f := range p.Forward {
			if f.ID == "" {
				return fmt.Errorf("path %q: forward %d has no id", p.Name, j+1)
			}
			if fids[f.ID] {
				return fmt.Errorf("path %q: two forwards share id %s", p.Name, f.ID)
			}
			fids[f.ID] = true
			if err := validateForward(f); err != nil {
				return fmt.Errorf("path %q: forward %d: %w", p.Name, j+1, err)
			}
		}
	}
	if c.Display.Path != "" && !seen[c.Display.Path] {
		return fmt.Errorf("display path %q is not one of the configured paths", c.Display.Path)
	}
	if c.Display.LatencyMS < 80 || c.Display.LatencyMS > 8000 {
		return fmt.Errorf("display latency must be 80–8000 ms (the decoder's own range), got %d", c.Display.LatencyMS)
	}
	if c.CameraPath != "" && !seen[c.CameraPath] {
		return fmt.Errorf("camera path %q is not one of the configured paths", c.CameraPath)
	}
	return nil
}

func validateSource(p Path) error {
	src := strings.TrimSpace(p.Source)
	if src == "" || src == "publish" || src == "publisher" {
		if p.SDP != "" {
			return errors.New("an SDP only applies to a udp+rtp:// source")
		}
		return nil
	}
	scheme, ok := hasScheme(src, sourceSchemes)
	if !ok {
		return fmt.Errorf("source must be \"publish\" or a URL starting with one of %s, got %q",
			strings.Join(sourceSchemes, "://, ")+"://", src)
	}
	u, err := url.Parse(src)
	if err != nil {
		return fmt.Errorf("source is not a valid URL: %v", err)
	}
	if u.Host == "" {
		return errors.New("source URL has no host")
	}
	if strings.ContainsAny(src, "\n\r\t\"") {
		return errors.New("source URL contains a control or quote character")
	}
	switch scheme {
	case "udp+rtp":
		if strings.TrimSpace(p.SDP) == "" {
			return errors.New("a udp+rtp:// source needs an SDP describing the stream")
		}
		if !strings.HasPrefix(strings.TrimSpace(p.SDP), "v=0") {
			return errors.New("the SDP must start with v=0")
		}
	case "udp+mpegts":
		if _, _, err := net.SplitHostPort(u.Host); err != nil {
			return errors.New("a udp+mpegts:// source needs ip:port")
		}
	case "srt":
		if u.Port() == "" {
			return errors.New("an srt:// source needs an explicit port")
		}
	case "http", "https":
		// Anything HLS-shaped is fine; MediaMTX decides from the playlist.
	}
	if p.SDP != "" && scheme != "udp+rtp" {
		return errors.New("an SDP only applies to a udp+rtp:// source")
	}
	return nil
}

func validateForward(f Forward) error {
	dst := strings.TrimSpace(f.URL)
	scheme, ok := hasScheme(dst, forwardSchemes)
	if !ok {
		return fmt.Errorf("destination must start with one of %s, got %q",
			strings.Join(forwardSchemes, "://, ")+"://", dst)
	}
	if strings.ContainsAny(dst, "\n\r\t\"") {
		return errors.New("destination contains a control or quote character")
	}
	u, err := url.Parse(dst)
	if err != nil {
		return fmt.Errorf("destination is not a valid URL: %v", err)
	}
	if u.Host == "" {
		return errors.New("destination has no host")
	}
	if scheme == "srt" && u.Port() == "" {
		return errors.New("an srt:// destination needs an explicit port")
	}
	if f.Fingerprint != "" {
		fp := strings.ToLower(strings.ReplaceAll(f.Fingerprint, ":", ""))
		if _, err := hex.DecodeString(fp); err != nil || len(fp) != 64 {
			return errors.New("fingerprint must be the SHA-256 of the certificate as 64 hex digits")
		}
	}
	return nil
}

// IsPublish reports whether a path takes a publisher rather than pulling.
func (p Path) IsPublish() bool {
	s := strings.TrimSpace(p.Source)
	return s == "" || s == "publish" || s == "publisher"
}

// NewForwardID makes the token a forward entry is addressed by.
func NewForwardID() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "f" + strconv.FormatInt(int64(os.Getpid()), 36)
	}
	return hex.EncodeToString(b[:])
}

// Masked hides the parts of a URL that are secrets: userinfo, and anything
// after '#', which is where RTMP carries the stream key. What is left still
// identifies the destination to a person looking at the tab.
func Masked(raw string) string {
	s := raw
	if i := strings.Index(s, "#"); i >= 0 {
		key := s[i+1:]
		tail := key
		if len(tail) > 4 {
			tail = tail[len(tail)-4:]
		}
		if key == "" {
			s = s[:i+1]
		} else {
			s = s[:i+1] + "••••" + tail
		}
	}
	if u, err := url.Parse(s); err == nil && u.User != nil {
		name := u.User.Username()
		if _, hasPass := u.User.Password(); hasPass {
			u.User = url.UserPassword(name, "••••")
		}
		s = u.String()
	}
	// Query strings can carry an SRT passphrase.
	if i := strings.Index(s, "passphrase="); i >= 0 {
		j := strings.IndexAny(s[i:], "&#")
		if j < 0 {
			s = s[:i] + "passphrase=••••"
		} else {
			s = s[:i] + "passphrase=••••" + s[i+j:]
		}
	}
	return s
}

// Public is what the API hands to the tab: the config with every secret
// masked. Forward entries keep their ids so an edit can address them.
func (c Config) Public() Config {
	out := c
	out.Paths = make([]Path, len(c.Paths))
	for i, p := range c.Paths {
		q := p
		q.Source = Masked(p.Source)
		q.Forward = make([]Forward, len(p.Forward))
		for j, f := range p.Forward {
			g := f
			g.URL = Masked(f.URL)
			q.Forward[j] = g
		}
		out.Paths[i] = q
	}
	return out
}

// Merge takes an edit from the tab and reconciles it with what is stored: a
// source or destination that comes back in its masked form means "unchanged",
// so the tab never has to hold a stream key in the page. New forward entries
// (no id, or an unknown id) get fresh ids.
func Merge(stored, edit Config) Config {
	storedPaths := map[string]Path{}
	for _, p := range stored.Paths {
		storedPaths[p.Name] = p
	}
	out := edit
	out.Paths = make([]Path, len(edit.Paths))
	for i, p := range edit.Paths {
		q := p
		old, had := storedPaths[p.Name]
		if had && q.Source == Masked(old.Source) {
			q.Source = old.Source
		}
		oldFwd := map[string]Forward{}
		if had {
			for _, f := range old.Forward {
				oldFwd[f.ID] = f
			}
		}
		q.Forward = make([]Forward, len(p.Forward))
		for j, f := range p.Forward {
			g := f
			if o, ok := oldFwd[f.ID]; ok && f.ID != "" {
				if g.URL == Masked(o.URL) || strings.TrimSpace(g.URL) == "" {
					g.URL = o.URL
				}
			} else {
				g.ID = NewForwardID()
			}
			q.Forward[j] = g
		}
		out.Paths[i] = q
	}
	return out
}

// writeFileAtomic writes via a temp file in the same directory and renames, so
// a power cut mid-write cannot leave a half-written file behind.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, mode); err != nil {
		return err
	}
	return os.Rename(name, path)
}
