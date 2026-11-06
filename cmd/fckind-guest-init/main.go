package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"fckind/pkg/guest"
)

var (
	version = "v0.1.0"
)

func main() {
	modeFlag := flag.String("mode", "", "Execution mode: 'overlay-init' or 'agent'")
	showVersion := flag.Bool("version", false, "Show version")
	flag.Parse()

	if *showVersion {
		fmt.Printf("fckind-guest-init %s\n", version)
		os.Exit(0)
	}

	mode := *modeFlag
	if mode == "" {
		prog := filepath.Base(os.Args[0])
		if prog == "overlay-init" || os.Getpid() == 1 {
			mode = "overlay-init"
		} else {
			mode = "agent"
		}
	}

	switch mode {
	case "overlay-init":
		if err := guest.RunOverlayInit(); err != nil {
			log.Fatalf("[overlay-init] Fatal error: %v", err)
		}
	case "agent":
		if err := guest.RunAgent(); err != nil {
			log.Fatalf("[fckind-agent] Fatal error: %v", err)
		}
	default:
		log.Fatalf("Unknown mode: %s. Valid modes: 'overlay-init', 'agent'", mode)
	}
}
