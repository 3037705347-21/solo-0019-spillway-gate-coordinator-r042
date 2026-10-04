# 泄洪闸门操作协调服务

这是一个无外部依赖的 Go 后端，用本地内存仓库协调闸门登记、开度调整命令、安全联锁复核和执行结果。服务强调单闸门活动命令互斥、原子状态转换、执行令牌生命周期和稳定错误传播。

## 运行

```sh
go build ./...
go run ./cmd/server --addr 127.0.0.1:18119
```

健康入口为 `GET http://127.0.0.1:18119/health`。

## API

```text
POST /gates
GET  /gates/{id}
GET  /gates/{id}/queue
POST /commands
GET  /commands/{id}
POST /commands/{id}/priority
POST /commands/{id}/interlock
POST /commands/{id}/execute
```

登记闸门示例：

```json
{
  "id": "gate-east",
  "name": "东侧泄洪闸",
  "max_aperture": 4.5,
  "initial_aperture": 0.5
}
```

提交命令示例（`priority` 与 `priority_basis` 可省略，缺省优先级为 0）：

```json
{
  "gate_id": "gate-east",
  "target_aperture": 2.25,
  "reason": "上游水位调整",
  "requested_by": "dispatcher-lin",
  "priority": 0,
  "priority_basis": "例行调度"
}
```

命令创建即进入该闸门的待复核队列。读取队列示例（`GET /gates/gate-east/queue`）：

```json
{
  "queue": {
    "gate_id": "gate-east",
    "head_command_id": "cmd-xxx",
    "entries": [
      {
        "command_id": "cmd-xxx",
        "priority": 5,
        "priority_basis": "上游洪峰临近",
        "queued_at": "2026-10-04T00:00:00Z",
        "position": 1,
        "is_head": true
      }
    ],
    "updated_at": "2026-10-04T00:00:00Z",
    "version": 2
  }
}
```

调整优先级示例（仅 pending 命令可调整，必须说明依据；队首按当前优先级与到达次序动态确定）：

```json
{
  "priority": 5,
  "basis": "上游洪峰临近"
}
```

只有队首命令可以提交联锁复核；复核完成后命令出队，下一项按当时真实条件成为队首。

联锁复核示例：

```json
{
  "verdict": "clear",
  "verifier": "safety-wu",
  "note": "上下游条件满足"
}
```

执行示例：

```json
{
  "operator": "operator-chen",
  "token": "复核响应中的 execution_token",
  "simulate_fault": false
}
```

## 目录

- `cmd/server`：服务进程。
- `cmd/workflowcheck`：五条工作流的真实 HTTP 检查入口。
- `internal/bootstrap`：依赖装配。
- `internal/contracts`：公开数据契约。
- `internal/coordination`：业务工作流。
- `internal/domain`：实体、不变量和状态机。
- `internal/httpapi`：路由、解码和错误映射。
- `internal/storage`：互斥保护的内存仓库。

## 工作流检查

```sh
go run ./cmd/workflowcheck --workflow register-gate
go run ./cmd/workflowcheck --workflow request-gate-command
go run ./cmd/workflowcheck --workflow review-safety-interlock
go run ./cmd/workflowcheck --workflow review-queue-priority
go run ./cmd/workflowcheck --workflow execute-gate-command
```

这些检查会创建临时 HTTP 服务并调用真实路由。它们不调用外部网络，也不依赖数据库。

## 测试状态

测试有意延后。当前交付仅包含初始化基线和生产级 smoke 检查；后续任务阶段会添加自动化测试与红绿验证，不应把当前没有测试文件视为项目未完成。
