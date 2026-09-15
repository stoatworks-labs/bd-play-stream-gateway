package main

// bdgw — the Streaming gateway panel for the BirdDog PLAY.
//
// MediaMTX does the streaming. This program does the three small jobs around
// it that make it a PLAY feature rather than a config file:
//
//   --render     write mediamtx.yml from config.json (bd-mtx.service's run.sh
//                calls this before starting MediaMTX)
//   --serve      answer the Streaming tab's API on its own port
//   --patch-ui   add the tab to the stock AV Setup page (and remove it again)
//
// and, on every save, point the PLAY's own decoder at whichever path should be
// on HDMI. See decoder.go for why the picture goes through PPApp rather than
// being drawn by us.

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"
)

// version is set by build.sh.
var version = "dev"

const (
	defaultDir     = "/userdata/bd-gw"
	defaultUIDir   = "/srv/birddog-web-ui"
	defaultUIBase  = "http://127.0.0.1"
	defaultPlayAPI = "http://127.0.0.1:8080"
	defaultMTXAPI  = "http://127.0.0.1:9997"
	defaultMTXUnit = "bd-mtx"
	defaultEtc     = "/etc"
)

func main() {
	var (
		serveAddr   = flag.String("serve", "", "serve the tab's API on this address (e.g. :8093)")
		render      = flag.Bool("render", false, "write mediamtx.yml from config.json and exit")
		printYML    = flag.Bool("print", false, "print the rendered mediamtx.yml to stdout and exit")
		dir         = flag.String("dir", defaultDir, "where config.json, mediamtx.yml and state.json live")
		configPath  = flag.String("config", "", "config file (default <dir>/config.json)")
		ymlPath     = flag.String("mtx-yml", "", "rendered MediaMTX config (default <dir>/mediamtx.yml)")
		statePath   = flag.String("state", "", "decoder state file (default <dir>/state.json)")
		patchUI     = flag.Bool("patch-ui", false, "add the Streaming tab to birdUI's AV Setup page")
		unpatchUI   = flag.Bool("unpatch-ui", false, "remove the tab, restoring the page")
		restoreUI   = flag.Bool("restore-ui", false, "restore the page from the backup taken before patching")
		uiDir       = flag.String("ui-dir", defaultUIDir, "the stock web UI's directory")
		uiBase      = flag.String("ui-base", defaultUIBase, "where the stock web UI answers, for session checks")
		apiPort     = flag.Int("api-port", defaultAPIPort, "port the tab calls; baked into the script at patch time")
		playAPI     = flag.String("play-api", defaultPlayAPI, "the PLAY's own REST API, for restarting the decoder")
		mtxAPI      = flag.String("mtx-api", defaultMTXAPI, "MediaMTX's control API")
		mtxUnit     = flag.String("mtx-unit", defaultMTXUnit, "systemd unit running MediaMTX")
		etc         = flag.String("etc", defaultEtc, "where the decoder's selection files live")
		allowUnprot = flag.Bool("allow-unprotected", false, "allow changes even when birdUI has no password set (see README)")
		showVersion = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}
	if *configPath == "" {
		*configPath = *dir + "/config.json"
	}
	if *ymlPath == "" {
		*ymlPath = *dir + "/mediamtx.yml"
	}
	if *statePath == "" {
		*statePath = *dir + "/state.json"
	}
	logf := func(format string, args ...any) { log.Printf(format, args...) }

	u := UIPaths{Dir: *uiDir}
	switch {
	case *patchUI:
		if err := ApplyPatch(u, *apiPort); err != nil {
			if errors.Is(err, errAlreadyPatched) {
				fmt.Println("AV Setup page already carries the Streaming tab; asset refreshed.")
				return
			}
			fatal(err)
		}
		fmt.Printf("patched %s\nwrote   %s\n", u.Template(), u.Asset())
		fmt.Println("birddog-web-ui must be restarted for this to appear (the unit is BirdDogWebUI).")
		return
	case *unpatchUI:
		if err := RemovePatch(u); err != nil {
			fatal(err)
		}
		fmt.Printf("removed the Streaming tab from %s\n", u.Template())
		return
	case *restoreUI:
		if err := RestoreBackup(u); err != nil {
			fatal(err)
		}
		fmt.Printf("restored %s from %s\n", u.Template(), u.Backup())
		return
	}

	if *printYML || *render {
		cfg, err := LoadConfig(*configPath)
		if err != nil {
			fatal(err)
		}
		if err := cfg.Validate(); err != nil {
			fatal(fmt.Errorf("%s: %w", *configPath, err))
		}
		if *printYML {
			fmt.Print(Render(cfg))
			return
		}
		// A first run has no config.json yet; write the defaults so the tab
		// and the renderer agree on what is running.
		if _, err := os.Stat(*configPath); os.IsNotExist(err) {
			if err := cfg.Save(*configPath); err != nil {
				fatal(err)
			}
		}
		if err := writeFileAtomic(*ymlPath, []byte(Render(cfg)), 0o644); err != nil {
			fatal(err)
		}
		if !cfg.Enabled {
			// run.sh reads this: exit 3 means "configured off, do not start".
			fmt.Fprintln(os.Stderr, "gateway is switched off in", *configPath)
			os.Exit(3)
		}
		fmt.Printf("wrote %s (%d paths)\n", *ymlPath, len(cfg.Paths))
		return
	}

	if *serveAddr == "" {
		flag.Usage()
		os.Exit(2)
	}

	gate := NewGate(*uiBase)
	gate.AllowUnprotected = *allowUnprot
	if *allowUnprot {
		fmt.Fprintln(os.Stderr, "WARNING: --allow-unprotected: anyone who can reach this port can reconfigure streaming and read stream keys")
	}
	dec := NewDecoder(*etc, *statePath, *playAPI)
	dec.Log = logf
	api := &APIServer{
		ConfigPath: *configPath,
		YMLPath:    *ymlPath,
		Gate:       gate,
		MTX:        NewMTX(*mtxAPI, *mtxUnit),
		Decoder:    dec,
		Version:    version,
		Log:        logf,
	}
	// Make sure the rendered file exists before the tab can be used, so a
	// box that was installed but never configured still starts the hub.
	if cfg, err := LoadConfig(*configPath); err == nil {
		if _, err := os.Stat(*ymlPath); os.IsNotExist(err) {
			_ = writeFileAtomic(*ymlPath, []byte(Render(cfg)), 0o644)
		}
	}
	srv := &http.Server{
		Addr:              *serveAddr,
		Handler:           api.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      90 * time.Second,
	}
	fmt.Printf("bdgw %s serving on %s (config=%s mtx=%s)\n", version, *serveAddr, *configPath, *mtxAPI)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
