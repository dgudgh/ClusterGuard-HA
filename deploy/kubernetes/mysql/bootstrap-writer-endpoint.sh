#!/usr/bin/env bash
set -euo pipefail

namespace="${1:?usage: bootstrap-writer-endpoint.sh NAMESPACE POD_NAME [SLICE_NAME]}"
pod_name="${2:?usage: bootstrap-writer-endpoint.sh NAMESPACE POD_NAME [SLICE_NAME]}"
slice_name="${3:-mysql-writer-clusterguard}"

[[ "${namespace}" =~ ^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$ ]] || { echo "invalid namespace" >&2; exit 2; }
[[ "${pod_name}" =~ ^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$ ]] || { echo "invalid Pod name" >&2; exit 2; }
[[ "${slice_name}" =~ ^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$ ]] || { echo "invalid EndpointSlice name" >&2; exit 2; }

pod_uid="$(kubectl -n "${namespace}" get pod "${pod_name}" -o jsonpath='{.metadata.uid}')"
pod_ip="$(kubectl -n "${namespace}" get pod "${pod_name}" -o jsonpath='{.status.podIP}')"
node_name="$(kubectl -n "${namespace}" get pod "${pod_name}" -o jsonpath='{.spec.nodeName}')"
[[ -n "${pod_uid}" && -n "${pod_ip}" && -n "${node_name}" ]] || { echo "Pod identity is incomplete" >&2; exit 3; }

payload="$(printf '{\"endpoints\":[{\"addresses\":[\"%s\"],\"conditions\":{\"ready\":true,\"serving\":true,\"terminating\":false},\"nodeName\":\"%s\",\"targetRef\":{\"kind\":\"Pod\",\"namespace\":\"%s\",\"name\":\"%s\",\"uid\":\"%s\"}}]}' "${pod_ip}" "${node_name}" "${namespace}" "${pod_name}" "${pod_uid}")"
kubectl -n "${namespace}" patch endpointslice "${slice_name}" --type merge -p "${payload}"

printf 'writer EndpointSlice %s/%s now targets Pod %s (%s)\n' "${namespace}" "${slice_name}" "${pod_name}" "${pod_uid}"
