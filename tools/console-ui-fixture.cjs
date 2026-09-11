const fs = require('node:fs');
const http = require('node:http');
const path = require('node:path');

function operationFixturePage(operations, url, clusters = []) {
  const params = url.searchParams;
  const label = id => clusters.find(cluster => cluster.resource_id === id)?.display_name || id || '';
  const order = (a, b) => Date.parse(b.created_at) - Date.parse(a.created_at) || b.resource_id.localeCompare(a.resource_id);
  const grouped = new Map();
  const records = [];
  for (const item of [...operations].sort(order)) {
    if (params.get('cluster_id') && params.get('cluster_id') !== item.operation.cluster_id) continue;
    const incident = item.operation.requested_by === 'clusterguard-automatic-recovery' && /^automatic-failover:.+:[1-9][0-9]*$/.test(item.idempotency_key || '') ? `${item.operation.cluster_id}:${item.idempotency_key.replace(/:[0-9]+$/, '')}` : '';
    if (incident && grouped.has(incident)) { grouped.get(incident).incident_attempt_count++; continue; }
    const summary = { resource_id:item.resource_id, created_at:item.created_at, operation:item.operation, target_id:item.target_id, status:item.status, stage:item.stage, idempotency_key:item.idempotency_key, plan:{ source_id:item.plan.source_id }, message:item.message, review:item.review, summary:true, incident_attempt_count:1, cluster_label:label(item.operation.cluster_id) };
    if (incident) grouped.set(incident, summary);
    records.push(summary);
  }
  const filtered = records.filter(item => (!params.get('kind') || item.operation.kind === params.get('kind')) && (!params.get('status') || item.status === params.get('status')) && (!params.get('q') || [item.cluster_label, item.operation.cluster_id, item.plan.source_id, item.target_id, item.operation.kind, item.status].join(' ').toLowerCase().includes(params.get('q').trim().toLowerCase())));
  const boundary = params.get('cursor') ? JSON.parse(atob(params.get('cursor').replace(/-/g, '+').replace(/_/g, '/'))) : null;
  const eligible = boundary ? filtered.filter(item => order(boundary, item) < 0) : filtered;
  const limit = Number(params.get('limit') || 20);
  const items = eligible.slice(0, limit);
  const remaining = eligible.length - items.length;
  const last = items.at(-1);
  return { view:'page', items, limit, total:filtered.length, record_count:filtered.reduce((n,item) => n + item.incident_attempt_count,0), remaining, next_cursor:remaining ? btoa(JSON.stringify({created_at:last.created_at, resource_id:last.resource_id})).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '') : '' };
}

// Actual console HTML with isolated example data. No request is forwarded to a database.
function createConsoleFixture(htmlPath = path.resolve(__dirname, '../internal/api/console.html')) {
  const now = new Date().toISOString();
  const clusters = ['mysql', 'postgresql'].map((engine, index) => ({
    resource_id:`11111111-1111-4111-8111-11111111111${index}`,
    display_name:index ? 'sample-pg16' : 'sample-mysql80', engine, health:{ state:'healthy' }
  }));
  const nodes = [1, 2, 3].map(i => ({ resource_id:`node-${i}`, node_name:`sample-host-0${i}`, kind:'mixed', active:true, ip_address:`192.0.2.${i}` }));
  const instances = cluster => nodes.map((node, i) => ({
    resource_id:`${cluster.resource_id}-instance-${i}`, cluster_id:cluster.resource_id, node_id:node.resource_id,
    hostname:`${cluster.engine === 'mysql' ? 'mysql' : 'pg'}-0${i + 1}`, ip_address:node.ip_address,
    port:cluster.engine === 'mysql' ? 3306 : 5432, engine:cluster.engine,
    role:i === 1 ? 'primary' : cluster.engine === 'mysql' ? 'replica' : 'standby',
    health:{ state:'healthy', replication:'running' },
    engine_metadata:{ version:cluster.engine === 'mysql' ? '8.0.44' : '16.4' },
    engine_identity:{ resource_id:`sample-native-${i}`, system_identifier:'7427000123456789000', server_uuid:`sample-server-${i}` },
    replication:{ source_instance_id:`${cluster.resource_id}-instance-1`, lag_seconds:0, io_thread:'running', sql_thread:'running' }
  }));
  const detail = cluster => ({ cluster, instances:instances(cluster), endpoints:[],
    runtime_profile:{ kinds:['docker'], complete:true, endpoint_providers:['linux_vip'], unbound_instance_ids:[] },
    workload_bindings:instances(cluster).map((instance, i) => ({ active:true, instance_id:instance.resource_id, runtime_kind:'docker', workload_name:`sample-${cluster.engine}-${i + 1}`, host_node_id:nodes[i].resource_id }))
  });
  const topology = cluster => ({ cluster_id:cluster.resource_id, observed_at:now, instances:instances(cluster), anomalies:[],
    probes:instances(cluster).map(instance => ({ instance_id:instance.resource_id, outcome:'reachable', discovery_observed_at:now })),
    links:[0, 2].map(i => ({ source_instance_id:`${cluster.resource_id}-instance-1`, target_instance_id:`${cluster.resource_id}-instance-${i}` }))
  });
  const operations = clusters.flatMap(cluster => [0, 1, 2].map(i => ({
    resource_id:`${cluster.resource_id}-operation-${i}`, cluster_id:cluster.resource_id,
    created_at:new Date(Date.now() - i * 3600000).toISOString(), status:'succeeded', stage:'report',
    operation:{ kind:i === 1 ? 'former_primary_rejoin' : 'switchover', cluster_id:cluster.resource_id, target_id:`${cluster.resource_id}-instance-1`, requested_by:'sample-admin' },
    plan:{ source_id:`${cluster.resource_id}-instance-0` }, verification:{ passed:true }, message:'示例记录：角色、复制与业务入口已验证'
  })));
  const packageItem = { package:{ patch_id:'sample-upgrade-87-to-88', source_version:'2.2-87', target_version:'2.2-88', uploaded_at:now, size_bytes:36704864, signature_verified:true, rolling_supported:true, rollback_supported:true },
    job:{ status:'succeeded', mode:'execute', updated_at:now, finished_at:now, message:'示例记录：全部节点升级完成', maintenance_active:false,
      progress:{ phase:'completed', total:3, current:3, percent:100 },
      events:Array.from({ length:30 }, (_, i) => ({ updated_at:new Date(Date.now() - (30 - i) * 10000).toISOString(), node:`192.0.2.${i % 3 + 1}`, status:'succeeded', message:`示例事件 ${i + 1}：节点版本与服务状态验证通过` }))
    }
  };
  const control = { hook:null, requests:[], mutations:0 };
  const server = http.createServer(async (req, res) => {
    try {
      const url = new URL(req.url, 'http://localhost');
      if (!url.pathname.startsWith('/api/')) {
        if (process.env.CONSOLE_UI_REVIEW === '1' && url.searchParams.get('ui') !== 'review') {
          url.searchParams.set('ui', 'review');
          res.writeHead(302, { Location:url.pathname + url.search }); res.end(); return;
        }
        res.setHeader('Content-Type', 'text/html; charset=utf-8');
        res.setHeader('Cache-Control', 'no-store');
        res.end(fs.readFileSync(htmlPath)); return;
      }
      control.requests.push({ method:req.method, path:url.pathname, query:url.search, at:Date.now() });
      let status = 200, result = [], message = '';
      const cluster = clusters.find(item => url.pathname.startsWith(`/api/v1/clusters/${item.resource_id}`));
      const intercepted = control.hook && await control.hook({ req, url, cluster });
      if (intercepted) ({ status = 200, result = null, message = '' } = intercepted);
      else if (req.method !== 'GET') { control.mutations++; status = 403; message = '本地示例预览不执行数据库或升级操作。'; }
      else if (url.pathname === '/api/v1/auth/me') result = { user:{ username:'sample-admin', display_name:'Preview Admin', role:'admin' } };
      else if (url.pathname === '/api/v1/clusters') result = clusters;
      else if (cluster && url.pathname === `/api/v1/clusters/${cluster.resource_id}`) result = detail(cluster);
      else if (cluster && url.pathname.endsWith('/topology')) result = topology(cluster);
      else if (cluster && url.pathname.endsWith('/health')) result = { state:'healthy' };
      else if (cluster && url.pathname.endsWith('/candidates')) result = [0, 2].map((i, rank) => ({ instance_id:`${cluster.resource_id}-instance-${i}`, eligible:true, rank:rank + 1 }));
      else if (cluster && url.pathname.endsWith('/metrics')) result = { observed_at:now, instances:instances(cluster).map(instance => ({ instance_id:instance.resource_id, values:{ qps:246, tps:83, connections:42, running_threads:6, slow_queries_per_second:0, buffer_pool_hit_ratio:.993, active_connections:21, transactions_total:29407, deadlocks_total:0, database_size_bytes:8654321920, buffer_cache_hit_ratio:.995, max_transaction_age_seconds:4 } })) };
      else if (cluster && url.pathname.endsWith('/power/status')) result = { outage_classification:{ kind:'normal', database_state:'running', control_plane:'online_independent', automatic_failover_suppressed:false }, operation_history:[] };
      else if (cluster && url.pathname.endsWith('/recovery/status')) result = { available:true, tasks:[] };
      else if (url.pathname === '/api/v1/operations') {
        result = operations.filter(item => !url.searchParams.get('cluster_id') || item.operation.cluster_id === url.searchParams.get('cluster_id'));
        if (url.searchParams.get('view') === 'context') {
          const clusterId = url.searchParams.get('cluster_id') || '';
          result = { view:'context', cluster_id:clusterId, operation_count:result.length,
            running_count:result.filter(item => item.status === 'running').length,
            unreviewed_count:result.filter(item => item.status === 'indeterminate' && !item.review).length,
            historical_source_ids:clusterId ? [...new Set(result.filter(item => ['switchover', 'failover'].includes(item.operation.kind)).map(item => item.plan?.source_id).filter(Boolean))].sort() : [],
            recent:clusterId ? [] : [...result].sort((a, b) => Date.parse(b.created_at) - Date.parse(a.created_at) || b.resource_id.localeCompare(a.resource_id)).slice(0, 5).map(item => ({ resource_id:item.resource_id, created_at:item.created_at, operation:item.operation, status:item.status, stage:item.stage, review:item.review, summary:true }))
          };
        }
        if (url.searchParams.get('view') === 'summary') result = result.map(item => ({ resource_id:item.resource_id, created_at:item.created_at, operation:item.operation, target_id:item.target_id, status:item.status, stage:item.stage, idempotency_key:item.idempotency_key, plan:{ source_id:item.plan.source_id }, message:item.message, review:item.review, summary:true }));
        if (url.searchParams.get('view') === 'page') result = operationFixturePage(operations, url, clusters);
      }
      else if (url.pathname.startsWith('/api/v1/operations/')) {
        result = operations.find(item => item.resource_id === url.pathname.split('/').pop());
        if (!result) { status = 404; message = 'operation not found'; }
      }
      else if (url.pathname === '/api/v1/nodes') result = nodes;
      else if (url.pathname === '/api/v1/capabilities') result = clusters.map(item => ({ engine:item.engine, features:{ execute:{ available:true }, discover:{ available:true }, topology:{ available:true } } }));
      else if (url.pathname.endsWith('/capabilities')) result = { available:false, reason:'本地示例预览' };
      else if (url.pathname === '/api/v1/platform/updates') result = { available:true, packages:[packageItem], current_version:'2.2-88', architecture:'x86_64' };
      else if (url.pathname === '/api/v1/platform/version') result = { version:'2.2', release:'88', architecture:'x86_64' };
      else if (url.pathname === '/api/v1/control-plane/status') result = { mode:'raft', role:'leader', ready:true, quorum_confirmed:true, voter_count:3, local_controller_id:'node-1', leader_id:'node-1', update_maintenance_active:false, uptime_seconds:72600 };
      res.writeHead(status, { 'Content-Type':'application/json', 'Cache-Control':'no-store' });
      res.end(JSON.stringify({ status:status < 400 ? 'ok' : 'error', result, message }));
    } catch (error) { res.writeHead(500); res.end(JSON.stringify({ status:'error', message:error.message })); }
  });
  return { server, control, clusters, detail, topology, packageItem, operations };
}
module.exports = { createConsoleFixture, operationFixturePage };
if (require.main === module) {
  const { server } = createConsoleFixture();
  server.listen(Number(process.env.PORT || 18789), '127.0.0.1', () => console.log(`Read-only example console: http://127.0.0.1:${server.address().port}/#topology`));
}
