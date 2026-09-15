package main

import (
	"html/template"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The AV Setup pages the patch is tested against.
//
// The synthetic page below ships with the repo; it is written here, not copied
// from a device, because the real template is BirdDog's copyrighted UI source
// and this repo is public — the same line bdts and bd-play-usb-player draw. It
// reproduces the structure the patch depends on: the decode-settings tab bar
// and the content div, inside the {{define}} block the page renders.
//
// Any real firmware pages dropped into testdata/firmware/ (gitignored) as
// videoset.<version>.html are tested too.
const syntheticVideoset = `{{template "layout.html" .}}
{{define "videoset"}}
<div class="row m-0 p-0 row_in_page cm_h_lg_auto">
    <div class="col-12 m-0 p-2">
        <div class="div_content_box back_gray_color">
		  {{if or (eq .HWVersion "BirdDog 4K QUAD") (eq .HWVersion "BirdDog 4K GEAR")}}
		 <div class="tab">
		 <button {{if or (ne .Mode "decode")}} style="display:none;" {{end}} id="tab1" class="dectablinks" onclick="opendecTab(event, 'dec1_form')">Decode-1 Settings</button>
		 </div>
		{{else}}
            <div class="tab">
                <button  id="tab1" class="dectablinks" onclick="opendecTab(event, 'dec1_form')">Decode Settings</button>
            </div>
        {{end}}
            <div class="div_box_content">
                <div class="row m-0 p-0 cm_h_lg_100">
                    <div class="col-12 m-0 p-0 v_center_parent">
                        <div class="pl-3 pr-3 pb-3 pt-0" id="div_advanced_settings_content">
                            <form class="dectabcontent" id="dec1_form" method="post" action="/videoset">
                                <select id="Source_Selection" onchange='this.form.submit();' name="SourceSelection">
                                    <option value="NDI" {{if eq .Source_Selection "NDI"}} selected {{end}}>NDI</option>
                                    <option value="SRT" {{if eq .Source_Selection "SRT"}} selected  {{end}} >SRT</option>
                                </select>
                            </form>
                        </div>
                    </div>
                </div>
            </div>
        </div>
    </div>
</div>
<script>
    function opendecTab(evt, target) {
        $("#div_advanced_settings_content .div_tab_contents").hide();
        $("#div_advanced_settings_content #"+target).show();
    }
</script>
{{end}}`

// bdcamPatched is the same page after bdcam's UVC Converter tab has been
// applied — its markers and anchors are reproduced from bdcam's patch.go, so
// this test breaks if either project changes how it anchors.
func bdcamPatched(src string) string {
	const start, end = "<!-- bdcam-uvc-tab:start -->", "<!-- bdcam-uvc-tab:end -->"
	btn := start + `<button id="tab_uvc" class="dectablinks" onclick="opendecTab(event, 'uvc_form')">UVC Converter</button>` + end
	body := start + `<div class="dectabcontent" id="uvc_form" style="display:none;"></div><script src="/static/uvc-converter.js?v=abcd1234"></script>` + end
	out := strings.Replace(src, anchorTabButton, anchorTabButton+"\n\t\t\t\t"+btn, 1)
	return strings.Replace(out, anchorContent, anchorContent+"\n\t\t\t\t\t\t"+body, 1)
}

func pages(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{
		"synthetic/stock":         syntheticVideoset,
		"synthetic/bdcam-applied": bdcamPatched(syntheticVideoset),
	}
	matches, _ := filepath.Glob("testdata/firmware/videoset.*.html")
	for _, m := range matches {
		b, err := os.ReadFile(m)
		if err != nil {
			t.Fatal(err)
		}
		name := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(m), "videoset."), ".html")
		out["firmware/"+name] = string(b)
		out["firmware/"+name+"/bdcam-applied"] = bdcamPatched(string(b))
	}
	if len(matches) == 0 {
		t.Log("no real firmware pages in testdata/firmware/ — testing the synthetic page only. " +
			"Copy a device's /srv/birddog-web-ui/videoset.html there to test the genuine markup.")
	}
	return out
}

func eachPage(t *testing.T, fn func(t *testing.T, src string)) {
	t.Helper()
	for name, src := range pages(t) {
		t.Run(name, func(t *testing.T) { fn(t, src) })
	}
}

func TestAnchorsAreUniqueInEveryPage(t *testing.T) {
	eachPage(t, func(t *testing.T, src string) {
		if n := strings.Count(src, anchorTabButton); n != 1 {
			t.Fatalf("tab anchor appears %d times, want 1", n)
		}
		if n := strings.Count(src, anchorContent); n != 1 {
			t.Fatalf("content anchor appears %d times, want 1", n)
		}
	})
}

func TestPatchIsIdempotentAndExactlyReversible(t *testing.T) {
	js := renderAsset(8093)
	eachPage(t, func(t *testing.T, src string) {
		once, err := PatchVideoset(src, js)
		if err != nil {
			t.Fatal(err)
		}
		if once == src {
			t.Fatal("patch changed nothing")
		}
		twice, err := PatchVideoset(once, js)
		if err != nil {
			t.Fatal(err)
		}
		if twice != once {
			t.Fatal("patching twice changed the file again")
		}
		if got := UnpatchVideoset(once); got != src {
			t.Fatal("unpatch did not restore the page byte for byte")
		}
		if strings.Count(once, patchStart) != 2 || strings.Count(once, patchEnd) != 2 {
			t.Fatal("expected exactly two marked blocks")
		}
	})
}

func TestPatchCoexistsWithBdcamInEitherOrder(t *testing.T) {
	js := renderAsset(8093)
	stock := syntheticVideoset

	// bdcam first, then us.
	a, err := PatchVideoset(bdcamPatched(stock), js)
	if err != nil {
		t.Fatal(err)
	}
	// Us first, then bdcam.
	ours, err := PatchVideoset(stock, js)
	if err != nil {
		t.Fatal(err)
	}
	b := bdcamPatched(ours)

	for name, page := range map[string]string{"bdcam-then-us": a, "us-then-bdcam": b} {
		if !strings.Contains(page, `id="tab_uvc"`) || !strings.Contains(page, `id="tab_bdgw"`) {
			t.Fatalf("%s: a tab is missing", name)
		}
		if !strings.Contains(page, `id="uvc_form"`) || !strings.Contains(page, `id="bdgw_form"`) {
			t.Fatalf("%s: a tab body is missing", name)
		}
		// Removing ours leaves bdcam's exactly as it was.
		got := UnpatchVideoset(page)
		if strings.Contains(got, patchStart) {
			t.Fatalf("%s: our markers survived unpatch", name)
		}
		if !strings.Contains(got, "bdcam-uvc-tab:start") {
			t.Fatalf("%s: unpatch removed bdcam's tab", name)
		}
	}
	if UnpatchVideoset(a) != bdcamPatched(stock) {
		t.Fatal("bdcam-then-us: unpatch did not give back bdcam's page byte for byte")
	}
}

func TestPatchAddsNoTemplateActions(t *testing.T) {
	js := renderAsset(8093)
	eachPage(t, func(t *testing.T, src string) {
		out, err := PatchVideoset(src, js)
		if err != nil {
			t.Fatal(err)
		}
		for _, tok := range []string{"{{", "}}"} {
			if b, a := strings.Count(src, tok), strings.Count(out, tok); b != a {
				t.Fatalf("%q count changed from %d to %d", tok, b, a)
			}
		}
	})
}

// The test this file exists for: birddog-web-ui parses the page with
// html/template at startup, and a parse error takes the whole web UI down.
func TestPatchedTemplateStillParses(t *testing.T) {
	js := renderAsset(8093)
	eachPage(t, func(t *testing.T, src string) {
		out, err := PatchVideoset(src, js)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := template.New("videoset.html").Parse(out); err != nil {
			t.Fatalf("patched page does not parse: %v", err)
		}
		at := strings.Index(out, patchStart)
		end := strings.LastIndex(out, "{{end}}")
		if at < 0 || end < 0 || at > end {
			t.Fatal("patch landed outside the define block")
		}
	})
}

func TestUnterminatedMarkerIsLeftAlone(t *testing.T) {
	src := "a\n" + patchStart + "half-edited by hand\n"
	if got := UnpatchVideoset(src); got != src {
		t.Fatal("an unterminated block should be left as is, not eaten to the end of the file")
	}
}

func TestApplyAndRemoveOnDisk(t *testing.T) {
	dir := t.TempDir()
	tpl := filepath.Join(dir, "videoset.html")
	if err := os.WriteFile(tpl, []byte(syntheticVideoset), 0o644); err != nil {
		t.Fatal(err)
	}
	u := UIPaths{Dir: dir}
	if err := ApplyPatch(u, 8093); err != nil {
		t.Fatal(err)
	}
	asset, err := os.ReadFile(u.Asset())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(asset), "var API_PORT = 8093;") || strings.Contains(string(asset), portPlaceholder) {
		t.Fatal("port not substituted into the asset")
	}
	backup, err := os.ReadFile(u.Backup())
	if err != nil || string(backup) != syntheticVideoset {
		t.Fatal("backup missing or not pristine")
	}
	if err := ApplyPatch(u, 8093); err != errAlreadyPatched {
		t.Fatalf("second apply: got %v, want errAlreadyPatched", err)
	}
	if err := RemovePatch(u); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(tpl)
	if string(got) != syntheticVideoset {
		t.Fatal("remove did not restore the page")
	}
	if _, err := os.Stat(u.Asset()); !os.IsNotExist(err) {
		t.Fatal("asset not removed")
	}
	// RestoreBackup works even after a hand edit.
	_ = os.WriteFile(tpl, []byte("garbage"), 0o644)
	if err := RestoreBackup(u); err != nil {
		t.Fatal(err)
	}
	got, _ = os.ReadFile(tpl)
	if string(got) != syntheticVideoset {
		t.Fatal("restore did not put the backup back")
	}
}

func TestAssetVersionTracksContent(t *testing.T) {
	if assetVersion("a") == assetVersion("b") {
		t.Fatal("different scripts must get different cache-busters")
	}
	if !strings.Contains(tabContent(renderAsset(8093)), "?v="+assetVersion(renderAsset(8093))) {
		t.Fatal("script URL does not carry the asset version")
	}
}
