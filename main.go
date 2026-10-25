//go:build windows

// OpenListFS GUI — a Wails v2 desktop wrapper around the same mounter
// package the CLI uses. Closing the window hides to tray; the tray menu
// handles mount/unmount and quit. Flags:
//
//	--minimized  start hidden in the tray
//	--automount  mount the active profile at startup
package main

import (
	"context"
	"embed"
	"flag"
	"fmt"
	"os"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"openlistfs/internal/singleinstance"
)

//go:embed all:frontend/dist
var assets embed.FS

//go:embed assets/icon.ico
var trayIcon []byte

var (
	minimized          bool
	automountRequested bool
)

// Stable across releases and install paths. Tests use a separate build identity.
var instanceID = "OpenListFS-6afeb18d-92d7-43f8-998e-fb1cce559456"

func main() {
	flag.BoolVar(&minimized, "minimized", false, "start hidden in the system tray")
	flag.BoolVar(&automountRequested, "automount", false, "mount the active profile at startup")
	flag.Parse()
	instance, primary, err := singleinstance.Acquire(instanceID)
	if err != nil {
		fmt.Fprintln(os.Stderr, "single instance:", err)
		os.Exit(1)
	}
	if !primary {
		return
	}
	defer instance.Close()

	app := NewApp()
	err = wails.Run(&options.App{
		Title:            "OpenListFS",
		Width:            1100,
		Height:           820,
		MinWidth:         720,
		MinHeight:        560,
		Frameless:        true,
		AssetServer:      &assetserver.Options{Assets: assets},
		BackgroundColour: &options.RGBA{R: 25, G: 25, B: 25, A: 255},
		StartHidden:      minimized,
		OnStartup:        app.startup,
		OnDomReady:       func(ctx context.Context) { instance.Start(ctx, app.showWindow) },
		OnBeforeClose:    app.beforeClose,
		OnShutdown:       app.shutdown,
		Bind:             []interface{}{app},
	})
	if err != nil {
		fmt.Println("error:", err.Error())
	}
}
