module github.com/varad/jobstream/sources/scraper

go 1.25.0

require (
	github.com/brianvoe/gofakeit/v7 v7.1.2
	github.com/golang-jwt/jwt/v5 v5.3.1
	github.com/varad/jobstream/gen/go v0.0.0
	golang.org/x/time v0.15.0
	google.golang.org/grpc v1.74.2
	google.golang.org/protobuf v1.36.11
)

require (
	golang.org/x/net v0.40.0 // indirect
	golang.org/x/sys v0.33.0 // indirect
	golang.org/x/text v0.25.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20250528174236-200df99c418a // indirect
)

replace github.com/varad/jobstream/gen/go => ../../gen/go
