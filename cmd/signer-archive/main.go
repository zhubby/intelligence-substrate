// signer-archive is an offline administrator tool, never a Worker repository tool.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/intelligence-substrate/platform/internal/signingarchive"
)

func run(args []string, diagnostics io.Writer) int {
	fail := func() int { fmt.Fprintln(diagnostics, "private archive operation failed"); return 1 }
	if len(args) < 1 {
		return fail()
	}
	flags := flag.NewFlagSet("signer-archive", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	output := flags.String("output", "", "exclusive absolute output path")
	key := flags.String("key-file", "", "private recovery key file")
	input := flags.String("input", "", "private archive input")
	if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 || *output == "" {
		return fail()
	}
	var err error
	switch args[0] {
	case "keygen":
		if *key != "" || *input != "" {
			return fail()
		}
		err = signingarchive.GenerateKey(*output)
	case "seal", "open":
		if *key == "" || *input == "" {
			return fail()
		}
		if args[0] == "seal" {
			err = signingarchive.Seal(*key, *input, *output)
		} else {
			err = signingarchive.Open(*key, *input, *output)
		}
	default:
		return fail()
	}
	if err != nil {
		return fail()
	}
	return 0
}

func main() { os.Exit(run(os.Args[1:], os.Stderr)) }
