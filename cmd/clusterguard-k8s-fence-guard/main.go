package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"clusterguard.io/ha/internal/kubernetes"
	"clusterguard.io/ha/pkg/model"
)

const (
	fenceGuardAnnotation = "clusterguard.io/fence-guard"
	fencedAnnotation     = "clusterguard.io/fenced"
	mysqlRoleAnnotation  = "clusterguard.io/mysql-role"
)

func statefulSetStartAllowed(value kubernetes.StatefulSet) error {
	if value.Metadata.Annotations[fenceGuardAnnotation] != "enabled" {
		return fmt.Errorf("StatefulSet does not opt in to the ClusterGuard fail-closed fence guard")
	}
	if strings.EqualFold(strings.TrimSpace(value.Metadata.Annotations[fencedAnnotation]), "true") {
		return fmt.Errorf("StatefulSet is fenced by ClusterGuard")
	}
	if value.Spec.Replicas == nil || *value.Spec.Replicas != 1 {
		return fmt.Errorf("StatefulSet must be a dedicated one-replica database workload")
	}
	return nil
}

func writeRoleConfiguration(path string, contents []byte) error {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".clusterguard-role-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0644); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(contents); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	if handle, err := os.Open(directory); err == nil {
		_ = handle.Sync()
		_ = handle.Close()
	}
	return nil
}

func mysqlRoleConfiguration(value kubernetes.StatefulSet) ([]byte, error) {
	role := strings.ToLower(strings.TrimSpace(value.Metadata.Annotations[mysqlRoleAnnotation]))
	switch role {
	case "primary":
		return []byte("[mysqld]\nread_only=OFF\nsuper_read_only=OFF\n"), nil
	case "replica":
		return []byte("[mysqld]\nread_only=ON\nsuper_read_only=ON\n"), nil
	default:
		return nil, fmt.Errorf("StatefulSet has no valid durable MySQL role")
	}
}

func run() error {
	apiEndpoint := flag.String("api", "https://kubernetes.default.svc", "Kubernetes API HTTPS origin")
	credentialProfile := flag.String("credentials", "/etc/clusterguard/kubernetes-credentials.json", "credential profile path")
	namespace := flag.String("namespace", os.Getenv("POD_NAMESPACE"), "StatefulSet namespace")
	statefulSet := flag.String("statefulset", os.Getenv("STATEFULSET_NAME"), "StatefulSet name")
	podName := flag.String("pod", os.Getenv("POD_NAME"), "ordinal-zero Pod name used to derive the StatefulSet")
	timeout := flag.Duration("timeout", 10*time.Second, "API request timeout")
	mysqlConfig := flag.String("mysql-config", "", "optional generated MySQL role configuration path")
	flag.Parse()
	if strings.TrimSpace(*statefulSet) == "" && strings.HasSuffix(strings.TrimSpace(*podName), "-0") {
		*statefulSet = strings.TrimSuffix(strings.TrimSpace(*podName), "-0")
	}
	if strings.TrimSpace(*namespace) == "" || strings.TrimSpace(*statefulSet) == "" {
		return fmt.Errorf("namespace and StatefulSet name are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	client, err := (kubernetes.ClientFactory{Timeout: *timeout}).ForTarget(ctx, model.RuntimeTarget{
		Kind: model.RuntimeKubernetes, Endpoint: *apiEndpoint, CredentialRef: *credentialProfile, Active: true,
	})
	if err != nil {
		return err
	}
	value, err := client.GetStatefulSet(ctx, strings.TrimSpace(*namespace), strings.TrimSpace(*statefulSet))
	if err != nil {
		return fmt.Errorf("read StatefulSet fence state: %w", err)
	}
	if err := statefulSetStartAllowed(value); err != nil {
		return err
	}
	if strings.TrimSpace(*mysqlConfig) != "" {
		contents, err := mysqlRoleConfiguration(value)
		if err != nil {
			return err
		}
		if err := writeRoleConfiguration(*mysqlConfig, contents); err != nil {
			return fmt.Errorf("publish MySQL role configuration: %w", err)
		}
	}
	return nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "ClusterGuard Kubernetes start guard blocked database startup:", err)
		os.Exit(1)
	}
}
