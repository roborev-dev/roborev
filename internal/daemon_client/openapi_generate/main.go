package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/roborev-dev/roborev/internal/daemon"
)

func main() {
	output := flag.String("o", "", "output file")
	downgrade := flag.Bool("openapi-3.0", false, "emit OpenAPI 3.0 for code generators")
	flag.Parse()
	if *output == "" {
		fmt.Fprintln(os.Stderr, "missing -o")
		os.Exit(2)
	}

	var (
		spec []byte
		err  error
	)
	if *downgrade {
		spec, err = daemon.OpenAPISpec30()
	} else {
		spec, err = daemon.OpenAPISpec()
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "generate OpenAPI spec: %v\n", err)
		os.Exit(1)
	}
	if err := os.WriteFile(*output, append(spec, '\n'), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "write OpenAPI spec: %v\n", err)
		os.Exit(1)
	}
}
