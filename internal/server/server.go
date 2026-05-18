package server

import (
	"context"

	api "github.com/iamismile/proglog/api/v1"
	"google.golang.org/grpc"
)

// CommitLog abstracts the log so the server isn't tied to a concrete implementation.
// Swap in any type that satisfies Append/Read (e.g. in-memory for tests, disk-backed for prod).
type CommitLog interface {
	Append(*api.Record) (uint64, error)
	Read(uint64) (*api.Record, error)
}

// Config holds dependencies injected into the server.
type Config struct {
	CommitLog CommitLog
}

type grpcServer struct {
	api.UnimplementedLogServer
	*Config
}

func NewGRPCServer(config *Config, opts ...grpc.ServerOption) (*grpc.Server, error) {
	gsrv := grpc.NewServer(opts...)
	srv, err := newgrpcServer(config)
	if err != nil {
		return nil, err
	}

	api.RegisterLogServer(gsrv, srv)
	return gsrv, nil
}

func newgrpcServer(config *Config) (srv *grpcServer, err error) {
	srv = &grpcServer{
		Config: config,
	}

	return srv, nil
}

// --- Unary RPCs ---

func (s *grpcServer) Produce(ctx context.Context, req *api.ProduceRequest) (*api.ProduceResponse, error) {
	offset, err := s.CommitLog.Append(req.Record)
	if err != nil {
		return nil, err
	}

	return &api.ProduceResponse{Offset: offset}, nil
}

func (s *grpcServer) Consume(ctx context.Context, req *api.ConsumeRequest) (*api.ConsumeResponse, error) {
	record, err := s.CommitLog.Read(req.Offset)
	if err != nil {
		return nil, err
	}

	return &api.ConsumeResponse{Record: record}, nil
}

// --- Streaming RPCs ---

// ProduceStream is bidirectional streaming.
//
// Client continuously sends records.
// Server continuously responds with offsets.
//
//	Client ---> record
//	Server ---> offset
func (s *grpcServer) ProduceStream(stream api.Log_ProduceStreamServer) error {
	for {
		// Receive next record from client.
		req, err := stream.Recv()
		if err != nil {
			return err // Returning the error closes the stream.
		}

		// Append record to log.
		res, err := s.Produce(stream.Context(), req)
		if err != nil {
			return err
		}

		// Send stored offset back to client.
		if err = stream.Send(res); err != nil {
			return err
		}
	}
}

// ConsumeStream is server-side streaming.
//
// Client sends a starting offset once.
// Server continuously streams records from that offset onward.
//
//	Client ---> starting offset
//	Server ---> record 10
//	Server ---> record 11
//	Server ---> record 12
//	...
//
// As new records appear, the client keeps receiving them in real time.
func (s *grpcServer) ConsumeStream(req *api.ConsumeRequest, stream api.Log_ConsumeStreamServer) error {
	for {
		select {

		// Stop if client disconnects or cancels request.
		case <-stream.Context().Done():
			return nil

		default:
			// Try reading the record at the current offset.
			res, err := s.Consume(stream.Context(), req)

			switch err.(type) {
			// No error -> record exists
			case nil:

			// Offset not available yet.
			// Keep waiting for future records.
			case api.ErrOffsetOutOfRange:
				continue

			// Unexpected error.
			default:
				return err
			}

			// Send the record to the client.
			if err = stream.Send(res); err != nil {
				return err
			}

			// Move to next record.
			req.Offset++
		}
	}
}
