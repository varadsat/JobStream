package server

import (
	intakev1 "github.com/varad/jobstream/gen/go/jobstream/v1"
)

// IntakeServer embeds the generated unimplemented stub so all RPCs return
// codes.Unimplemented until each method is filled in by a later chunk.
type IntakeServer struct {
	intakev1.UnimplementedIntakeServiceServer
}

func New() *IntakeServer {
	return &IntakeServer{}
}
