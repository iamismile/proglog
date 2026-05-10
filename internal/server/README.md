# Proglog – gRPC Setup

## Prerequisites

- Go 1.25 or later
- Protocol Buffers compiler (`protoc`)
- Go plugins for protoc: `protoc-gen-go` and `protoc-gen-go-grpc`
- `google.golang.org/protobuf/proto` — used internally for marshaling/unmarshaling records

Install protoc plugins:

```bash
go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
```

## Installation

```bash
git clone https://github.com/iamismile/proglog.git
cd proglog
```

## Generate Protobuf Files

```bash
make compile
```

## gRPC Methods

The gRPC service is defined in `api/v1/log.proto` and exposes:

- `Produce` – Append a record to the log
- `Consume` – Read a record by offset
- `ConsumeStream` – Stream records starting from an offset
- `ProduceStream` – Bidirectional stream for producing multiple records

> **Note:** The default `main.go` starts the HTTP server. To use gRPC, modify `main.go` or create a separate entry point that starts the gRPC server on `:50051`.
