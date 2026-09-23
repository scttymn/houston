package main

import (
	"os"

	"github.com/scttymn/houston/internal/cli"
	"github.com/scttymn/houston/internal/docker"
)

func main() {
	os.Exit(cli.Main(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, docker.New()))
}
