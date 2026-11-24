package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"

	"github.com/X9st/TianFaSRC/internal/api"
)

func main() {
	check := flag.Bool("check", false, "verify that the generated contract is current")
	output := flag.String("output", "api/openapi.yaml", "output path")
	flag.Parse()

	content, err := api.OpenAPI()
	if err != nil {
		fail(err)
	}
	if *check {
		existing, err := os.ReadFile(*output)
		if err != nil {
			fail(err)
		}
		if !bytes.Equal(existing, content) {
			fail(fmt.Errorf("%s is stale; run make openapi", *output))
		}
		return
	}
	if err := os.WriteFile(*output, content, 0o644); err != nil {
		fail(err)
	}
}

func fail(err error) {
	_, _ = fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
