package main

// Adding the "Streaming" tab to the PLAY's stock AV Setup page.
//
// videoset.html is a Go html/template that birddog-web-ui parses when it
// starts, so a malformed edit does not fail until the service restarts — and
// if it fails then, the firmware upload page goes with it. Hence, following
// bdcam and bdts:
//
//   * the patch lives here, in tested Go, rather than in sed in an installer;
//   * both insertions are wrapped in markers so removal is exact;
//   * patching is idempotent, so a re-install cannot double up;
//   * the anchors are the ones bdcam already relies on, verified unique in the
//     stock file, so the two tabs coexist whichever order they are applied in;
//   * a pristine backup is written before anything is touched;
//   * the template edit contains no template action; the whole tab lives in
//     the static JS, which is outside the template system entirely.

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

//go:embed web/streaming.js
var streamingJS string

const (
	patchStart = "<!-- bdgw-streaming-tab:start -->"
	patchEnd   = "<!-- bdgw-streaming-tab:end -->"

	// Both appear exactly once in the stock 1.0.30–1.0.34 videoset.html, and
	// both are what bdcam's UVC Converter tab anchors on. Each patch inserts
	// directly after the anchor, so the tabs end up side by side in whichever
	// order the installer applied them.
	anchorTabButton = `<button  id="tab1" class="dectablinks" onclick="opendecTab(event, 'dec1_form')">Decode Settings</button>`
	anchorContent   = `<div class="pl-3 pr-3 pb-3 pt-0" id="div_advanced_settings_content">`

	stockSuffix     = ".bdgw-stock"
	assetName       = "bdgw-streaming.js"
	portPlaceholder = "__BDGW_API_PORT__"
)

var errAlreadyPatched = errors.New("already patched")

// The tab button: same classes as the stock one, so opendecTab's show/hide
// and active-state handling apply for free.
const tabButtonHTML = patchStart +
	`<button id="tab_bdgw" class="dectablinks" onclick="opendecTab(event, 'bdgw_form')">Streaming</button>` +
	patchEnd

// tabContent is the tab body: empty, hidden, filled by the script. The script
// URL carries a content hash — browsers cache /static/ hard, and without it a
// change to the tab is invisible until someone force-reloads.
func tabContent(js string) string {
	return patchStart +
		`<div class="dectabcontent" id="bdgw_form" style="display:none;"></div>` +
		`<script src="/static/` + assetName + `?v=` + assetVersion(js) + `"></script>` +
		patchEnd
}

func assetVersion(js string) string {
	sum := sha256.Sum256([]byte(js))
	return hex.EncodeToString(sum[:4])
}

// renderAsset substitutes the port the daemon was configured with.
func renderAsset(port int) string {
	return strings.ReplaceAll(streamingJS, portPlaceholder, fmt.Sprint(port))
}

func IsPatched(src string) bool { return strings.Contains(src, patchStart) }

// PatchVideoset inserts the tab. Idempotent.
func PatchVideoset(src, js string) (string, error) {
	if IsPatched(src) {
		return src, nil
	}
	if n := strings.Count(src, anchorTabButton); n != 1 {
		return "", fmt.Errorf("expected exactly one decode-tab button to anchor to, found %d — this firmware's videoset.html differs from the one this patch was written for, so nothing was changed", n)
	}
	if n := strings.Count(src, anchorContent); n != 1 {
		return "", fmt.Errorf("expected exactly one settings-content div to anchor to, found %d — this firmware's videoset.html differs from the one this patch was written for, so nothing was changed", n)
	}
	out := strings.Replace(src, anchorTabButton, anchorTabButton+"\n\t\t\t\t"+tabButtonHTML, 1)
	out = strings.Replace(out, anchorContent, anchorContent+"\n\t\t\t\t\t\t"+tabContent(js), 1)
	return out, nil
}

// UnpatchVideoset removes every marked block, returning the file to stock —
// and leaves other people's markers (bdcam's, bdplay's) alone.
func UnpatchVideoset(src string) string {
	for {
		i := strings.Index(src, patchStart)
		if i < 0 {
			return src
		}
		j := strings.Index(src[i:], patchEnd)
		if j < 0 {
			return src // unterminated marker: hand-edited; leave it rather than eat the file
		}
		end := i + j + len(patchEnd)
		start := i
		for start > 0 && (src[start-1] == '\t' || src[start-1] == ' ') {
			start--
		}
		if start > 0 && src[start-1] == '\n' {
			start--
		}
		src = src[:start] + src[end:]
	}
}

type UIPaths struct {
	Dir string // /srv/birddog-web-ui
}

func (u UIPaths) Template() string { return filepath.Join(u.Dir, "videoset.html") }
func (u UIPaths) Backup() string   { return u.Template() + stockSuffix }
func (u UIPaths) Asset() string    { return filepath.Join(u.Dir, "static", assetName) }

// ApplyPatch writes the asset, then patches the template. The asset goes
// first: the other way round leaves a window where the page asks for a script
// that is not there, and a tab that renders as nothing is indistinguishable
// from a failed install.
func ApplyPatch(u UIPaths, port int) error {
	js := renderAsset(port)
	if err := writeFileAtomic(u.Asset(), []byte(js), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", u.Asset(), err)
	}
	raw, err := os.ReadFile(u.Template())
	if err != nil {
		return fmt.Errorf("reading %s: %w", u.Template(), err)
	}
	src := string(raw)
	// Keep a copy of the page as we found it, never overwriting an earlier
	// one — that copy is the only thing that is definitely pre-us.
	if _, err := os.Stat(u.Backup()); os.IsNotExist(err) {
		if werr := writeFileAtomic(u.Backup(), raw, 0o644); werr != nil {
			return fmt.Errorf("writing backup %s: %w", u.Backup(), werr)
		}
	}
	out, err := PatchVideoset(src, js)
	if err != nil {
		return err
	}
	if out == src {
		return errAlreadyPatched
	}
	return writeFileAtomic(u.Template(), []byte(out), 0o644)
}

// RemovePatch restores the template and deletes the asset.
func RemovePatch(u UIPaths) error {
	raw, err := os.ReadFile(u.Template())
	if err != nil {
		return fmt.Errorf("reading %s: %w", u.Template(), err)
	}
	out := UnpatchVideoset(string(raw))
	if err := writeFileAtomic(u.Template(), []byte(out), 0o644); err != nil {
		return err
	}
	if err := os.Remove(u.Asset()); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// RestoreBackup puts the pre-patch copy back — the installer's rollback.
func RestoreBackup(u UIPaths) error {
	raw, err := os.ReadFile(u.Backup())
	if err != nil {
		return fmt.Errorf("reading backup %s: %w", u.Backup(), err)
	}
	return writeFileAtomic(u.Template(), raw, 0o644)
}
