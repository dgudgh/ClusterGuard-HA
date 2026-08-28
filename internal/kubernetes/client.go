package kubernetes

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"clusterguard.io/ha/pkg/model"
)

const (
	defaultRequestTimeout = 10 * time.Second
	maximumResponseBytes  = int64(4 << 20)
)

var ErrNotFound = errors.New("Kubernetes resource not found")

type APIError struct {
	StatusCode int
	Reason     string
}

func (err *APIError) Error() string {
	if err == nil {
		return "Kubernetes API request failed"
	}
	if err.Reason != "" {
		return fmt.Sprintf("Kubernetes API request failed with status %d: %s", err.StatusCode, err.Reason)
	}
	return fmt.Sprintf("Kubernetes API request failed with status %d", err.StatusCode)
}

func (err *APIError) Is(target error) bool {
	return target == ErrNotFound && err != nil && err.StatusCode == http.StatusNotFound
}

type TypeMeta struct {
	APIVersion string `json:"apiVersion,omitempty"`
	Kind       string `json:"kind,omitempty"`
}

type OwnerReference struct {
	APIVersion string `json:"apiVersion,omitempty"`
	Kind       string `json:"kind,omitempty"`
	Name       string `json:"name,omitempty"`
	UID        string `json:"uid,omitempty"`
	Controller *bool  `json:"controller,omitempty"`
}

type ObjectMeta struct {
	Name              string            `json:"name,omitempty"`
	Namespace         string            `json:"namespace,omitempty"`
	UID               string            `json:"uid,omitempty"`
	ResourceVersion   string            `json:"resourceVersion,omitempty"`
	Labels            map[string]string `json:"labels,omitempty"`
	Annotations       map[string]string `json:"annotations,omitempty"`
	OwnerReferences   []OwnerReference  `json:"ownerReferences,omitempty"`
	DeletionTimestamp *time.Time        `json:"deletionTimestamp,omitempty"`
}

type ServicePort struct {
	Name     string `json:"name,omitempty"`
	Protocol string `json:"protocol,omitempty"`
	Port     int32  `json:"port"`
}

type Service struct {
	TypeMeta `json:",inline"`
	Metadata ObjectMeta `json:"metadata"`
	Spec     struct {
		Selector  map[string]string `json:"selector,omitempty"`
		ClusterIP string            `json:"clusterIP,omitempty"`
		Ports     []ServicePort     `json:"ports,omitempty"`
	} `json:"spec"`
}

type ObjectReference struct {
	Kind      string `json:"kind,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name,omitempty"`
	UID       string `json:"uid,omitempty"`
}

type EndpointConditions struct {
	Ready       *bool `json:"ready,omitempty"`
	Serving     *bool `json:"serving,omitempty"`
	Terminating *bool `json:"terminating,omitempty"`
}

type DiscoveryEndpoint struct {
	Addresses  []string           `json:"addresses"`
	Conditions EndpointConditions `json:"conditions,omitempty"`
	Hostname   *string            `json:"hostname,omitempty"`
	NodeName   *string            `json:"nodeName,omitempty"`
	TargetRef  *ObjectReference   `json:"targetRef,omitempty"`
}

type EndpointPort struct {
	Name        *string `json:"name,omitempty"`
	Protocol    *string `json:"protocol,omitempty"`
	Port        *int32  `json:"port,omitempty"`
	AppProtocol *string `json:"appProtocol,omitempty"`
}

type EndpointSlice struct {
	TypeMeta    `json:",inline"`
	Metadata    ObjectMeta          `json:"metadata"`
	AddressType string              `json:"addressType"`
	Endpoints   []DiscoveryEndpoint `json:"endpoints"`
	Ports       []EndpointPort      `json:"ports,omitempty"`
}

type PodCondition struct {
	Type   string `json:"type"`
	Status string `json:"status"`
}

type Pod struct {
	TypeMeta `json:",inline"`
	Metadata ObjectMeta `json:"metadata"`
	Spec     struct {
		NodeName string   `json:"nodeName,omitempty"`
		Volumes  []Volume `json:"volumes,omitempty"`
	} `json:"spec"`
	Status struct {
		Phase      string         `json:"phase,omitempty"`
		PodIP      string         `json:"podIP,omitempty"`
		Conditions []PodCondition `json:"conditions,omitempty"`
	} `json:"status"`
}

type PersistentVolumeClaimVolumeSource struct {
	ClaimName string `json:"claimName"`
}

type Volume struct {
	Name                  string                             `json:"name"`
	PersistentVolumeClaim *PersistentVolumeClaimVolumeSource `json:"persistentVolumeClaim,omitempty"`
}

type PersistentVolumeClaim struct {
	TypeMeta `json:",inline"`
	Metadata ObjectMeta `json:"metadata"`
}

type NodeCondition struct {
	Type   string `json:"type"`
	Status string `json:"status"`
}

type Node struct {
	TypeMeta `json:",inline"`
	Metadata ObjectMeta `json:"metadata"`
	Status   struct {
		Conditions []NodeCondition `json:"conditions,omitempty"`
	} `json:"status"`
}

type Scale struct {
	TypeMeta `json:",inline"`
	Metadata ObjectMeta `json:"metadata"`
	Spec     struct {
		Replicas int32 `json:"replicas"`
	} `json:"spec"`
	Status struct {
		Replicas int32 `json:"replicas"`
	} `json:"status,omitempty"`
}

type StatefulSet struct {
	TypeMeta `json:",inline"`
	Metadata ObjectMeta `json:"metadata"`
	Spec     struct {
		Replicas *int32 `json:"replicas,omitempty"`
	} `json:"spec"`
}

type API interface {
	GetService(context.Context, string, string) (Service, error)
	GetEndpointSlice(context.Context, string, string) (EndpointSlice, error)
	UpdateEndpointSlice(context.Context, EndpointSlice) (EndpointSlice, error)
	GetPod(context.Context, string, string) (Pod, error)
	GetPersistentVolumeClaim(context.Context, string, string) (PersistentVolumeClaim, error)
	GetNode(context.Context, string) (Node, error)
	GetStatefulSet(context.Context, string, string) (StatefulSet, error)
	PatchStatefulSetAnnotations(context.Context, string, string, map[string]string) (StatefulSet, error)
	GetStatefulSetScale(context.Context, string, string) (Scale, error)
	UpdateStatefulSetScale(context.Context, Scale) (Scale, error)
}

type Factory interface {
	ForTarget(context.Context, model.RuntimeTarget) (API, error)
}

type CredentialProfile struct {
	BearerTokenFile string `json:"bearer_token_file,omitempty"`
	CAFile          string `json:"ca_file"`
	ClientCertFile  string `json:"client_cert_file,omitempty"`
	ClientKeyFile   string `json:"client_key_file,omitempty"`
	ServerName      string `json:"server_name,omitempty"`
}

type ClientFactory struct {
	Timeout time.Duration
}

func (factory ClientFactory) ForTarget(_ context.Context, target model.RuntimeTarget) (API, error) {
	if target.Kind != model.RuntimeKubernetes || !target.Active {
		return nil, fmt.Errorf("runtime target is not an active Kubernetes target")
	}
	endpoint, err := url.Parse(strings.TrimSpace(target.Endpoint))
	if err != nil || endpoint.Scheme != "https" || endpoint.Host == "" || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return nil, fmt.Errorf("Kubernetes API endpoint must be an HTTPS origin")
	}
	if endpoint.Path != "" && endpoint.Path != "/" {
		return nil, fmt.Errorf("Kubernetes API endpoint cannot contain a path")
	}
	credentialPath := strings.TrimSpace(target.CredentialRef)
	if !filepath.IsAbs(credentialPath) {
		return nil, fmt.Errorf("Kubernetes credential reference must be an absolute file path")
	}
	profile, err := readCredentialProfile(credentialPath)
	if err != nil {
		return nil, err
	}
	caContents, err := os.ReadFile(profile.CAFile)
	if err != nil {
		return nil, fmt.Errorf("read Kubernetes CA file: %w", err)
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(caContents) {
		return nil, fmt.Errorf("Kubernetes CA file contains no certificates")
	}
	tlsConfiguration := &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool, ServerName: strings.TrimSpace(profile.ServerName)}
	if profile.ClientCertFile != "" {
		certificate, err := tls.LoadX509KeyPair(profile.ClientCertFile, profile.ClientKeyFile)
		if err != nil {
			return nil, fmt.Errorf("load Kubernetes client certificate: %w", err)
		}
		tlsConfiguration.Certificates = []tls.Certificate{certificate}
	}
	timeout := factory.Timeout
	if timeout <= 0 {
		timeout = defaultRequestTimeout
	}
	client := &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			TLSClientConfig:       tlsConfiguration,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          16,
			MaxIdleConnsPerHost:   8,
			IdleConnTimeout:       30 * time.Second,
			TLSHandshakeTimeout:   5 * time.Second,
			ResponseHeaderTimeout: timeout,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	endpoint.Path = ""
	return &Client{baseURL: endpoint, httpClient: client, bearerTokenFile: profile.BearerTokenFile}, nil
}

func readCredentialProfile(path string) (CredentialProfile, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return CredentialProfile{}, fmt.Errorf("read Kubernetes credential profile: %w", err)
	}
	if len(contents) > 64*1024 {
		return CredentialProfile{}, fmt.Errorf("Kubernetes credential profile is too large")
	}
	profile := CredentialProfile{}
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&profile); err != nil {
		return CredentialProfile{}, fmt.Errorf("decode Kubernetes credential profile: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return CredentialProfile{}, fmt.Errorf("Kubernetes credential profile contains multiple JSON values")
	}
	profile.BearerTokenFile = strings.TrimSpace(profile.BearerTokenFile)
	profile.CAFile = strings.TrimSpace(profile.CAFile)
	profile.ClientCertFile = strings.TrimSpace(profile.ClientCertFile)
	profile.ClientKeyFile = strings.TrimSpace(profile.ClientKeyFile)
	profile.ServerName = strings.TrimSpace(profile.ServerName)
	if !filepath.IsAbs(profile.CAFile) {
		return CredentialProfile{}, fmt.Errorf("Kubernetes credential profile requires an absolute CA file")
	}
	if profile.BearerTokenFile == "" && profile.ClientCertFile == "" {
		return CredentialProfile{}, fmt.Errorf("Kubernetes credential profile requires bearer token or client certificate authentication")
	}
	if profile.BearerTokenFile != "" && !filepath.IsAbs(profile.BearerTokenFile) {
		return CredentialProfile{}, fmt.Errorf("Kubernetes bearer token file must be absolute")
	}
	if (profile.ClientCertFile == "") != (profile.ClientKeyFile == "") ||
		(profile.ClientCertFile != "" && (!filepath.IsAbs(profile.ClientCertFile) || !filepath.IsAbs(profile.ClientKeyFile))) {
		return CredentialProfile{}, fmt.Errorf("Kubernetes client certificate and key paths must be absolute and configured together")
	}
	return profile, nil
}

type Client struct {
	baseURL         *url.URL
	httpClient      *http.Client
	bearerTokenFile string
}

func namespacedPath(group, version, namespace, resource, name string) string {
	prefix := "/api/" + version
	if group != "" {
		prefix = "/apis/" + url.PathEscape(group) + "/" + url.PathEscape(version)
	}
	return prefix + "/namespaces/" + url.PathEscape(namespace) + "/" + url.PathEscape(resource) + "/" + url.PathEscape(name)
}

func (client *Client) request(ctx context.Context, method, path string, body interface{}, result interface{}) error {
	return client.requestWithContentType(ctx, method, path, body, result, "application/json")
}

func (client *Client) requestWithContentType(ctx context.Context, method, path string, body interface{}, result interface{}, contentType string) error {
	if client == nil || client.baseURL == nil || client.httpClient == nil || !strings.HasPrefix(path, "/") {
		return fmt.Errorf("Kubernetes client is not configured")
	}
	var requestBody io.Reader
	if body != nil {
		contents, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode Kubernetes API request: %w", err)
		}
		requestBody = bytes.NewReader(contents)
	}
	target := *client.baseURL
	target.Path = path
	request, err := http.NewRequestWithContext(ctx, method, target.String(), requestBody)
	if err != nil {
		return fmt.Errorf("create Kubernetes API request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", contentType)
	}
	if client.bearerTokenFile != "" {
		token, err := os.ReadFile(client.bearerTokenFile)
		if err != nil {
			return fmt.Errorf("read Kubernetes bearer token: %w", err)
		}
		value := strings.TrimSpace(string(token))
		if value == "" || strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("Kubernetes bearer token is empty or malformed")
		}
		request.Header.Set("Authorization", "Bearer "+value)
	}
	response, err := client.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("call Kubernetes API: %w", err)
	}
	defer response.Body.Close()
	contents, err := io.ReadAll(io.LimitReader(response.Body, maximumResponseBytes+1))
	if err != nil {
		return fmt.Errorf("read Kubernetes API response: %w", err)
	}
	if int64(len(contents)) > maximumResponseBytes {
		return fmt.Errorf("Kubernetes API response is too large")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		status := struct {
			Reason  string `json:"reason"`
			Message string `json:"message"`
		}{}
		_ = json.Unmarshal(contents, &status)
		reason := strings.TrimSpace(status.Reason)
		if reason == "" {
			reason = http.StatusText(response.StatusCode)
		}
		return &APIError{StatusCode: response.StatusCode, Reason: reason}
	}
	if result == nil || len(bytes.TrimSpace(contents)) == 0 {
		return nil
	}
	if err := json.Unmarshal(contents, result); err != nil {
		return fmt.Errorf("decode Kubernetes API response: %w", err)
	}
	return nil
}

func (client *Client) GetService(ctx context.Context, namespace, name string) (Service, error) {
	result := Service{}
	err := client.request(ctx, http.MethodGet, namespacedPath("", "v1", namespace, "services", name), nil, &result)
	return result, err
}

func (client *Client) GetEndpointSlice(ctx context.Context, namespace, name string) (EndpointSlice, error) {
	result := EndpointSlice{}
	err := client.request(ctx, http.MethodGet, namespacedPath("discovery.k8s.io", "v1", namespace, "endpointslices", name), nil, &result)
	return result, err
}

func (client *Client) UpdateEndpointSlice(ctx context.Context, value EndpointSlice) (EndpointSlice, error) {
	result := EndpointSlice{}
	err := client.request(ctx, http.MethodPut, namespacedPath("discovery.k8s.io", "v1", value.Metadata.Namespace, "endpointslices", value.Metadata.Name), value, &result)
	return result, err
}

func (client *Client) GetPod(ctx context.Context, namespace, name string) (Pod, error) {
	result := Pod{}
	err := client.request(ctx, http.MethodGet, namespacedPath("", "v1", namespace, "pods", name), nil, &result)
	return result, err
}

func (client *Client) GetPersistentVolumeClaim(ctx context.Context, namespace, name string) (PersistentVolumeClaim, error) {
	result := PersistentVolumeClaim{}
	err := client.request(ctx, http.MethodGet, namespacedPath("", "v1", namespace, "persistentvolumeclaims", name), nil, &result)
	return result, err
}

func VerifyPodPVCIdentity(ctx context.Context, api API, pod Pod, expectedUID string) error {
	expectedUID = strings.TrimSpace(expectedUID)
	if expectedUID == "" {
		return fmt.Errorf("registered Kubernetes PVC UID is empty")
	}
	foundClaim := false
	for _, volume := range pod.Spec.Volumes {
		if volume.PersistentVolumeClaim == nil || strings.TrimSpace(volume.PersistentVolumeClaim.ClaimName) == "" {
			continue
		}
		foundClaim = true
		name := strings.TrimSpace(volume.PersistentVolumeClaim.ClaimName)
		claim, err := api.GetPersistentVolumeClaim(ctx, pod.Metadata.Namespace, name)
		if err != nil {
			return fmt.Errorf("read Kubernetes PVC %s: %w", name, err)
		}
		if claim.Metadata.Namespace != pod.Metadata.Namespace || claim.Metadata.Name != name || claim.Metadata.DeletionTimestamp != nil {
			return fmt.Errorf("Kubernetes PVC %s identity is invalid", name)
		}
		if claim.Metadata.UID == expectedUID {
			return nil
		}
	}
	if !foundClaim {
		return fmt.Errorf("Kubernetes Pod has no persistent volume claim")
	}
	return fmt.Errorf("Kubernetes Pod is not attached to the registered PVC UID")
}

func (client *Client) GetNode(ctx context.Context, name string) (Node, error) {
	result := Node{}
	err := client.request(ctx, http.MethodGet, "/api/v1/nodes/"+url.PathEscape(name), nil, &result)
	return result, err
}

func (client *Client) GetStatefulSet(ctx context.Context, namespace, name string) (StatefulSet, error) {
	result := StatefulSet{}
	err := client.request(ctx, http.MethodGet, namespacedPath("apps", "v1", namespace, "statefulsets", name), nil, &result)
	return result, err
}

func (client *Client) PatchStatefulSetAnnotations(ctx context.Context, namespace, name string, annotations map[string]string) (StatefulSet, error) {
	result := StatefulSet{}
	body := map[string]interface{}{"metadata": map[string]interface{}{"annotations": annotations}}
	err := client.requestWithContentType(ctx, http.MethodPatch, namespacedPath("apps", "v1", namespace, "statefulsets", name), body, &result, "application/merge-patch+json")
	return result, err
}

func (client *Client) GetStatefulSetScale(ctx context.Context, namespace, name string) (Scale, error) {
	result := Scale{}
	path := namespacedPath("apps", "v1", namespace, "statefulsets", name) + "/scale"
	err := client.request(ctx, http.MethodGet, path, nil, &result)
	return result, err
}

func (client *Client) UpdateStatefulSetScale(ctx context.Context, value Scale) (Scale, error) {
	result := Scale{}
	path := namespacedPath("apps", "v1", value.Metadata.Namespace, "statefulsets", value.Metadata.Name) + "/scale"
	err := client.request(ctx, http.MethodPut, path, value, &result)
	return result, err
}
