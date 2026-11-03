package runtime

import (
	"clusterguard.io/ha/pkg/model"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func newInstanceFailoverPeerCheck(membership consensusMembershipReader, client *http.Client, token string, allowHTTP bool) func(context.Context, model.ResourceID, model.Engine, bool) error {
	return func(ctx context.Context, id model.ResourceID, engine model.Engine, enabling bool) error {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if membership == nil || client == nil || token == "" {
			return fmt.Errorf("instance failover peer verification is unavailable")
		}
		// A capability response must come from the registered controller itself.
		peerClient := *client
		peerClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		members, err := membership.ControllerMembers(ctx)
		if err != nil {
			return err
		}
		if len(members) == 0 {
			return fmt.Errorf("instance failover voter inventory is unavailable")
		}
		seen := map[model.ResourceID]bool{}
		for _, member := range members {
			if !model.ValidResourceID(member.ResourceID) || seen[member.ResourceID] {
				return fmt.Errorf("instance failover voter identity is invalid")
			}
			seen[member.ResourceID] = true
			endpoint, err := url.Parse(strings.TrimRight(member.APIAddress, "/"))
			if err != nil || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || (endpoint.Scheme != "https" && !(allowHTTP && endpoint.Scheme == "http")) {
				return fmt.Errorf("instance failover voter API is unavailable")
			}
			endpoint.Path = "/api/v1/clusters/" + string(id) + "/failover-policy"
			request, err := http.NewRequestWithContext(ctx, "GET", endpoint.String(), nil)
			if err != nil {
				return err
			}
			request.Header.Set("Authorization", "Bearer "+token)
			response, err := peerClient.Do(request)
			if err != nil {
				return fmt.Errorf("instance failover voter could not be verified: %s", member.ResourceID)
			}
			var payload struct {
				Result struct {
					ProtocolVersion    int              `json:"protocol_version"`
					ControllerID       model.ResourceID `json:"controller_id"`
					ClusterID          model.ResourceID `json:"cluster_id"`
					AutomaticAvailable bool             `json:"automatic_available"`
				} `json:"result"`
			}
			decodeErr := json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(&payload)
			response.Body.Close()
			if response.StatusCode != 200 || decodeErr != nil || payload.Result.ProtocolVersion != 1 || payload.Result.ControllerID != member.ResourceID || payload.Result.ClusterID != id {
				return fmt.Errorf("all voters must support the instance failover policy protocol")
			}
			if enabling && !payload.Result.AutomaticAvailable {
				return fmt.Errorf("automatic failover prerequisites are unavailable on a voter")
			}
		}
		return nil
	}
}
