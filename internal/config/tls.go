package config

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
)

type TLSConfig struct {
	CertFile      string // certificate file (proves this party's identity)
	KeyFile       string // private key file (cryptographic pair to CertFile)
	CAFile        string // Certificate Authority file (defines who we trust)
	ServerAddress string // Hostname/IP of the server — used to verify the server's certificate
	Server        bool   // true → configure as a SERVER; false → configure as a CLIENT
}

// SetupTLSConfig creates a secure connection config.
// It loads our identity (cert+key) and decides who we trust (CA).
func SetupTLSConfig(cfg TLSConfig) (*tls.Config, error) {
	var err error

	tlsConfig := &tls.Config{}

	// Load our own certificate and private key.
	// This is how we prove who we are to the other side.
	if cfg.CertFile != "" && cfg.KeyFile != "" {
		tlsConfig.Certificates = make([]tls.Certificate, 1)

		// LoadX509KeyPair reads both files, parses them, and verifies that the
		// private key matches the public key embedded in the certificate.
		tlsConfig.Certificates[0], err = tls.LoadX509KeyPair(
			cfg.CertFile,
			cfg.KeyFile,
		)
		if err != nil {
			return nil, err
		}
	}

	// Load the Certificate Authority (CA) — the authority both sides trust.
	// Used to verify that the other side's certificate is legitimate.
	if cfg.CAFile != "" {
		// Read the CA certificate file from disk into a byte slice.
		b, err := os.ReadFile(cfg.CAFile)
		if err != nil {
			return nil, err
		}

		// NewCertPool creates an empty pool of trusted certificates.
		// Think of it as an empty "trusted issuers" list.
		ca := x509.NewCertPool()

		// AppendCertsFromPEM parses the PEM-encoded data and adds any certificates
		// it finds into the pool. Returns false if no valid certificate was found.
		ok := ca.AppendCertsFromPEM(b)
		if !ok {
			return nil, fmt.Errorf(
				"failed to parse root certificate: %q",
				cfg.CAFile,
			)
		}

		//  Apply the CA pool differently based on the role
		if cfg.Server {
			// SERVER: use the CA to verify incoming client certificates.
			// Reject any client that doesn't have a valid certificate (mTLS).
			tlsConfig.ClientCAs = ca
			tlsConfig.ClientAuth = tls.RequireAndVerifyClientCert
		} else {
			// CLIENT: use the CA to verify the server's certificate.
			tlsConfig.RootCAs = ca
		}

		// The expected hostname on the server's certificate.
		// Prevents connecting to the wrong server by mistake.
		tlsConfig.ServerName = cfg.ServerAddress
	}

	return tlsConfig, nil
}
