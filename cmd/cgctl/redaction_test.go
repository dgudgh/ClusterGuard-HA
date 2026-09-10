package main

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCLIJSONRedactsLegacyServerSecrets(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"status":"ok","result":{"revision":18446744073709551615,"observation_token":"public-cas","primary_conninfo":"password=leaked","nested":{"token":"leaked"}}}`)
	}))
	defer server.Close()
	var out, errout bytes.Buffer
	code := run([]string{"--server", server.URL, "--json", "health", "11111111-1111-4111-8111-111111111111"}, &out, &errout, server.Client())
	if code != 0 || strings.Contains(out.String()+errout.String(), "leaked") || !strings.Contains(out.String(), "18446744073709551615") || !strings.Contains(out.String(), "public-cas") {
		t.Fatalf("code=%d out=%s err=%s", code, &out, &errout)
	}
}
