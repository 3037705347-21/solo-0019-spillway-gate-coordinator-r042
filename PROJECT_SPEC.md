# 泄洪闸门操作协调服务

## 项目目标

该服务用于水工控制室协调泄洪闸门的命令申请、安全联锁复核和单次开度调整。系统在同一闸门上只允许一条已通过复核且尚未结束的执行命令，任何冲突都必须被显式拒绝，不能覆盖已经占用的闸门。

## 使用角色

- 运行协调员：登记闸门并提交开度调整命令。
- 安全复核员：提交联锁结论，决定命令是否通过。
- 闸门操作员：凭执行令牌启动命令并登记执行结果。

## 核心实体

- `gate`：闸门及其当前开度、运行状态、最大开度和活动命令。
- `gate_command`：一次开度调整申请，包含目标开度、申请原因、状态和执行令牌。
- `interlock_decision`：安全复核结论，状态为 clear 或 blocked。

## 业务流程

### register-gate

运行协调员通过 `POST /gates` 登记闸门。服务校验标识、名称、最大开度和初始开度，写入空闲状态；重复标识返回稳定冲突错误，不覆盖原记录。

### request-gate-command

运行协调员通过 `POST /commands` 提交开度调整命令。服务读取闸门当前开度和限制，校验目标开度与申请信息，创建待复核命令并冻结起始开度。命令在同一事务内进入该闸门的待复核队列尾部，默认优先级 0，依据为初始排队的 FIFO 顺序。

### review-safety-interlock

安全复核员通过 `POST /commands/{id}/interlock` 提交复核结论。服务先按存储中的真实状态确认该命令仍是 pending 且是同闸门待复核队列的当前队首；非队首、已离开队列或已进入其他状态的命令一律冲突拒绝且不写入。clear 结论会原子占用闸门并签发一次执行令牌；blocked 结论会把命令置为 rejected。复核提交成功时队首同步出队，下一项按当时仍真实处于 pending 的条目重新计算为队首。其他命令先占用同一闸门时，当前 clear 结论返回冲突且不改变闸门。

### gate-review-queue

运行协调员通过 `GET /gates/{id}/review-queue` 读取同闸门待复核队列：有效顺序（高优先级在前，同优先级按入队序号 FIFO）、每条命令的当前优先级、优先级依据、完整调整历史、位置以及当前队首。

通过 `POST /commands/{id}/priority` 调整一条待复核命令的优先级，必须提供新优先级（-1000 到 1000）、依据和操作人；调整只向该条目的历史追加一条事实，从不改写已有记录。相同优先级与相同依据的重复提交是无操作。命令离开 pending 或已离开队列后不能再调整。

通过 `POST /commands/{id}/enqueue` 可对仍为 pending 但不在队列中的命令显式排队；对已经在队中的命令重复排队返回稳定冲突，位置、优先级和历史均不改变。

### execute-gate-command

闸门操作员通过 `POST /commands/{id}/execute` 提交执行令牌。服务校验命令、令牌、闸门占用关系和目标开度，在临界区内完成 approved、executing、completed 或 aborted 的转换，并同步更新闸门状态。令牌在命令结束后失效，不能复用。

## 状态与规则

- `gate` 状态：idle、reserved、moving、faulted。
- `gate_command` 状态：pending、approved、rejected、executing、completed、aborted。
- 每个闸门持有一个 `review_queue`，条目仅包含仍待复核的命令；队首为优先级最高者，同优先级按入队序号（提交 FIFO）。
- 只有 pending 命令可以复核，并且必须是所属闸门队列的当前队首。
- 队首复核提交后原子出队；下一队首按存储中仍真实为 pending 的条目重算，而不是按假设顺序。
- 优先级调整只追加依据历史（优先级、依据、操作人、时间），不覆盖既有事实；相同优先级加相同依据不产生新事实。
- 重复排队、对已离开 pending 或已离开队列的命令调整优先级或排队，均返回稳定冲突且不写入。
- 只有 approved 命令可以执行，且执行令牌必须匹配。
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
- `GET /gates/{id}/review-queue`
- `POST /commands`
- `GET /commands/{id}`
- `POST /commands/{id}/enqueue`
- `POST /commands/{id}/priority`
- `POST /commands/{id}/interlock`
- `POST /commands/{id}/execute`

错误响应统一为：

```json
{"code":"stable_error_code","message":"可读说明"}
```

## 验证计划

每条工作流通过 `cmd/workflowcheck` 启动真实 HTTP 服务并调用公开接口，校验成功状态、持久化结果和关键失败分支。`review-queue` 工作流验证队列顺序、优先级依据历史、当前队首、非队首复核冲突、重复排队冲突、相同调整无操作、队首完成后按真实条件前进，以及对已进入其他状态的命令不能改写事实。`internal/domain` 与 `internal/storage` 另含单元测试，包括高并发复核下只有一个赢家、非队首命令状态不变的并发验证。
