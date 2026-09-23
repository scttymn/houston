package main

import (
	"os"

	"github.com/sevenmoons/houston/internal/cli"
	"github.com/sevenmoons/houston/internal/docker"
)

func main() {
	os.Exit(cli.Main(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, docker.New()))
}
