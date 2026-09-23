package main

import (
	"fmt"
	"os"

	"norn/v2/api/effect/supervisor"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == supervisor.ProtocolFlag {
		fmt.Println(supervisor.ProtocolV1)
		return
	}
	if len(os.Args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: norn-effect-runner [--protocol] (request on stdin)")
		os.Exit(2)
	}
	if err := supervisor.RunHelper(os.Stdin); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
