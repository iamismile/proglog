package log_v1

import (
	"fmt"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/status"
)

// ErrOffsetOutOfRange is returned when a client
// requests a non-existent log offset.
type ErrOffsetOutOfRange struct {
	Offset uint64
}

// GRPCStatus builds and returns a gRPC *status.Status for this error.
//
// gRPC checks if an error implements this method (the GRPCStatus interface).
// If it does, gRPC uses this status directly when transmitting the error to
// the client, instead of sending a generic "Unknown" error.
func (e ErrOffsetOutOfRange) GRPCStatus() *status.Status {
	st := status.New(
		404,
		fmt.Sprintf("offset out of range: %d", e.Offset),
	)

	msg := fmt.Sprintf(
		"The requested offset is outside the log's range: %d",
		e.Offset,
	)
	// Human-readable detail for i18n-aware clients.
	d := &errdetails.LocalizedMessage{
		Locale:  "en-US",
		Message: msg,
	}

	std, err := st.WithDetails(d)
	if err != nil {
		return st // fall back to plain status if detail serialization fails
	}

	return std
}

// Error implements the standard Go error interface.
// This allows ErrOffsetOutOfRange to be used anywhere a normal Go error
// is expected (e.g. returned from functions, passed to log.Fatal, etc.)
func (e ErrOffsetOutOfRange) Error() string {
	return e.GRPCStatus().Err().Error()
}
