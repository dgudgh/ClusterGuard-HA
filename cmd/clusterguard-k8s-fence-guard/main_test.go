package main

import (
	"testing"

	"clusterguard.io/ha/internal/kubernetes"
)

func TestStatefulSetStartAllowedFailsClosed(t *testing.T) {
	value := kubernetes.StatefulSet{}
	replicas := int32(1)
	value.Spec.Replicas = &replicas
	for name, annotations := range map[string]map[string]string{
		"missing guard": nil,
		"fenced":        {fenceGuardAnnotation: "enabled", fencedAnnotation: "true"},
	} {
		t.Run(name, func(t *testing.T) {
			value.Metadata.Annotations = annotations
			if err := statefulSetStartAllowed(value); err == nil {
				t.Fatal("unsafe StatefulSet start was allowed")
			}
		})
	}
	value.Metadata.Annotations = map[string]string{fenceGuardAnnotation: "enabled", fencedAnnotation: "false"}
	if err := statefulSetStartAllowed(value); err != nil {
		t.Fatalf("unfenced guarded StatefulSet was blocked: %v", err)
	}
	value.Metadata.Annotations[mysqlRoleAnnotation] = "replica"
	contents, err := mysqlRoleConfiguration(value)
	if err != nil || string(contents) != "[mysqld]\nread_only=ON\nsuper_read_only=ON\n" {
		t.Fatalf("replica configuration=%q err=%v", contents, err)
	}
	value.Metadata.Annotations[mysqlRoleAnnotation] = "primary"
	contents, err = mysqlRoleConfiguration(value)
	if err != nil || string(contents) != "[mysqld]\nread_only=OFF\nsuper_read_only=OFF\n" {
		t.Fatalf("primary configuration=%q err=%v", contents, err)
	}
	replicas = 2
	if err := statefulSetStartAllowed(value); err == nil {
		t.Fatal("multi-replica StatefulSet was accepted by the start guard")
	}
}
