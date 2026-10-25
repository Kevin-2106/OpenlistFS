// winfsp-check is the setup program's read-only preflight and explicit MSI
// launcher. Plain inspection never loads WinFsp, contacts a server, or installs.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"openlistfs/internal/winfspcheck"
	"openlistfs/internal/winfspinstall"
)

func main() { os.Exit(run()) }

func run() int {
	quiet := flag.Bool("quiet", false, "report readiness through the exit code only")
	jsonOutput := flag.Bool("json", false, "print read-only dependency status as JSON")
	installPlan := flag.Bool("install-plan", false, "inspect whether the bundled MSI can safely install or repair")
	install := flag.Bool("install", false, "explicitly launch the verified MSI, elevate only Windows Installer, and wait")
	flag.Parse()
	if *install {
		code, err := winfspinstall.Run()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
		}
		return int(code)
	}
	if *installPlan {
		plan := winfspinstall.Plan()
		if *jsonOutput {
			_ = json.NewEncoder(os.Stdout).Encode(plan)
		} else if !*quiet {
			fmt.Println(plan.Message)
		}
		switch plan.Action {
		case "reuse":
			return 0
		case "install":
			return 10
		case "upgrade":
			return 11
		case "repair":
			return 12
		default:
			return 14
		}
	}
	status := winfspcheck.Check()
	if *jsonOutput {
		_ = json.NewEncoder(os.Stdout).Encode(status)
	} else if !*quiet {
		fmt.Println(status.Message)
	}
	switch status.Code {
	case "ready":
		return 0
	case "missing_installation":
		return 10
	case "incompatible_version":
		return 11
	case "unsupported_architecture":
		return 13
	default:
		return 12
	}
}
