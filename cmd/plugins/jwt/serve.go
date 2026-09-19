//go:build !pluginmain

package main

import (
	"fmt"
	"os"

	"github.com/sarnas-it/pluginrpc"
	"google.golang.org/grpc"
)

func main() {
	if err := pluginrpc.Serve(pluginrpc.ServeConfig{
		Name:    "jwt",
		Version: "0.1.0",
		Register: func(s grpc.ServiceRegistrar) {
			register(s)
		},
	}); err != nil {
		fmt.Fprintln(os.Stderr, "jwt plugin:", err)
		os.Exit(1)
	}
}
