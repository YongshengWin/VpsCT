package api

// Workflow steps refer to the registered product operations. They introduce no
// alternate authorization or execution path; availability is identity-specific.
type agentWorkflowStep struct {
	Purpose     string `json:"purpose"`
	Method      string `json:"method"`
	Path        string `json:"path"`
	OperationID string `json:"operation_id"`
	Available   bool   `json:"available"`
}
type agentWorkflow struct {
	ID         string              `json:"id"`
	Title      string              `json:"title"`
	Steps      []agentWorkflowStep `json:"steps"`
	Completion string              `json:"completion"`
	Recovery   string              `json:"recovery"`
}

func agentWorkflows(operations []agentOperation) []agentWorkflow {
	step := func(purpose, method, path string) agentWorkflowStep {
		return agentWorkflowStep{Purpose: purpose, Method: method, Path: path}
	}
	items := []agentWorkflow{
		{ID: "server_enrollment", Title: "接入 VPS", Steps: []agentWorkflowStep{
			step("复用已有服务器，避免重复创建", "GET", "/api/v1/servers"),
			step("仅缺少记录时创建", "POST", "/api/v1/servers"),
			step("生成临时安装命令；执行需要目标主机授权", "POST", "/api/v1/servers/{id}/enroll-token"),
			step("安装后核对 agent_status 和实际能力", "GET", "/api/v1/servers/{id}"),
		}, Completion: "目标 agent_status=online，能力满足请求；生成安装命令不算安装完成。", Recovery: "先查询是否已接入；安装令牌不得写入日志或回显。"},
		{ID: "deploy_node", Title: "部署代理节点", Steps: []agentWorkflowStep{
			step("确认协议支持和运行条件", "GET", "/api/v1/servers/{id}/network/capabilities"),
			step("创建节点", "POST", "/api/v1/servers/{id}/nodes"),
			step("核对创建的节点及归属", "GET", "/api/v1/nodes/{id}"),
			step("核对服务器期望配置与实际回执", "GET", "/api/v1/servers/{id}"),
			step("必要时检查实际发布的 revision", "GET", "/api/v1/servers/{id}/desired"),
		}, Completion: "目标节点存在，服务器 desired.in_sync=true、desired.status=applied 且无 apply_error；仅有 201 或服务器在线不足以证明节点成功。", Recovery: "先查询资源和最新回执；超时后不重复创建节点。"},
		{ID: "import_nodes", Title: "导入外部节点", Steps: []agentWorkflowStep{
			step("先解析并核对内容", "POST", "/api/v1/nodes/parse"), step("保存用户选择的节点", "POST", "/api/v1/nodes/import"), step("筛选 source、协议、服务器和名称", "GET", "/api/v1/nodes"),
		}, Completion: "节点记录存在且协议、来源、端点与请求一致；导入不代表已测试可达。", Recovery: "先比对导入结果，避免重复导入；不要回显 URI。"},
		{ID: "access_route", Title: "两台或三台转发线路", Steps: []agentWorkflowStep{
			step("选择受管、导入或手动落地节点", "GET", "/api/v1/nodes"),
			step("operation_id 固定；两台用 ix_server_id=0，三台指定中转；获取 candidate/token/checks", "POST", "/api/v1/access-routes/preview"),
			step("提交完整 candidate 并附 expected_impact=token", "POST", "/api/v1/access-routes"),
			step("使用返回的 id 查询 stage/message/revision", "GET", "/api/v1/access-routes/{op}"),
			step("applied 后取得入口节点及 URI", "GET", "/api/v1/access-routes/{op}/node"),
		}, Completion: "stage=applied 且 node 接口成功；pending/blocked 不表示完成。", Recovery: "先 GET 同一 id；相同 candidate 和 operation_id 可核对后重放，不能换 id 盲目重试；409/428 重新预览。"},
		{ID: "stop_access_route", Title: "停止并清理线路", Steps: []agentWorkflowStep{
			step("读取最新 revision", "GET", "/api/v1/access-routes/{op}"), step("提交 expected_revision", "DELETE", "/api/v1/access-routes/{op}"), step("等待逐段清理结束", "GET", "/api/v1/access-routes/{op}"),
		}, Completion: "stage=retired；stopping_entry/stopping_ix 仍在清理。", Recovery: "读取 message 和最新 revision；不得删除线路管理的底层转发来绕过清理。"},
		{ID: "managed_exit", Title: "已有节点通过受管 VPS 出网", Steps: []agentWorkflowStep{
			step("核对源节点与目标能力", "GET", "/api/v1/servers/{id}/network/capabilities"), step("预览实际影响", "POST", "/api/v1/transits/preview"), step("按预览提交", "POST", "/api/v1/transits"), step("查询目标服务器中转状态", "GET", "/api/v1/servers/{id}/transits"),
		}, Completion: "中转达到 applied，源节点及两端配置已应用。", Recovery: "依据实际 operation_id、revision 和状态重试，不重复创建。"},
		{ID: "existing_exit", Title: "已有代理或指定网卡出口", Steps: []agentWorkflowStep{
			step("读取可用出口与网卡", "GET", "/api/v1/servers/{id}/network"), step("预览出口创建", "POST", "/api/v1/servers/{id}/egress-profiles/preview"), step("创建出口版本", "POST", "/api/v1/servers/{id}/egress-profiles"), step("预览节点绑定", "POST", "/api/v1/nodes/{id}/network/preview"), step("提交节点网络绑定", "PUT", "/api/v1/nodes/{id}/network"), step("查询实际操作结果", "GET", "/api/v1/network/operations/{op}"),
		}, Completion: "实际网络操作达到成功状态，节点当前配置已应用。", Recovery: "遵循 preview 影响凭证及版本校验；节点入口协议与出口协议分别判断。"},
		{ID: "subscription", Title: "生成或转换订阅", Steps: []agentWorkflowStep{
			step("验证模板、节点选择和分流", "POST", "/api/v1/subscriptions/preview"), step("创建订阅", "POST", "/api/v1/subscriptions"), step("核对资源配置", "GET", "/api/v1/subscriptions/{id}"), step("检查客户端格式渲染", "GET", "/api/v1/subscriptions/{id}/render"),
		}, Completion: "保存和指定格式渲染均成功；渲染结果可能包含凭据。", Recovery: "无通用幂等保证；超时比对资源 ID、字段和创建时间，不按名称盲目重建。"},
		{ID: "share", Title: "创建资源分享", Steps: []agentWorkflowStep{
			step("核对目标与权限", "GET", "/api/v1/servers"), step("创建专用资源及配额", "POST", "/api/v1/shares"), step("核对分享状态、节点和用量", "GET", "/api/v1/shares/{id}"),
		}, Completion: "分享配置正确；专用节点实际已应用。", Recovery: "暂停、撤销、重签与删除的影响不同，按用户实际要求选择。"},
		{ID: "observe", Title: "监控和排查", Steps: []agentWorkflowStep{
			step("查看服务器回执与诊断", "GET", "/api/v1/servers/{id}"), step("查看本期或时间段用量", "GET", "/api/v1/servers/{id}/traffic"), step("查看操作状态", "GET", "/api/v1/servers/{id}/network/operations"), step("按需有界监听事件", "GET", "/api/v1/events"),
		}, Completion: "报告实际状态与证据；离线、失败、待应用分别说明。", Recovery: "限制查询范围与事件数量；429 遵循 Retry-After，不自动重试写操作。"},
		{ID: "fixed_port", Title: "独立端口转发", Steps: []agentWorkflowStep{
			step("确认协议、授权和网络运行能力", "GET", "/api/v1/servers/{id}/network/capabilities"), step("预览独立端口转发", "POST", "/api/v1/servers/{id}/forwards/preview"), step("提交固定 operation_id 和实际影响凭证", "POST", "/api/v1/servers/{id}/forwards"), step("核对实际应用结果", "GET", "/api/v1/network/operations/{op}"),
		}, Completion: "返回任务 status=applied 且对应版本已确认；UDP 还需实际业务验证。", Recovery: "先查原 operation_id；409/428 重新预览，不重复添加线路已有的转发。"},
		{ID: "nat_mapping", Title: "NAT 端口映射", Steps: []agentWorkflowStep{
			step("读取当前 revision 与商家映射", "GET", "/api/v1/servers/{id}/nat"), step("提交 expected_revision 与明确配置", "PUT", "/api/v1/servers/{id}/nat"), step("核对当前映射及 agent 确认", "GET", "/api/v1/servers/{id}/nat"),
		}, Completion: "映射已被 agent 确认；面板不会代替商家建立公网转发。", Recovery: "版本冲突重新读取；本机 root 文件优先时遵守只读约束。"},
		{ID: "network_billing", Title: "网卡流量计费来源", Steps: []agentWorkflowStep{
			step("读取现有计费来源与 revision", "GET", "/api/v1/servers/{id}/network/billing"), step("按用户要求修改来源", "PUT", "/api/v1/servers/{id}/network/billing"), step("核对配置与实际应用状态", "GET", "/api/v1/servers/{id}/network/billing"),
		}, Completion: "来源配置已被 agent 确认；计费来源不等于业务出口。", Recovery: "按整个账期方向聚合后计费；不要用节点计量要求网卡计量数值相同。"},
		{ID: "agent_maintenance", Title: "受管 VPS agent 升级或卸载", Steps: []agentWorkflowStep{
			step("查询目标主机维护能力、版本与任务", "GET", "/api/v1/servers/{id}/maintenance"), step("按明确授权提交带二次认证及目标确认的任务", "POST", "/api/v1/servers/{id}/maintenance"), step("查询同一任务并核对结果", "GET", "/api/v1/servers/{id}/maintenance"),
		}, Completion: "对应任务 succeeded；升级后 agent 在线且版本符合控制端分发目标。", Recovery: "202 或主机失联不能当成功；先查原任务，保留固定任务编号和备份。"},
		{ID: "maintenance", Title: "原生安装升级或卸载", Steps: []agentWorkflowStep{
			step("确认控制端维护能力及阻塞原因", "GET", "/api/v1/system/maintenance"), step("核对实际发行目标", "GET", "/api/v1/system/maintenance/latest"), step("仅授权时提交目标任务，需密码与已启用的两步验证", "POST", "/api/v1/system/maintenance"), step("按返回任务编号核对结果", "GET", "/api/v1/system/maintenance"),
		}, Completion: "对应任务 succeeded；Docker 部署使用宿主机 Compose 流程，不调用原生维护执行器。", Recovery: "同一任务编号查询结果；升级失败核对备份和回滚状态，不能将 202 当成功。"},
	}
	for i := range items {
		for j := range items[i].Steps {
			s := &items[i].Steps[j]
			for _, op := range operations {
				if op.Method == s.Method && op.Path == s.Path {
					s.OperationID, s.Available = op.OperationID, op.Available
					break
				}
			}
		}
	}
	return items
}
