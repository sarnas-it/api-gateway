package main

import (
	"context"
	"fmt"

	"github.com/basili4-1982/api-gateway/internal/config"
	"github.com/basili4-1982/api-gateway/internal/features/authv1"
	"github.com/basili4-1982/api-gateway/internal/jwtutil"
	"github.com/sarnas-it/pluginrpc"
	"google.golang.org/grpc"
)

type jwtServer struct {
	authv1.UnimplementedAuthServiceServer
	v             *jwtutil.JWTValidator
	claimMappings []string
}

func (s *jwtServer) Validate(_ context.Context, req *authv1.ValidateRequest) (*authv1.ValidateResponse, error) {
	if req.Token == "" {
		if req.Required {
			return &authv1.ValidateResponse{Ok: false, Error: "missing authorization token"}, nil
		}
		return &authv1.ValidateResponse{Ok: true}, nil
	}
	claims, err := s.v.ParseAndValidate(req.Token)
	if err != nil {
		if req.Required {
			return &authv1.ValidateResponse{Ok: false, Error: fmt.Sprintf("invalid token: %v", err)}, nil
		}
		return &authv1.ValidateResponse{Ok: true}, nil
	}
	if err := s.v.ValidateClaims(claims); err != nil {
		if req.Required {
			return &authv1.ValidateResponse{Ok: false, Error: fmt.Sprintf("invalid token claims: %v", err)}, nil
		}
		return &authv1.ValidateResponse{Ok: true}, nil
	}
	if err := jwtutil.CheckRoles(claims, req.AnyRoles, req.AllRoles); err != nil {
		return &authv1.ValidateResponse{Ok: false, Error: err.Error()}, nil
	}
	extracted := jwtutil.ExtractClaims(claims, s.claimMappings)
	resp := &authv1.ValidateResponse{Ok: true, HasClaims: true, Claims: make(map[string]string, len(extracted))}
	for k, v := range extracted {
		resp.Claims[k] = fmt.Sprintf("%v", v)
	}
	return resp, nil
}

func newServer() (*jwtServer, error) {
	jcfg, err := pluginrpc.PluginConfig[config.JWTConfig]()
	if err != nil {
		return nil, err
	}
	v, err := jwtutil.NewJWTValidator(
		jcfg.SecretKey, jcfg.Algorithm, jcfg.ValidateExp, jcfg.ValidateIss,
		jcfg.ExpectedIss, jcfg.ValidateAud, jcfg.ExpectedAud, jcfg.PublicKeyFile,
	)
	if err != nil {
		return nil, err
	}
	return &jwtServer{v: v, claimMappings: jcfg.ClaimMappings}, nil
}

func register(s grpc.ServiceRegistrar) {
	srv, err := newServer()
	if err != nil {
		panic(err)
	}
	authv1.RegisterAuthServiceServer(s, srv)
}
