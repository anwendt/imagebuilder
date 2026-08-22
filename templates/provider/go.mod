module github.com/yourorg/imagebuilder-provider-example

go 1.26.6

require github.com/anwendt/imagebuilder v0.0.0

require (
	golang.org/x/net v0.56.0 // indirect
	golang.org/x/sys v0.46.0 // indirect
	golang.org/x/text v0.39.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260519071638-aa98bba5eb94 // indirect
	google.golang.org/grpc v1.82.1 // indirect
	google.golang.org/protobuf v1.36.12-0.20260120151049-f2248ac996af // indirect
)

replace github.com/anwendt/imagebuilder => ../..
