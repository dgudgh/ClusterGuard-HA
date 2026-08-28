package kubernetes

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"clusterguard.io/ha/pkg/model"
)

func TestClientFactoryUsesTLSAndReloadsBearerToken(t *testing.T) {
	requests := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests++
		want := "Bearer token-one"
		if requests == 2 {
			want = "Bearer token-two"
		}
		if request.Header.Get("Authorization") != want {
			t.Errorf("authorization=%q want=%q", request.Header.Get("Authorization"), want)
		}
		if request.URL.Path != "/api/v1/namespaces/database/services/mysql-writer" {
			t.Errorf("path=%q", request.URL.Path)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"metadata":{"namespace":"database","name":"mysql-writer"}}`))
	}))
	defer server.Close()

	directory := t.TempDir()
	certificate, err := x509.ParseCertificate(server.Certificate().Raw)
	if err != nil {
		t.Fatal(err)
	}
	caPath := filepath.Join(directory, "ca.pem")
	if err := os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	tokenPath := filepath.Join(directory, "token")
	if err := os.WriteFile(tokenPath, []byte("token-one\n"), 0600); err != nil {
		t.Fatal(err)
	}
	profilePath := filepath.Join(directory, "profile.json")
	profile, _ := json.Marshal(CredentialProfile{CAFile: caPath, BearerTokenFile: tokenPath})
	if err := os.WriteFile(profilePath, profile, 0600); err != nil {
		t.Fatal(err)
	}
	client, err := (ClientFactory{}).ForTarget(context.Background(), model.RuntimeTarget{
		Kind: model.RuntimeKubernetes, Endpoint: server.URL, CredentialRef: profilePath, Active: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.GetService(context.Background(), "database", "mysql-writer"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tokenPath, []byte("token-two\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := client.GetService(context.Background(), "database", "mysql-writer"); err != nil {
		t.Fatal(err)
	}
}

func TestClientRejectsPlainHTTPKubernetesAPI(t *testing.T) {
	if _, err := (ClientFactory{}).ForTarget(context.Background(), model.RuntimeTarget{
		Kind: model.RuntimeKubernetes, Endpoint: "http://127.0.0.1:6443", CredentialRef: "/tmp/profile.json", Active: true,
	}); err == nil {
		t.Fatal("plain HTTP Kubernetes endpoint was accepted")
	}
}
