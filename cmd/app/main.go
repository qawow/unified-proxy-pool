package main

import (
	"fmt"
	"os"

	"unified-proxy-pool/internal/app"
	"unified-proxy-pool/internal/version"
)

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "-version" || os.Args[1] == "--version" || os.Args[1] == "version") {
		fmt.Printf("%s %s\n", version.Short(), version.Time)
		return
	}
	app.Run()
}
