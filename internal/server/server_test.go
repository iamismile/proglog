package server

import (
	"context"
	"net"
	"os"
	"testing"

	api "github.com/iamismile/proglog/api/v1"
	"github.com/iamismile/proglog/internal/config"
	"github.com/iamismile/proglog/internal/log"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
)

func TestServer(t *testing.T) {
	for scenario, fn := range map[string]func(
		t *testing.T,
		client api.LogClient,
		config *Config,
	){
		"produce/consume a message to/from the log succeeds": testProduceConsume,
		"produce/consume stream succeeds":                    testProduceConsumeStream,
		"consume past log boundary":                          testConsumePastBoundary,
	} {
		t.Run(scenario, func(t *testing.T) {
			// Create test server and client.
			client, config, teardown := setupTest(t, nil)
			defer teardown()

			// Execute current test scenario.
			fn(t, client, config)
		})
	}
}

func setupTest(t *testing.T, fn func(*Config)) (
	client api.LogClient,
	cfg *Config,
	teardown func(),
) {
	t.Helper()

	// Start TCP listener on random port
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	// Build client TLS — loads client cert/key (proves client identity to server)
	// and CA (used to verify the server's certificate).
	clientTLSConfig, err := config.SetupTLSConfig(config.TLSConfig{
		CertFile: config.ClientCertFile,
		KeyFile:  config.ClientKeyFile,
		CAFile:   config.CAFile,
	})
	require.NoError(t, err)

	// Wrap the TLS config into gRPC transport credentials and open the connection.
	// No actual network dial happens here — gRPC dials lazily on first RPC call.
	clientCreds := credentials.NewTLS(clientTLSConfig)
	cc, err := grpc.NewClient(
		l.Addr().String(),
		grpc.WithTransportCredentials(clientCreds),
	)
	require.NoError(t, err)
	client = api.NewLogClient(cc)

	// Build server TLS — loads server cert/key (proves server identity to client)
	// and CA (used to verify incoming client certificates — this is mTLS).
	serverTLSConfig, err := config.SetupTLSConfig(config.TLSConfig{
		CertFile:      config.ServerCertFile,
		KeyFile:       config.ServerKeyFile,
		CAFile:        config.CAFile,
		ServerAddress: l.Addr().String(),
		Server:        true,
	})
	require.NoError(t, err)

	serverCreds := credentials.NewTLS(serverTLSConfig)

	// Create temporary directory for log data
	dir, err := os.MkdirTemp("", "server-test")
	require.NoError(t, err)

	// Create commit log instance
	clog, err := log.NewLog(dir, log.Config{})
	require.NoError(t, err)

	// Server configuration
	cfg = &Config{
		CommitLog: clog,
	}

	// fn lets individual test cases tweak the config before the server starts.
	if fn != nil {
		fn(cfg)
	}

	// Create gRPC server
	server, err := NewGRPCServer(cfg, grpc.Creds(serverCreds))
	require.NoError(t, err)

	// Serve blocks, so run it in a goroutine.
	// server.Stop() in teardown triggers a graceful shutdown.
	go func() {
		server.Serve(l)
	}()

	// Return cleanup function
	return client, cfg, func() {
		server.Stop() // graceful shutdown, waits for in-flight RPCs
		cc.Close()    // close client connection
		l.Close()     // release the port
		clog.Remove() // delete temp log files from disk
	}
}

func testProduceConsume(t *testing.T, client api.LogClient, config *Config) {
	ctx := context.Background()

	want := &api.Record{
		Value: []byte("hello world"),
	}

	produce, err := client.Produce(
		ctx,
		&api.ProduceRequest{
			Record: want,
		},
	)
	require.NoError(t, err)

	consume, err := client.Consume(
		ctx,
		&api.ConsumeRequest{
			Offset: produce.Offset,
		},
	)
	require.NoError(t, err)
	require.Equal(t, want.Value, consume.Record.Value)
	require.Equal(t, want.Offset, consume.Record.Offset)
}

func testConsumePastBoundary(t *testing.T, client api.LogClient, config *Config) {
	ctx := context.Background()

	produce, err := client.Produce(
		ctx,
		&api.ProduceRequest{
			Record: &api.Record{
				Value: []byte("hello world"),
			},
		},
	)
	require.NoError(t, err)

	consume, err := client.Consume(
		ctx,
		&api.ConsumeRequest{
			Offset: produce.Offset + 1,
		},
	)
	if consume != nil {
		t.Fatal("consume not nil")
	}

	got := status.Code(err)
	want := status.Code(api.ErrOffsetOutOfRange{}.GRPCStatus().Err())
	if got != want {
		t.Fatalf("got err: %v, want: %v", got, want)
	}
}

func testProduceConsumeStream(t *testing.T, client api.LogClient, config *Config) {
	ctx := context.Background()

	records := []*api.Record{{
		Value:  []byte("first message"),
		Offset: 0,
	}, {
		Value:  []byte("second message"),
		Offset: 1,
	}}

	{
		stream, err := client.ProduceStream(ctx)
		require.NoError(t, err)

		for offset, record := range records {
			err = stream.Send(&api.ProduceRequest{
				Record: record,
			})
			require.NoError(t, err)

			res, err := stream.Recv()
			require.NoError(t, err)

			if res.Offset != uint64(offset) {
				t.Fatalf("got offset: %d, want: %d", res.Offset, offset)
			}
		}
	}

	{
		stream, err := client.ConsumeStream(
			ctx,
			&api.ConsumeRequest{
				Offset: 0,
			},
		)
		require.NoError(t, err)

		for i, record := range records {
			res, err := stream.Recv()
			require.NoError(t, err)
			require.Equal(t, res.Record, &api.Record{
				Value:  record.Value,
				Offset: uint64(i),
			})
		}
	}
}
