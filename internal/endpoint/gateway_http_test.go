package endpoint

import (
	"clusterguard.io/ha/pkg/model"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func stableHTTPGrant() model.GatewayGrant {
	return model.GatewayGrant{ClusterID: model.NewResourceID(), Epoch: 1, Sequence: 2, Mode: "activate", TargetID: model.NewResourceID(), LeaseID: model.NewResourceID(), OperationID: model.NewResourceID(), IssuedAt: time.Now().UTC(), ExpiresAt: time.Now().Add(time.Minute).UTC()}
}
func TestGatewayHTTPPartialRenewalIsNotAPublication(t *testing.T) {
	old := stableHTTPGrant()
	current := GatewayStatus{InstanceID: "process-a", ClusterID: old.ClusterID, Active: true, LastGrant: old}
	posts := 0
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			posts++
			var grant model.GatewayGrant
			if err := json.NewDecoder(r.Body).Decode(&grant); err != nil {
				t.Error(err)
			}
			if grant.Mode != "activate" {
				t.Error("partial renewal withdrew a healthy peer")
			}
			current.LastGrant = grant
		}
		json.NewEncoder(w).Encode(current)
	}))
	defer s.Close()
	offline, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := offline.Addr().String()
	offline.Close()
	client := &gatewayHTTP{urls: []string{"https://" + address, s.URL}, client: s.Client(), minimum: 2}
	observation, err := client.AvailableStatuses(context.Background(), old.ClusterID)
	if err != nil || observation.Unavailable != 1 || len(observation.Statuses) != 1 {
		t.Fatal(observation, err)
	}
	next := old
	next.Sequence++
	next.ExpiresAt = next.ExpiresAt.Add(time.Second)
	validations := 0
	n, err := client.RenewStable(context.Background(), next, observation.Statuses, func(context.Context) error { validations++; return nil })
	if n != 1 || err == nil || posts != 1 || validations < 2 || !sameGatewayGrant(current.LastGrant, next) {
		t.Fatalf("n=%d err=%v posts=%d validations=%d", n, err, posts, validations)
	}
}
func TestGatewayHTTPAvailabilityDoesNotHideSecurityOrProtocolErrors(t *testing.T) {
	for _, mode := range []string{"certificate", "forbidden", "malformed", "wrong-cluster"} {
		t.Run(mode, func(t *testing.T) {
			wanted := model.NewResourceID()
			s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch mode {
				case "forbidden":
					w.WriteHeader(403)
				case "malformed":
					w.Write([]byte("invalid"))
				default:
					json.NewEncoder(w).Encode(GatewayStatus{ClusterID: model.NewResourceID()})
				}
			}))
			defer s.Close()
			transport := s.Client()
			if mode == "certificate" {
				transport = &http.Client{Timeout: time.Second}
			}
			c := &gatewayHTTP{urls: []string{s.URL}, client: transport, minimum: 1}
			got, err := c.AvailableStatuses(context.Background(), wanted)
			if err == nil || got.Unavailable != 0 {
				t.Fatalf("%s was downgraded to an offline peer: %+v %v", mode, got, err)
			}
		})
	}
}
func TestGatewayHTTPRenewalNeverReactivatesUnknownState(t *testing.T) {
	for _, mode := range []string{"withdrawn", "restarted", "wrong-target", "future-sequence", "expired"} {
		t.Run(mode, func(t *testing.T) {
			g := stableHTTPGrant()
			state := GatewayStatus{InstanceID: "known", ClusterID: g.ClusterID, Active: true, LastGrant: g}
			expected := []GatewayStatus{state}
			posts := 0
			switch mode {
			case "withdrawn":
				state.Active = false
			case "restarted":
				state.InstanceID = "new-process"
			case "wrong-target":
				state.LastGrant.TargetID = model.NewResourceID()
			case "future-sequence":
				state.LastGrant.Sequence += 2
			case "expired":
				state.LastGrant.ExpiresAt = time.Now().Add(-time.Second)
			}
			s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					posts++
				}
				json.NewEncoder(w).Encode(state)
			}))
			defer s.Close()
			c := &gatewayHTTP{urls: []string{s.URL}, client: s.Client(), minimum: 1}
			expected[0].managementURL = s.URL
			g.Sequence++
			n, err := c.RenewStable(context.Background(), g, expected, func(context.Context) error { return nil })
			if err == nil || n != 0 || posts != 0 {
				t.Fatal("unsafe renewal performed network mutation", mode, n, err, posts)
			}
		})
	}
}
func TestSingleGatewayProfileRequiresExplicitOptIn(t *testing.T) {
	_, err := NewGatewayHTTP(GatewayProfile{URLs: []string{"https://127.0.0.1:9443"}})
	if err == nil || !strings.Contains(err.Error(), "2..8") {
		t.Fatal("singleton silently accepted", err)
	}
	_, err = NewGatewayHTTP(GatewayProfile{URLs: []string{"https://127.0.0.1:9443", "https://127.0.0.1:9444"}, AllowSingleGateway: true})
	if err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatal("singleton flag silently weakened a multi-node profile", err)
	}
}
