package main

import "testing"

func TestEndpointForReadCommands(t *testing.T) {
	tests := []struct {
		arguments []string
		want      string
	}{
		{[]string{"engines"}, "/api/v1/engines"},
		{[]string{"clusters"}, "/api/v1/clusters"},
		{[]string{"topology", "cluster-1"}, "/api/v1/clusters/cluster-1/topology"},
		{[]string{"health", "cluster-1"}, "/api/v1/clusters/cluster-1/health"},
	}
	for _, test := range tests {
		got, err := endpointFor(test.arguments)
		if err != nil || got != test.want {
			t.Fatalf("endpointFor(%v) = %q, %v; want %q", test.arguments, got, err, test.want)
		}
	}
}
