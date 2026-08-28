package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/jisung9870/binbox-cli/internal/releasearchive"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: releasearchive archive|checksums|verify")
	}
	switch args[0] {
	case "archive":
		flags := flag.NewFlagSet("archive", flag.ContinueOnError)
		input := flags.String("input", "", "input executable")
		output := flags.String("output", "", "output tar.gz")
		name := flags.String("name", "bb", "archive entry name")
		notice := flags.String("notice", "", "optional third-party notice")
		epoch := flags.String("epoch", "", "source date epoch")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		seconds, err := strconv.ParseInt(*epoch, 10, 64)
		if err != nil || *input == "" || *output == "" || flags.NArg() != 0 {
			return fmt.Errorf("usage: releasearchive archive --input PATH --output PATH --epoch UNIX")
		}
		return releasearchive.WriteArchiveWithNotice(*input, *output, *name, *notice, time.Unix(seconds, 0))
	case "checksums":
		flags := flag.NewFlagSet("checksums", flag.ContinueOnError)
		output := flags.String("output", "", "output manifest")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *output == "" || flags.NArg() == 0 {
			return fmt.Errorf("usage: releasearchive checksums --output PATH ARCHIVE...")
		}
		return releasearchive.WriteChecksums(*output, flags.Args())
	case "verify":
		flags := flag.NewFlagSet("verify", flag.ContinueOnError)
		manifest := flags.String("manifest", "", "checksum manifest")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *manifest == "" || flags.NArg() != 0 {
			return fmt.Errorf("usage: releasearchive verify --manifest PATH")
		}
		return releasearchive.VerifyChecksums(*manifest)
	default:
		return fmt.Errorf("unknown releasearchive command %q", args[0])
	}
}
