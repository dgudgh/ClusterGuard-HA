# ClusterGuard HA 接管 Kubernetes MySQL

## 1. 当前交付范围

ClusterGuard HA 2.2 首批开放 Kubernetes 上 MySQL 的计划切换和受保护故障切换。数据库复制仍由 MySQL GTID 管理，Kubernetes 管理 Pod 生命周期，ClusterGuard 管理数据库角色、持久重启角色、旧主隔离、Service 后端、Raft 租约、审计和验证。

本实现不修改 CoreDNS，也不把宿主机 VIP 绑进 Pod。集群内应用连接固定的 selectorless Service；集群外应用连接固定的 LoadBalancer IP 或由 MetalLB 提供的地址。切换时只更新该 Service 对应的 EndpointSlice 后端。

PostgreSQL 的 Kubernetes 入口模型可以登记，但 PostgreSQL Pod 角色控制器尚未开放执行，因此当前会 fail-closed，不应按本文启用自动故障切换。

## 2. 三种运行模式

| 模式 | 数据库进程 | 业务入口 | ClusterGuard 迁移动作 |
| --- | --- | --- | --- |
| 物理机或虚拟机 | systemd/宿主机进程 | 宿主机 VIP | 迁移网卡 VIP |
| Docker Swarm | 固定宿主机上的 Service | 宿主机 VIP | 固化容器只读角色并迁移宿主机 VIP |
| Kubernetes | 独立单副本 StatefulSet Pod | selectorless Service | 固化 StatefulSet 角色并迁移 EndpointSlice |

RuntimeTarget 和 WorkloadBinding 是强制边界标记。平台不会根据端口、主机名或容器名称猜测运行模式。

## 3. 强制安全约束

1. 每个数据库实例使用独立的单副本 StatefulSet，ordinal 必须为 0。禁止用一个三副本 StatefulSet 表示一主两从，因为缩容旧主会同时影响其他实例。
2. 每个实例必须有独立 PVC，数据库 `server_uuid` 不因 Pod 重建改变。
3. WorkloadBinding 必须登记 StatefulSet、Pod 名、不可变 Pod UID、Node 名和 PVC UID。
4. writer Service 必须没有 selector；EndpointSlice 必须标记 `endpointslice.kubernetes.io/managed-by=clusterguard.io/ha`，并带有 `kubernetes.io/service-name=<writer Service 名>` 标签且 `addressType: IPv4`，否则控制面会拒绝该切片。
5. StatefulSet 必须启用启动守卫（注解 `clusterguard.io/fence-guard=enabled`，初始 `clusterguard.io/fenced=false`），并登记 `clusterguard.io/mysql-role=primary|replica`。缺少 `fence-guard` 注解的 Pod 会被拒绝启动。
6. Controller 在数据库命名空间内只取得 `get` Service/Pod/PVC、`get/update` EndpointSlice、`get/patch` StatefulSet 和 `get/update` scale 子资源权限；集群级权限只有 `get Node`。
7. Kubernetes API 必须使用 HTTPS、CA 校验和短期 ServiceAccount token 或客户端证书。token 文件每次请求都会重新读取，支持轮换。
8. 数据库 Pod 禁止自动挂载 ServiceAccount token；短期 token 只投影给启动守卫 init container，MySQL 主容器不可见。

## 4. 切换顺序

计划切换执行以下顺序：

1. 验证 MySQL 身份、GTID、复制状态、Raft 多数派和 EndpointSlice 唯一所有者。
2. 将旧主实时设置为只读并等待目标追平。
3. 提升目标 MySQL 为可写主库。
4. 把旧主 StatefulSet 持久角色写为 `replica`，目标写为 `primary`。
5. 使用带 `resourceVersion` 的 PUT 把 EndpointSlice 原子改为目标 Pod UID 和 Pod IP。
6. 验证目标可写、Service 只有一个后端、旧主只读及 Raft 所有权元数据一致。

故障切换会先给旧主写入 `clusterguard.io/fenced=true`，再把该独立 StatefulSet 缩到 0。只有在 Pod 已消失、scale 状态为 0、旧主 Node 仍为 Ready 且 Raft 目标租约有效时才允许提升候选。Node 为 NotReady 或 API 不可达时，平台不会把“未知”伪造成“已隔离”，必须配置额外的云平台、BMC 或虚拟化节点隔离能力。

## 5. 部署资源

RPM 安装后，资源位于 `/usr/share/clusterguard/kubernetes/`；源码目录为 `deploy/kubernetes/`。

```bash
kubectl apply -f deploy/kubernetes/clusterguard-rbac.yaml
kubectl apply -f deploy/kubernetes/mysql/fence-guard-rbac.yaml
kubectl apply -f deploy/kubernetes/mysql/writer-service.yaml
```

示例 Role 固定在 `database` 命名空间。数据库位于其他命名空间时，应复制该 Role 和 RoleBinding 并修改命名空间，不能把写权限扩大为全局 ClusterRole。

构建并预加载启动守卫镜像：

```bash
docker build -f deploy/kubernetes/fence-guard/Dockerfile \
  -t clusterguard/k8s-fence-guard:2.2.0 .
```

复制 `statefulset-fence-guard-patch.yaml`，替换 StatefulSet 名和 MySQL 容器名。当前主库设置 `clusterguard.io/mysql-role=primary`，两个从库设置为 `replica`。补丁会把生成的 `99-clusterguard-role.cnf` 挂载到 `/etc/mysql/conf.d/`。必须先在测试命名空间确认所用 MySQL 镜像会读取该目录。

初始化 writer EndpointSlice：

```bash
deploy/kubernetes/mysql/bootstrap-writer-endpoint.sh \
  database mysql-a-0 mysql-writer-clusterguard
```

## 6. 控制面配置

控制节点配置启用：

```json
"kubernetes": {
  "enabled": true,
  "request_timeout_seconds": 10,
  "fence_timeout_seconds": 60
}
```

还需同时启用 MySQL 的操作与复制凭据，否则切换无法执行——平台只有在这些凭据解析成功时才对外宣告 MySQL 变更能力：

```json
"mysql": {
  "enabled": true,
  "operation": { "username": "cg_operator", "password_env": "CG_MYSQL_OPERATION_PASSWORD" },
  "replication": { "username": "cg_replication", "password_env": "CG_MYSQL_REPLICATION_PASSWORD" }
}
```

在每个控制节点准备权限为 `0600` 的凭据描述文件、CA 和 token。`credential_ref` 只保存凭据描述文件的绝对路径，不把 token 写入 ClusterGuard 元数据。

```json
{
  "ca_file": "/etc/clusterguard/kubernetes/ca.crt",
  "bearer_token_file": "/etc/clusterguard/kubernetes/token",
  "server_name": "kubernetes.default.svc"
}
```

三个 Raft 控制节点必须能访问相同 Kubernetes API，并各自具有等价的本地凭据文件。

## 7. 资源登记

先通过已认证的控制 API 创建 Kubernetes RuntimeTarget：

```json
{
  "display_name": "production-k8s",
  "kind": "kubernetes",
  "endpoint": "https://kubernetes.example.internal:6443",
  "credential_ref": "/etc/clusterguard/kubernetes/credentials.json",
  "active": true
}
```

每个实例创建一个 WorkloadBinding：

```json
{
  "instance_id": "INSTANCE_UUID",
  "runtime_target_id": "RUNTIME_UUID",
  "runtime_kind": "kubernetes",
  "active": true,
  "kubernetes": {
    "cluster_name": "production",
    "namespace": "database",
    "stateful_set": "mysql-a",
    "ordinal": 0,
    "pod_name": "mysql-a-0",
    "pod_uid": "LIVE_POD_UID",
    "node_name": "worker-a",
    "pvc_uid": "MYSQL_DATA_PVC_UID"
  }
}
```

数据库实例自身的探测地址应使用每个 Pod 的 headless Service DNS，不能把三个实例都登记为 writer Service。最后创建 HAEndpoint：

```json
{
  "kind": "service",
  "provider": "kubernetes_service",
  "provider_ref": "database/mysql-writer/mysql-writer-clusterguard",
  "hostname": "mysql-writer.database.svc",
  "port": 3306,
  "owner_id": "CURRENT_PRIMARY_INSTANCE_UUID",
  "active": true
}
```

## 8. 验收边界

上线前至少验证计划切换循环、主 Pod 删除、kube-apiserver 暂时不可达、EndpointSlice 冲突、旧主 Node NotReady、Pod UID 变化、StatefulSet 被误扩为两副本、Raft 少数派和守卫镜像拉取失败。

当前代码与单元测试已覆盖 API TLS、token 轮换、selector 阻断、Pod UID 绑定、单后端迁移、角色注解、持久 fence、缩容和 NotReady 阻断。尚未在真实 Kubernetes 集群执行端到端验收，不能把代码测试等同于生产验收。

故障切换后的旧主会保持 `fenced=true` 且副本数为 0。重新加入前必须通过受控维护流程确认其持久角色为 `replica`、清除 fence、扩容到 1，再进行 GTID 旧主恢复；该图形化维护动作仍需后续接入，当前不得手工跳过数据校验直接恢复为可写。
