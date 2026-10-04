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
GET  /gates/{id}/review-queue
POST /commands
GET  /commands/{id}
POST /commands/{id}/enqueue
POST /commands/{id}/priority
POST /commands/{id}/interlock
POST /commands/{id}/execute
```

待复核队列在命令创建时自动形成：同一闸门的多条 pending 命令按提交顺序排队，只有当前队首可以进入复核；队首复核完成（通过或拒绝）后出队，下一项按当时仍真实 pending 的条目成为队首。

读取队列返回顺序、当前队首以及每条命令的优先级与依据历史：

```sh
curl http://127.0.0.1:18119/gates/gate-east/review-queue
```

调整优先级时必须给出数值（高数值优先，范围 -1000 到 1000）、依据和操作人；调整只追加事实，不覆盖既有历史：

```json
{
  "priority": 10,
  "basis": "上游来水急涨，优先预泄",
  "changed_by": "dispatcher-lin"
}
```

对已经在队中的命令重复排队、对已离开 pending 的命令调整优先级或排队，都会返回稳定冲突错误且不改变任何既有事实。

登记闸门示例：

```json
{
  "id": "gate-east",
  "name": "东侧泄洪闸",
  "max_aperture": 4.5,
  "initial_aperture": 0.5
}
```

提交命令示例：

```json
{
  "gate_id": "gate-east",
  "target_aperture": 2.25,
  "reason": "上游水位调整",
  "requested_by": "dispatcher-lin"
}
```

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
- `cmd/workflowcheck`：四条工作流的真实 HTTP 检查入口。
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
go run ./cmd/workflowcheck --workflow execute-gate-command
go run ./cmd/workflowcheck --workflow review-queue
```

这些检查会创建临时 HTTP 服务并调用真实路由。它们不调用外部网络，也不依赖数据库。

## 测试状态

队列能力附带 `internal/domain` 与 `internal/storage` 的单元测试，覆盖 FIFO/优先级排序、依据历史只追加、重复排队、队首门禁、队首完成后按真实条件前进，以及并发复核只有单一赢家且非队首事实不变。运行：

```sh
go test ./...
```
