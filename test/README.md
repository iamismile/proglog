# Test Certificate Configuration

This directory contains certificate configuration files used for TLS/SSL setup during testing and development. These files are used with [CFSSL](https://github.com/cloudflare/cfssl) (CloudFlare's Public Key Infrastructure toolkit) to generate certificates for secure communication.

This section explains only the CFSSL-related part of the Makefile used for generating TLS certificates in this project.

---

# Prerequisites

Install CFSSL before running any commands:

```bash
go install github.com/cloudflare/cfssl/cmd/cfssl@latest
go install github.com/cloudflare/cfssl/cmd/cfssljson@latest
```

Verify installation:

```bash
cfssl version
```

---

# Makefile Targets

> Run all `make` commands from the **project root directory** (where the `Makefile` lives).

## `make init`

Creates a local config directory to store generated certificates.

```bash
make init
```

Default path:

```text
~/.proglog/
```

---

## `make gencert`

Generates CA, server, and client certificates using CFSSL.

```bash
make gencert
```

### Steps performed

### 1. Generate CA certificate

```bash
cfssl gencert -initca test/ca-csr.json | cfssljson -bare ca
```

### 2. Generate server certificate (signed by CA)

```bash
cfssl gencert \
  -ca=ca.pem \
  -ca-key=ca-key.pem \
  -config=test/ca-config.json \
  -profile=server \
  test/server-csr.json | cfssljson -bare server
```

### 3. Generate client certificate (signed by CA)

```bash
cfssl gencert \
  -ca=ca.pem \
  -ca-key=ca-key.pem \
  -config=test/ca-config.json \
  -profile=client \
  test/client-csr.json | cfssljson -bare client
```

> **Client certificates are required.** The test suite (`server/server_test.go`) loads
> `client.pem` and `client-key.pem` directly. Tests will fail if these files are missing.

### 4. Move output files

All generated files are moved to:

```text
~/.proglog/
```

---

# Generated Files

## CA files

- `ca.pem` → CA certificate
- `ca-key.pem` → CA private key

## Server files

- `server.pem` → server certificate
- `server-key.pem` → server private key

## Client files

- `client.pem` → client certificate
- `client-key.pem` → client private key

> `*.csr` files are intermediate signing requests generated during the process. They are not needed after the certificates are created.

---

# Configuration Files

## `ca-csr.json`

Defines the Certificate Authority identity.

- CN: My Awesome CA
- RSA 2048-bit key

## `ca-config.json`

Defines certificate profiles:

- `server` → server authentication
- `client` → client authentication

## `server-csr.json`

Defines server certificate details:

- CN: 127.0.0.1
- Hosts:
  - localhost
  - 127.0.0.1

## `client-csr.json`

Defines client certificate details used for mTLS authentication.

Hosts are required as **Subject Alternative Names (SANs)**. The TLS client checks that the server's certificate was issued for the hostname it is connecting to. Without SANs, the connection is rejected even if the certificate is otherwise valid.

---

# Connecting Certificates to the Go Code

The `config` package (`internal/config/files.go`) resolves certificate paths automatically:

```go
var (
    CAFile         = configFile("ca.pem")
    ServerCertFile = configFile("server.pem")
    ServerKeyFile  = configFile("server-key.pem")
    ClientCertFile = configFile("client.pem")
    ClientKeyFile  = configFile("client-key.pem")
)
```

These map to the `TLSConfig` struct fields used in the test setup:

```go
// Client-side TLS (used in setupTest)
config.SetupTLSConfig(config.TLSConfig{
    CertFile: config.ClientCertFile, // ~/.proglog/client.pem
    KeyFile:  config.ClientKeyFile,  // ~/.proglog/client-key.pem
    CAFile:   config.CAFile,         // ~/.proglog/ca.pem
})

// Server-side TLS (used in setupTest)
config.SetupTLSConfig(config.TLSConfig{
    CertFile:      config.ServerCertFile, // ~/.proglog/server.pem
    KeyFile:       config.ServerKeyFile,  // ~/.proglog/server-key.pem
    CAFile:        config.CAFile,         // ~/.proglog/ca.pem
    ServerAddress: l.Addr().String(),
    Server:        true,
})
```

---

# Notes

- This setup is for **development only**
- The CA is self-signed and not publicly trusted
- Do not use these certificates in production
- To override the default `~/.proglog/` path (e.g. in CI or Docker), set the `CONFIG_DIR`
  environment variable to point to your certificate directory:

  ```bash
  export CONFIG_DIR=/path/to/certs
  ```

---

## Purpose in Proglog

These certificates enable TLS encryption for the distributed logging system:

- Secure communication between server and clients
- Mutual TLS (mTLS) — both server and client verify each other's identity
- Development and testing environment setup
