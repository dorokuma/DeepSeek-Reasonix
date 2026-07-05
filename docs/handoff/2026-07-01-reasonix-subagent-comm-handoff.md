# Reasonix 子代理通信重构交接文档 (2026-07-01)

> **⚠ 已过时（勿按本文排障）**  
> 下文 §3.1「废除 auto-reentry」等与当前代码不符：`internal/control/controller.go` 仍使用 `autoReenter` + `Send("")`，结果消费见 `Agent.drainNotify` / `PatchToolResult`。  
> **以仓库内 `docs/spark/2026-07-01-task-async-troubleshooting.md` 为准。**  
> 下文仅保留历史背景与部分已落地项（Context 挂靠 session、通道 close 策略等）供考古。

## 1. 优化背景
在之前的 Reasonix 异步通信重构后，系统在后台运行子任务、多代理协同以及 TUI 交互时，频繁出现严重的 Panic 崩溃和任务被无故杀死的现象，表现为：
- TUI 状态机偶尔锁死，无法通过 Enter 键发送消息或继续对话。
- 子任务在启动后瞬间被杀死，无法完成长周期任务（如 explore/research 技能）。
- 通道关闭时概率性触发 go runtime panic。

## 2. 根本原因诊断
经过对运行轨迹和代码逻辑的深度审计，定位出以下三大根本原因：

### 2.1 自动重入（auto-reentry）与被动 Turn 锁死
- **现象**：当子代理汇报或任务结束触发 `auto-reentry` 回调时，系统通过 `c.Send("")` 触发一个空内容被动 Turn，试图让大模型感知最新状态。
- **病因**：被动 Turn 在执行时会占用 `pendingToolResult` 通道，导致 TUI 状态被硬性置于 `tuiRunning`。由于被动 Turn 并非由用户通过键盘回车触发，用户的键盘 Focus 状态被强行打断，后续的 Enter 键事件被误路由至 `Steer` 分支（即方向舵/转向控制分支），导致 TUI 阻塞在等待输入的死锁状态，对话完全无法继续。

### 2.2 后台子任务生命周期 Context 误用
- **现象**：由子代理发起的后台探路、探索等子任务生存周期极短，刚启动即被强杀。
- **病因**：在启动后台异步 Task 时，传入的 Context 误用了当前 Turn 的生命周期 Context。当该 Turn 结束（TurnDone）时，其对应的 Context 会被立刻 cancel。这导致与之挂靠的后台异步子任务在第一个 Turn 结束时就被系统 Kill 强杀。

### 2.3 `jobs.go` 通道关闭竞态（Panic 隐患）
- **现象**：任务退出或异常时偶尔抛出 `panic: close of closed channel`。
- **病因**：在 `jobs.go` 中，任务退出函数中执行了 `close(j.ackCh)` 和 `close(j.notifyCh)`。然而这些通道在外部有其他的异步发送者，或者在重试/取消流中可能被多次关闭，极易触发 Go 语言的通道关闭竞态 Panic。

## 3. 优化与重构的具体细节

### 3.1 彻底废除 `auto-reentry`
- 移除了所有 `auto-reentry` 回调机制以及大模型重载重入（`c.Send("")`）逻辑。
- 保证大模型在无用户输入时处于完全空闲与闭嘴状态，杜绝由于被动 Turn 引起的键盘路由混淆。

### 3.2 主动通知机制改版
- 后台子任务或子代理发送的消息，不再通过激活新 Turn 的方式强行重入。
- 改为通过 `sink.Emit(event.Notice)` 直接将通知文本渲染到 TUI 屏幕的历史记录中。这既确保了用户能即时看到后台进展，又完全不打断键盘 Focus，实现了真正的随时对话。

### 3.3 双向信箱（inbox）与延迟消费
- 实现了双向 `inbox` 信箱机制。
- 当后台有多个子代理或子任务消息到达时，消息先暂存在信箱中。
- 当用户下一次发起主动 Turn（即敲击 Enter 键发送消息）时，系统通过 `drainNotify()` 延迟消费汇总信箱中的所有通知，将它们合并并作为上下文一并送给大模型，保证大模型对后台状态的完整感知。

### 3.4 修正 Context 挂靠生命周期
- 将后台子任务的 Context 生命周期从短寿命的 Turn 级 Context 改为挂靠到 Session 级同寿的 `m.root` Context。
- 确保后台子任务在 TurnDone 后依然能够平稳运行，直到 Session 结束或任务自主退出。

### 3.5 消除通道 Panic 与加固容错
- 移除了 `jobs.go` 中对 `ackCh` 和 `notifyCh` 冗余的 `close` 语句，依靠 Go GC 自动回收，彻底消除 Panic 隐患。
- 增加了严格的空指针防护，防止在极端并发下出现 nil pointer 解引用。
- 将子任务的超时误杀时间从较短的阈值大幅延长至 120 秒，给复杂的 explore/research 任务留出充足的执行时间。

### 3.6 补全技能回调绑定
- 在 `boot.go` 中，补全了关于 `explore`、`research` 等技能的回调和生命周期绑定，确保其完全接入全新的 Session 级 Context 和通知系统。

## 4. 验证状态
- **清理工作**：已在远程开发机 `eqi12` 上执行 `pkill` 清理了残留的旧版 Reasonix 进程。
- **编译部署**：编译了最新的全局二进制文件，覆盖安装于 `/usr/local/bin/reasonix` 以及本地 `./reasonix`。
- **黑盒测试验证**：
  - 测试了在后台任务运行时，交互式强行插话输入，系统流畅响应，无卡死。
  - 测试了多次按 Esc 键取消当前 Turn，状态机能完美复位。
  - 经历多次高强度并发和取消测试，系统运行极其稳定，未发生任何 Panic。
