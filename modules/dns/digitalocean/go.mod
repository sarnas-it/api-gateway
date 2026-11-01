module code.sarnas.ru/xgate-modules/dns-digitalocean

go 1.26.9

require (
	code.sarnas.ru/api-gateway v0.0.0
	github.com/libdns/digitalocean v0.0.0-20250606071607-dfa7af5c2e31
	github.com/libdns/libdns v1.1.1
)

require (
	github.com/digitalocean/godo v1.148.0 // indirect
	github.com/google/go-querystring v1.1.0 // indirect
	github.com/hashicorp/go-cleanhttp v0.5.2 // indirect
	github.com/hashicorp/go-retryablehttp v0.7.7 // indirect
	golang.org/x/oauth2 v0.37.0 // indirect
	golang.org/x/time v0.15.0 // indirect
)

replace code.sarnas.ru/api-gateway => ../../..
