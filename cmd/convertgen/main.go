package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/cogitatorum/convertgen"
)

func main() {
	configPath := flag.String("config", "", "path to convert.yaml")
	descriptorsPath := flag.String("descriptors", "", "path to FileDescriptorSet from `buf build -o`")
	flag.Parse()

	if err := convertgen.Generate(convertgen.Options{
		ConfigPath:      *configPath,
		DescriptorsPath: *descriptorsPath,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "convertgen: %v\n", err)
		os.Exit(1)
	}
}
