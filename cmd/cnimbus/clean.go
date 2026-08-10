package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
)

// runClean removes the Docker-side leftovers `cnimbus prepare` creates
// (a named cache volume and a builder image per architecture) that
// nothing else in cnimbus ever cleans up on its own -- the cache volume
// in particular holds the full downloaded kernel + BusyBox source tree
// plus build objects, hundreds of MB per architecture, silently
// accumulating across every `prepare` run.
func runClean(args []string) error {
	fs := flag.NewFlagSet("clean", flag.ExitOnError)
	piecesDir := fs.String("pieces-dir", defaultPiecesDir, "also remove this pieces output directory")
	removePieces := fs.Bool("pieces", false, "also remove the pieces output directory (see --pieces-dir)")
	dryRun := fs.Bool("dry-run", false, "print what would be removed without actually removing it")
	if err := fs.Parse(args); err != nil {
		return err
	}

	archs := []string{"amd64", "arm64"}
	var toRemoveVolumes, toRemoveImages []string
	for _, arch := range archs {
		toRemoveVolumes = append(toRemoveVolumes, "cnimbus-cache-"+arch)
		toRemoveImages = append(toRemoveImages, builderImageTag+"-"+arch+":latest")
	}

	if _, err := exec.LookPath("docker"); err != nil {
		fmt.Println("docker not found on PATH -- nothing Docker-side to clean (skipping volumes/images)")
	} else {
		for _, v := range toRemoveVolumes {
			removeDockerResource(*dryRun, "volume", v)
		}
		for _, img := range toRemoveImages {
			removeDockerResource(*dryRun, "image", img)
		}
	}

	if *removePieces {
		if *dryRun {
			fmt.Printf("would remove %s\n", *piecesDir)
		} else if err := os.RemoveAll(*piecesDir); err != nil {
			return fmt.Errorf("removing %s: %w", *piecesDir, err)
		} else {
			fmt.Printf("removed %s\n", *piecesDir)
		}
	}

	return nil
}

// removeDockerResource runs `docker <kind> rm <name>`, tolerating "no
// such volume/image" as success (there's nothing to clean up if
// `prepare` was never run for that architecture) -- printing anything
// else Docker says, but never treating it as fatal: clean's whole point
// is best-effort tidying, not a hard requirement that every artifact
// existed in the first place.
func removeDockerResource(dryRun bool, kind, name string) {
	if dryRun {
		fmt.Printf("would remove docker %s %s\n", kind, name)
		return
	}
	cmd := exec.Command("docker", kind, "rm", name) // #nosec G204 -- kind/name are this file's own fixed strings, never user input
	out, err := cmd.CombinedOutput()
	if err != nil {
		fmt.Printf("docker %s %s: %s\n", kind, name, firstLine(string(out)))
		return
	}
	fmt.Printf("removed docker %s %s\n", kind, name)
}

func firstLine(s string) string {
	for i, c := range s {
		if c == '\n' || c == '\r' {
			return s[:i]
		}
	}
	return s
}
