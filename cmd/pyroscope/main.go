package main

import (
	"os"

	"github.com/fatih/color"
	"github.com/taoey/pyroscope/pkg/cli"
	"github.com/taoey/pyroscope/pkg/config"
)

func main() {
	cfg := config.New()
	err := cli.Start(cfg)
	if err != nil {
		os.Stderr.Write([]byte(color.RedString("Error: ") + err.Error() + "\n\n"))
	}
}
