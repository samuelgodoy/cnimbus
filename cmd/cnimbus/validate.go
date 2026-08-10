package main

import (
	"debug/elf"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"cnimbus/internal/nimbusfile"
)

// runValidate checks a Nimbusfile without building anything: syntax
// (via nimbusfile.Parse itself), every local COPY/ADD source actually
// existing, and -- the single most common mistake this project's own
// README warns about twice -- every COPY/ADD'd ELF binary actually
// being built for the Nimbusfile's own declared ARCH. A binary built
// for the wrong architecture doesn't fail at `build-disk` time (the
// bytes just get copied in verbatim); it fails silently at boot,
// inside a VM with no shell to debug from, which is exactly the
// scenario this command exists to catch before you get there.
func runValidate(args []string) error {
	fs := flag.NewFlagSet("validate", flag.ExitOnError)
	nimbusfilePath := fs.String("f", "Nimbusfile", "Nimbusfile to validate")
	arch := fs.String("arch", "", "target architecture: amd64 or arm64; overrides the Nimbusfile's ARCH")
	buildArgs := buildArgFlag{}
	fs.Var(buildArgs, "build-arg", "set a value for an ARG directive (NAME=VALUE); repeatable")
	if err := fs.Parse(args); err != nil {
		return err
	}

	hf, err := nimbusfile.Parse(*nimbusfilePath, buildArgs)
	if err != nil {
		return fmt.Errorf("parsing %s: %w", *nimbusfilePath, err)
	}
	if *arch != "" {
		if *arch != "amd64" && *arch != "arm64" {
			return fmt.Errorf("--arch must be \"amd64\" or \"arm64\", got %q", *arch)
		}
		hf.Arch = *arch
	}
	fmt.Printf("%s: syntax OK (ARCH %s, FORMAT %s)\n", *nimbusfilePath, hf.Arch, hf.Format)

	var problems []string

	for _, c := range hf.Copies {
		if c.IsURL {
			continue // fetched at build-disk time; nothing local to check now
		}
		srcPath := filepath.Join(hf.BaseDir, c.Src)
		info, err := os.Stat(srcPath)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s %s: %v", directiveName(c), c.Src, err))
			continue
		}
		if info.IsDir() || strings.ContainsAny(c.Src, "*?[") {
			continue // directory/glob sources: arch-checking every match is out of scope here, existence is enough
		}
		if isTarball(c.Src) {
			continue // extracted at build time; not a single ELF binary to check
		}
		if prob := checkELFArch(srcPath, hf.Arch); prob != "" {
			problems = append(problems, fmt.Sprintf("%s %s: %s", directiveName(c), c.Src, prob))
		}
	}

	if hf.User != "" {
		fmt.Printf("USER %s: ENTRYPOINT/CMD/SERVICE run as this unprivileged account -- remember ports below 1024 need root\n", hf.User)
	}

	if len(problems) == 0 {
		fmt.Println("no problems found")
		return nil
	}

	fmt.Fprintln(os.Stderr, "problems found:")
	for _, p := range problems {
		fmt.Fprintf(os.Stderr, "  - %s\n", p)
	}
	return fmt.Errorf("%d problem(s) found", len(problems))
}

func directiveName(c nimbusfile.CopyOp) string {
	if c.IsAdd {
		return "ADD"
	}
	return "COPY"
}

// checkELFArch reads just enough of the file to identify it as ELF and
// check its e_machine field, returning a human-readable problem
// description, or "" if the file matches wantArch (or isn't ELF at
// all -- a non-ELF COPY target, e.g. a config file or shell script, is
// not this check's concern).
func checkELFArch(path, wantArch string) string {
	f, err := elf.Open(path)
	if err != nil {
		return "" // not a valid ELF file (or not ELF at all) -- nothing for this check to say
	}
	defer func() { _ = f.Close() }()

	var wantMachine elf.Machine
	switch wantArch {
	case "amd64":
		wantMachine = elf.EM_X86_64
	case "arm64":
		wantMachine = elf.EM_AARCH64
	default:
		return ""
	}
	if f.Machine != wantMachine {
		return fmt.Sprintf("ELF binary built for %s, but the Nimbusfile declares ARCH %s -- "+
			"it will not run at boot (no shell to debug from once it doesn't)", f.Machine, wantArch)
	}
	return ""
}
