module code.sarnas.ru/xgate-modules/dns-cloudflare

go 1.26.9

require (
	code.sarnas.ru/api-gateway v0.0.0
	github.com/libdns/cloudflare v0.2.2
	github.com/libdns/libdns v1.1.1
)

replace code.sarnas.ru/api-gateway => ../../..
