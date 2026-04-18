module github.com/varad/jobstream/services/intake

go 1.23

require (
	github.com/varad/jobstream/gen/go v0.0.0
	google.golang.org/grpc v1.72.0
)

replace github.com/varad/jobstream/gen/go => ../../gen/go

require (
	golang.org/x/net v0.35.0 // indirect
	golang.org/x/sys v0.30.0 // indirect
	golang.org/x/text v0.22.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20250218202821-56aae31c358a // indirect
	google.golang.org/protobuf v1.36.11 // indirect
)
