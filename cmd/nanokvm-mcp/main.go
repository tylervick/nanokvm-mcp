// Command nanokvm-mcp is an MCP server for Sipeed NanoKVM devices.
package main

import (
	"fmt"
	"os"
)

var version = "dev"

func main() {
	fmt.Fprintf(os.Stderr, "nanokvm-mcp %s\n", version)
}
