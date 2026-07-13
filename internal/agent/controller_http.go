package agent

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"os"
	"time"
)

func NewControllerHTTPClient(configuration Config) (*http.Client, error) {
	roots, err := x509.SystemCertPool()
	if err != nil || roots == nil {
		roots = x509.NewCertPool()
	}
	if configuration.ControllerCAFile != "" {
		contents, err := os.ReadFile(configuration.ControllerCAFile)
		if err != nil {
			return nil, fmt.Errorf("read controller CA: %w", err)
		}
		if !roots.AppendCertsFromPEM(contents) {
			return nil, fmt.Errorf("controller CA file contains no certificates")
		}
	}
	timeout := time.Duration(configuration.ReconcileTimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
			RootCAs:    roots,
			ServerName: configuration.ControllerServerName,
		},
		ForceAttemptHTTP2: true,
	}
	return &http.Client{Transport: transport, Timeout: timeout}, nil
}
