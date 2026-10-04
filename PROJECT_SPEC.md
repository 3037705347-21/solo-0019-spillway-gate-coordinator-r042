# 泄洪闸门操作协调服务

## 项目目标

该服务用于水工控制室协调泄洪闸门的命令申请、安全联锁复核和单次开度调整。系统在同一闸门上只允许一条已通过复核且尚未结束的执行命令，任何冲突都必须被显式拒绝，不能覆盖已经占用的闸门。

## 使用角色

- 运行协调员：登记闸门并提交开度调整命令。
- 安全复核员：提交联锁结论，决定命令是否通过。
- 闸门操作员：凭执行令牌启动命令并登记执行结果。

## 核心实体

- `gate`：闸门及其当前开度、运行状态、最大开度和活动命令。
- `gate_command`：一次开度调整申请，包含目标开度、申请原因、状态、业务优先级、优先级依据和执行令牌。
- `interlock_decision`：安全复核结论，状态为 clear 或 blocked。
- `review_queue`：同闸门待复核队列，按优先级与到达次序排序，队首按当前真实条件动态确定。

## 业务流程

### register-gate

运行协调员通过 `POST /gates` 登记闸门。服务校验标识、名称、最大开度和初始开度，写入空闲状态；重复标识返回稳定冲突错误，不覆盖原记录。登记的同时为该闸门建立空的待复核队列。

### request-gate-command

运行协调员通过 `POST /commands` 提交开度调整命令。服务读取闸门当前开度和限制，校验目标开度与申请信息，创建待复核命令并冻结起始开度。命令创建即进入该闸门的待复核队列，可携带初始优先级与优先级依据，缺省优先级为 0。

### review-queue-priority

调度员通过 `GET /gates/{id}/queue` 读取待复核队列：有序条目、每条优先级与依据、当前队首一目了然。通过 `POST /commands/{id}/priority` 调整仍在排队命令的优先级，必须填写优先级依据；调整只改变排队顺序，不改写任何已发生的处理事实。只有 pending 命令可以调整优先级，已进入其他状态的命令返回稳定冲突。

### review-safety-interlock

安全复核员通过 `POST /commands/{id}/interlock` 提交复核结论。只有当前队首命令可以进入复核，非队首的待复核命令返回稳定冲突且队列不变。clear 结论会原子占用闸门并签发一次执行令牌；blocked 结论会把命令置为 rejected。其他命令先占用同一闸门时，当前 clear 结论返回冲突且不改变闸门。复核完成后命令出队，下一项按当时真实条件（当前优先级与到达次序）成为新的队首。

### execute-gate-command

闸门操作员通过 `POST /commands/{id}/execute` 提交执行令牌。服务校验命令、令牌、闸门占用关系和目标开度，在临界区内完成 approved、executing、completed 或 aborted 的转换，并同步更新闸门状态。令牌在命令结束后失效，不能复用。

## 状态与规则

- `gate` 状态：idle、reserved、moving、faulted。
- `gate_command` 状态：pending、approved、rejected、executing、completed、aborted。
- 只有 pending 命令可以复核，且必须是当前队首。
- 只有 approved 命令可以执行，且执行令牌必须匹配。
- 只有 pending 命令可以调整优先级，调整必须携带优先级依据。
- 队首按当前优先级降序、到达次序升序动态确定，不提前冻结。
- 同一命令在队列中至多出现一次，重复排队返回稳定冲突。
- 复核完成（clear 或 blocked）即出队；出队后优先级与依据作为历史事实保留在命令上。
- clear 复核与闸门占用必须同时成功；任一步失败时两者都不写入。
- blocked 复核使命令 rejected，不会占用闸门。
- 模拟故障会令命令 aborted、闸门 faulted，并保留最终开度。
- 已完成或已中止的命令不能再次执行。

## 模块边界

- `cmd/server`：进程入口、监听地址和退出控制。
- `internal/httpapi`：HTTP 路由、JSON 解码、状态码映射。
- `internal/coordination`：工作流编排、命令标识和执行令牌签发。
- `internal/domain`：实体、不变量和状态转换规则。
- `internal/storage`：带互斥保护的内存仓库和原子写入。
- `internal/contracts`：API 请求、响应和稳定错误结构。
- `internal/bootstrap`：运行时依赖装配。

## 接口

- `GET /health`
- `POST /gates`
- `GET /gates/{id}`
- `GET /gates/{id}/queue`
- `POST /commands`
- `GET /commands/{id}`
- `POST /commands/{id}/priority`
- `POST /commands/{id}/interlock`
- `POST /commands/{id}/execute`

错误响应统一为：

```json
{"code":"stable_error_code","message":"可读说明"}
```

## 验证计划

本初始化基线刻意不生成单元测试、夹具或浏览器测试文件，也不声明 `test_command`。后续任务阶段负责补充测试，并在该阶段建立红绿验证。当前每条工作流通过 `cmd/workflowcheck` 启动真实 HTTP 服务并调用公开接口，校验成功状态、持久化结果和关键失败分支。
