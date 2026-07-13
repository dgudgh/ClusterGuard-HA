package report

import (
	"fmt"
	"html/template"
	"io"
	"strings"

	"clusterguard.io/ha/internal/lifecycle"
	"clusterguard.io/ha/pkg/model"
)

type Data struct {
	Report    model.Report
	Operation model.OperationRecord
	Lifecycle *lifecycle.Task
	Audits    []model.AuditEvent
}

type renderData struct {
	Report      model.Report
	ClusterID   model.ResourceID
	SourceID    model.ResourceID
	TargetIDs   string
	RequestedBy string
	Checks      []model.Check
	Audits      []model.AuditEvent
}

var htmlReportTemplate = template.Must(template.New("report").Parse(`<!doctype html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width,initial-scale=1">
  <title>{{.Report.Title}} - ClusterGuard HA</title>
  <style>
    :root{color-scheme:light;--ink:#17221d;--muted:#65716b;--line:#d8e1dc;--panel:#fff;--canvas:#f4f7f5;--accent:#087a68}
    *{box-sizing:border-box}body{margin:0;background:var(--canvas);color:var(--ink);font:14px/1.5 system-ui,-apple-system,"Segoe UI",sans-serif}
    main{width:min(1080px,calc(100% - 32px));margin:28px auto 48px}.header,.section{border:1px solid var(--line);border-radius:8px;background:var(--panel)}
    .header{padding:24px}.section{margin-top:16px;padding:20px}h1{margin:0;font-size:24px;letter-spacing:0}h2{margin:0 0 14px;font-size:17px}
    .subtitle{margin:6px 0 0;color:var(--muted)}.grid{display:grid;grid-template-columns:repeat(4,minmax(0,1fr));gap:10px}.field{padding:12px;border:1px solid var(--line);border-radius:6px}
    .label{display:block;color:var(--muted);font-size:12px}.value{display:block;margin-top:4px;overflow-wrap:anywhere;font-weight:700}.status{color:var(--accent)}
    table{width:100%;border-collapse:collapse}th,td{padding:10px;border-bottom:1px solid var(--line);text-align:left;vertical-align:top}th{color:var(--muted);font-size:12px}
    details{margin-top:8px;border:1px solid var(--line);border-radius:6px}summary{padding:11px 12px;cursor:pointer;font-weight:700}.evidence{padding:0 12px 12px;white-space:pre-wrap;overflow-wrap:anywhere;color:var(--muted)}
    @media(max-width:760px){.grid{grid-template-columns:1fr 1fr}}@media(max-width:480px){.grid{grid-template-columns:1fr}}
  </style>
</head>
<body><main>
  <section class="header">
    <h1>{{.Report.Title}}</h1>
    <p class="subtitle">ClusterGuard HA 多数据库高可用控制平台</p>
  </section>
  <section class="section">
    <h2>操作摘要</h2>
    <div class="grid">
      <div class="field"><span class="label">报告 ID</span><span class="value">{{.Report.ResourceID}}</span></div>
      <div class="field"><span class="label">操作 ID</span><span class="value">{{.Report.OperationID}}</span></div>
      <div class="field"><span class="label">集群 ID</span><span class="value">{{.ClusterID}}</span></div>
      <div class="field"><span class="label">状态</span><span class="value status">{{.Report.Status}}</span></div>
      <div class="field"><span class="label">源节点</span><span class="value">{{.SourceID}}</span></div>
      <div class="field"><span class="label">目标节点</span><span class="value">{{.TargetIDs}}</span></div>
      <div class="field"><span class="label">操作人</span><span class="value">{{.RequestedBy}}</span></div>
      <div class="field"><span class="label">生成时间</span><span class="value">{{.Report.CreatedAt}}</span></div>
    </div>
    <p>{{.Report.Summary}}</p>
  </section>
  <section class="section">
    <h2>验证结果</h2>
    <table><thead><tr><th>检查项</th><th>状态</th><th>说明</th></tr></thead><tbody>
      {{range .Checks}}<tr><td>{{.Name}}</td><td>{{.Status}}</td><td>{{.Message}}</td></tr>{{else}}<tr><td colspan="3">暂无验证记录</td></tr>{{end}}
    </tbody></table>
  </section>
  <section class="section">
    <h2>审计时间线</h2>
    {{range .Audits}}<details><summary>原始证据</summary><div class="evidence">{{.CreatedAt}} | {{.Stage}} | {{.Actor}}
{{.Message}}</div></details>{{else}}<p class="subtitle">暂无审计证据</p>{{end}}
  </section>
</main></body></html>`))

func RenderHTML(writer io.Writer, data Data) error {
	if writer == nil {
		return fmt.Errorf("HTML report writer is required")
	}
	if !model.ValidResourceID(data.Report.ResourceID) || !model.ValidResourceID(data.Report.OperationID) {
		return fmt.Errorf("HTML report identity is invalid")
	}
	view := renderData{Report: data.Report, Audits: data.Audits}
	if model.ValidResourceID(data.Operation.ResourceID) && data.Operation.ResourceID == data.Report.OperationID {
		view.ClusterID = data.Operation.Operation.ClusterID
		view.SourceID = data.Operation.Plan.SourceID
		view.TargetIDs = string(data.Operation.Plan.TargetID)
		view.RequestedBy = data.Operation.Operation.RequestedBy
		view.Checks = append([]model.Check{}, data.Operation.Verification.Checks...)
	} else if data.Lifecycle != nil && data.Lifecycle.OperationID == data.Report.OperationID {
		view.ClusterID = data.Lifecycle.ClusterID
		view.SourceID = data.Lifecycle.Request.Donor.InstanceID
		targetIDs := make([]string, 0, len(data.Lifecycle.Plan.Targets))
		for _, target := range data.Lifecycle.Plan.Targets {
			if target.NodeID != "" {
				targetIDs = append(targetIDs, string(target.NodeID))
			}
		}
		view.TargetIDs = strings.Join(targetIDs, ", ")
		view.RequestedBy = data.Lifecycle.Request.RequestedBy
		view.Checks = append([]model.Check{}, data.Lifecycle.Checks...)
	} else {
		return fmt.Errorf("HTML report operation context is unavailable")
	}
	return htmlReportTemplate.Execute(writer, view)
}
