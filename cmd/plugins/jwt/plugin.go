//go:build pluginmain

package main

import (
	"github.com/basili4-1982/api-gateway/internal/features/authv1"
	"github.com/sarnas-it/pluginrpc"
	"google.golang.org/grpc"
)

var (
	Name            = "jwt"
	Version         = "0.1.0"
	ProtocolVersion uint32
	AuthService     authv1.AuthServiceServer = mustServer()
)

func mustServer() authv1.AuthServiceServer {
	srv, err := newServer()
	if err != nil {
		panic(err)
	}
	return srv
}

func Register(s grpc.ServiceRegistrar) {
	authv1.RegisterAuthServiceServer(s, AuthService)
}

func init() {
	if err := pluginrpc.SOServe(pluginrpc.SOConfig{
		Name:            &Name,
		Version:         &Version,
		ProtocolVersion: &ProtocolVersion,
	}); err != nil {
		panic(err)
	}
}

func main() {}
