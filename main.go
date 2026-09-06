//go:build windows

// OpenListFS GUI — a Wails v2 desktop wrapper around the same mounter
// package the CLI uses. Closing the window hides to tray; the tray menu
// handles mount/unmount and quit. Flags:
//
//	--minimized  start hidden in the tray
//	--automount  mount the active profile at startup
package main

import (
	"embed"
	"flag"
	"fmt"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
)

//go:embed all:frontend/dist
var assets embed.FS

//go:embed assets/icon.ico
var trayIcon []byte

var (
	minimized          bool
	automountRequested bool
)

func main() {
	flag.BoolVar(&minimized, "minimized", false, "start hidden in the system tray")
	flag.BoolVar(&automountRequested, "automount", false, "mount the active profile at startup")
	flag.Parse()

	app := NewApp()
	err := wails.Run(&options.App{
		Title:            "OpenListFS (read-only PoC)",
		Width:            960,
		Height:           760,
		MinWidth:         720,
		MinHeight:        560,
		AssetServer:      &assetserver.Options{Assets: assets},
		BackgroundColour: &options.RGBA{R: 24, G: 26, B: 32, A: 255},
		StartHidden:      minimized,
		OnStartup:        app.startup,
		OnBeforeClose:    app.beforeClose,
		Bind:             []interface{}{app},
	})
	if err != nil {
		fmt.Println("error:", err.Error())
	}
}
