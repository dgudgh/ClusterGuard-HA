package api

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"clusterguard.io/ha/internal/controlstate"
)

const (
	mutationRPCForwardedHeader = "X-ClusterGuard-Mutation-RPC-Forwarded"
	mutationRPCRevisionHeader  = "X-ClusterGuard-Metadata-Revision"
)

type MutationRevisionReader interface {
	StateRevision() uint64
}

type MutationRPC interface {
	Forward(http.ResponseWriter, *http.Request, string) error
}

type LeaderMutationRPCClient struct {
	client    *http.Client
	revisions MutationRevisionReader
}

func NewLeaderMutationRPCClient(client *http.Client, revisions ...MutationRevisionReader) *LeaderMutationRPCClient {
	if client == nil {
		client = http.DefaultClient
	}
	isolatedClient := *client
	isolatedClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if isolatedClient.Timeout <= 0 {
		isolatedClient.Timeout = 10 * time.Minute
	}
	result := &LeaderMutationRPCClient{client: &isolatedClient}
	if len(revisions) > 0 {
		result.revisions = revisions[0]
	}
	return result
}

func (client *LeaderMutationRPCClient) Forward(writer http.ResponseWriter, request *http.Request, leaderAPIAddress string) error {
	leader, err := url.Parse(strings.TrimSpace(leaderAPIAddress))
	if err != nil || leader == nil || (leader.Scheme != "http" && leader.Scheme != "https") || leader.Host == "" || leader.User != nil {
		return fmt.Errorf("invalid leader API address")
	}
	target := &url.URL{
		Scheme: leader.Scheme, Host: leader.Host,
		Path: request.URL.Path, RawPath: request.URL.RawPath, RawQuery: request.URL.RawQuery,
	}
	var body []byte
	if request.Body != nil {
		body, err = io.ReadAll(io.LimitReader(request.Body, maximumJSONBodyBytes+1))
		if err != nil {
			return fmt.Errorf("read mutation RPC request: %w", err)
		}
		if len(body) > maximumJSONBodyBytes {
			return fmt.Errorf("mutation RPC request exceeds maximum size")
		}
	}
	forwarded, err := http.NewRequestWithContext(request.Context(), request.Method, target.String(), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create mutation RPC request: %w", err)
	}
	copyRPCHeaders(forwarded.Header, request.Header)
	forwarded.Header.Set(mutationRPCForwardedHeader, "1")
	forwarded.ContentLength = int64(len(body))

	response, err := client.client.Do(forwarded)
	if err != nil {
		return fmt.Errorf("call leader mutation RPC: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode >= 300 && response.StatusCode < 400 {
		return fmt.Errorf("leader mutation RPC returned an unsafe redirect")
	}
	localRevision, err := client.waitForMetadataRevision(request, response.StatusCode, response.Header.Get(mutationRPCRevisionHeader))
	if err != nil {
		return err
	}
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, controlstate.MaximumBytes+1))
	if err != nil {
		return fmt.Errorf("read leader mutation RPC response: %w", err)
	}
	if len(responseBody) > controlstate.MaximumBytes {
		return fmt.Errorf("leader mutation RPC response exceeds maximum size")
	}
	copyRPCHeaders(writer.Header(), response.Header)
	writer.Header().Set("X-ClusterGuard-Mutation-RPC-Leader", strings.TrimRight(leaderAPIAddress, "/"))
	if localRevision != "" {
		writer.Header().Set("X-ClusterGuard-Local-Metadata-Revision", localRevision)
	}
	writer.WriteHeader(response.StatusCode)
	_, _ = writer.Write(responseBody)
	return nil
}

func (client *LeaderMutationRPCClient) waitForMetadataRevision(request *http.Request, status int, value string) (string, error) {
	if status < 200 || status >= 300 {
		if client.revisions == nil {
			return "", nil
		}
		return strconv.FormatUint(client.revisions.StateRevision(), 10), nil
	}
	if client.revisions == nil {
		return "", fmt.Errorf("successful leader mutation RPC requires a local metadata revision reader")
	}
	target, err := strconv.ParseUint(strings.TrimSpace(value), 10, 64)
	if err != nil || target == 0 {
		return strconv.FormatUint(client.revisions.StateRevision(), 10), fmt.Errorf("successful leader mutation RPC is missing valid metadata revision evidence")
	}
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		current := client.revisions.StateRevision()
		if current >= target {
			return strconv.FormatUint(current, 10), nil
		}
		select {
		case <-request.Context().Done():
			return strconv.FormatUint(current, 10), fmt.Errorf(
				"wait for follower metadata revision %d (current %d): %w", target, current, request.Context().Err(),
			)
		case <-deadline.C:
			return strconv.FormatUint(current, 10), fmt.Errorf(
				"wait for follower metadata revision %d timed out at revision %d", target, current,
			)
		case <-ticker.C:
		}
	}
}

type mutationRPCRevisionWriter struct {
	http.ResponseWriter
	revisions MutationRevisionReader
	wrote     bool
}

func (writer *mutationRPCRevisionWriter) WriteHeader(status int) {
	if writer.wrote {
		return
	}
	writer.wrote = true
	writer.Header().Set(mutationRPCRevisionHeader, strconv.FormatUint(writer.revisions.StateRevision(), 10))
	writer.ResponseWriter.WriteHeader(status)
}

func (writer *mutationRPCRevisionWriter) Write(contents []byte) (int, error) {
	if !writer.wrote {
		writer.WriteHeader(http.StatusOK)
	}
	return writer.ResponseWriter.Write(contents)
}

func copyRPCHeaders(destination, source http.Header) {
	connectionHeaders := make(map[string]struct{})
	for _, value := range source.Values("Connection") {
		for _, name := range strings.Split(value, ",") {
			if name = strings.TrimSpace(name); name != "" {
				connectionHeaders[http.CanonicalHeaderKey(name)] = struct{}{}
			}
		}
	}
	for name, values := range source {
		canonical := http.CanonicalHeaderKey(name)
		if _, blocked := connectionHeaders[canonical]; blocked || hopByHopRPCHeader(canonical) {
			continue
		}
		for _, value := range values {
			destination.Add(canonical, value)
		}
	}
}

func hopByHopRPCHeader(name string) bool {
	switch http.CanonicalHeaderKey(name) {
	case "Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "Proxy-Connection", "Te", "Trailer", "Transfer-Encoding", "Upgrade":
		return true
	default:
		return false
	}
}
