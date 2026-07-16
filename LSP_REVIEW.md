# LSP 功能代码审查报告

- **分支**：`feat/LSP`
- **审查范围**：`internal/lsp/*`（协议/传输/发现/客户端/管理器/配置）、`internal/tools/catalog_lsp.go`（工具集成）、`internal/app/app_tools_init.go` 与 `internal/tools/toolset.go`（应用集成）
- **新增规模**：约 4,170 行（`internal/lsp` 6 文件 + 工具 + 集成 + 文档）
- **审查性质**：纯代码审查，未修改任何代码
- **构建状态**：`go build ./internal/lsp/... ./internal/tools/...` 通过；`go vet ./internal/lsp/...` 无告警

---

## 一、总体架构评价

设计整体合理，分层清晰：

```
LSP 子系统
├── protocol.go    LSP 类型 + URI 互转 + Hover 多态解析
├── jsonrpc.go     Content-Length 分帧的 JSON-RPC 2.0 传输层
├── discovery.go   按命令/安装目录/VS Code 扩展定位服务器二进制
├── config.go      lsp.json 配置加载、默认值、扩展→语言归属
├── client.go      单个语言服务器子进程封装 + LSP 握手 + 各类请求
└── manager.go     多语言服务器生命周期编排（懒启动/预热/崩溃重启/状态）
        ▲
        │ (lspToolProvider 接口 适配)
        ▼
internal/tools/catalog_lsp.go   9 个只读代码智能工具
        ▲
        │ SetLSPManager
        ▼
internal/app/app_tools_init.go   装配 + Warmup
```

**优点**
- 通过 `lspToolProvider` 接口把 `Manager` 抽象出来，工具层可注入 mock 测试，耦合度低。
- 工具全部 `readOnly: true` + `capabilities: ["lsp.read"]`，符合仓库既有工具范式（见 `AGENTS.md`）。
- 启动超时、崩溃重启、就绪探测等"工程化"考虑已具备雏形。
- `HoverContents.UnmarshalJSON` 对 LSP 三种 hover 格式的兼容处理是正确的。

**核心架构缺陷（一句话）**：传输层只处理"响应"，不处理"服务器主动发起的请求"（如 `workspace/configuration`、`client/registerCapability`），且客户端进程在应用退出时**从未被优雅关闭**。这两点是集成层面最该先修的。

---

## 二、按模块审查结论与建议

### 模块 1：`internal/lsp/jsonrpc.go`（传输层）

**结论：可用，但有两个边界/协议正确性隐患。**

1. **（中等）分帧头大小写敏感** — `ReadMessage` 用 `strings.HasPrefix(line, "Content-Length:")` 做精确匹配（第 63 行）。LSP 规范要求头部名大小写不敏感，个别服务器（或代理）可能发 `content-length:`。建议统一 `strings.ToLower` 后再判前缀。

2. **（重要）readLoop 把"带 ID 的消息"一律当响应处理** — `readLoop`（第 201–225 行）仅判断 `msg.ID != nil` 就路由到 pending 通道。但 LSP 中**服务器→客户端的请求**（如 `workspace/configuration`、`client/registerCapability`、`window/showMessageRequest`）也同时带 `method` 和 `id`。这类消息会被误当成某个 pending 请求的响应投递，导致：
   - 该消息被错误路由到不相关的 pending 调用；
   - 服务器因收不到自己的请求响应而阻塞/报错。
   
   当前实现等于**只支持单向请求（客户端→服务器）**。对 gopls（声明能力极少）可能勉强可用，但对 `typescript-language-server` / `clangd` / 任何依赖动态注册或配置拉取的服务器会行为异常。
   **建议**：在 `readLoop` 中区分 `msg.Method != ""`（请求）与响应（`msg.Method == ""` 且含 `result`/`error`），对服务器请求要么回 `method not supported`，要么实现最小应答（至少 `workspace/configuration` 回空、`client/registerCapability` 回 `{}`）。

3. **（低）`headerTimeout` 常量未被使用**（第 16 行）。属死代码，可删除或真正用于 header 读取的 `io` 超时控制。

4. **（低）无 Content-Length 上限** — `contentLength` 直接 `make([]byte, contentLength)`。恶意/损坏服务器可声明超大长度导致内存暴涨。建议加一个合理上限（如 64MB）并校验。

---

### 模块 2：`internal/lsp/protocol.go`

**结论：良好，仅小问题。**

1. **（低）`URIToPath` 容错但会静默返回原串** — 解析失败时返回原始 `uri`（第 329 行）。若 URI 非法，下游会把一个非路径字符串当路径用。建议至少在工具层校验 URI 形态，或在此处返回 error。

2. **（低）`SymbolKindName` 未覆盖全部 26 种** — 缺少 20–26（`Key/Null/EnumMember/Struct/Event/Operator/TypeParameter`）的具名映射，会落入 `symbol(N)` 兜底。功能无碍，但展示体验略差，建议补全。

3. `PathToURI`/`URIToPath` 对 Windows 的处理是正确的（`/C:/...` ↔ `C:/...`）。

---

### 模块 3：`internal/lsp/discovery.go`

**结论：逻辑清晰，可移植性与健壮性有小瑕疵。**

1. **（低）`isWindows()` 用 `os.PathSeparator` 判定** — 在极罕见交叉编译场景下不可靠，但实践中可接受。仓库已有 `server_unix.go` 等平台文件，未来可改为 build-tag 变体；当前不必强求。

2. **（低）`knownInstallDirs` 硬编码版本路径** — `filepath.Join(home, "clangd", "clangd_19.1.2", "bin")` 写死了 clangd 版本号（第 68 行）。clangd 升级后即失效，建议改为遍历 `~/clangd/*/bin` 通配匹配。

3. **（低）VS Code 扩展发现仅支持 python/rust/yaml/vue** — 这是有意收敛，问题不大；但 `vscodeExtensionServer` 的 `lang` 遍历顺序依赖 `ExtensionToLanguage` map 的遍历次序（Go map 随机），同一扩展映射多语言时返回哪个不确定。当前默认值无冲突，可忽略。

---

### 模块 4：`internal/lsp/config.go`

**结论：配置加载健壮，有一处状态不一致需留意。**

1. **（低）`claimed` map 与 `Servers` 的一致性由 `RegisterServer` 单点维护**，但 `LoadLSPConfig` 在"用户覆盖默认"分支里手动 `delete(cfg.claimed, ext)` 再 `delete(cfg.Servers, name)`（第 230–233 行）。若用户配置校验失败回退到 `saved`，`claimed` 已被删除又由 `RegisterServer(saved)` 重新注册——逻辑正确，但属于易碎的"先拆后建"模式。建议封装一个 `replaceServer(name, srv)` 辅助函数降低出错面。

2. **（低）`ShouldPushDiagnostics` / `Diagnostics` 字段从未被消费** — 配置支持 `diagnostics`，但客户端从不订阅/处理 `textDocument/publishDiagnostics`（见模块 1、8）。属未实现的"僵尸配置"。要么实现诊断捕获，要么在文档中明确标注"暂不支持"。

3. `LoadLSPConfig` 对不存在的配置文件返回默认配置（不报错）是正确的"可选配置"语义。

---

### 模块 5：`internal/lsp/client.go`（单服务器客户端）

**结论：握手与生命周期管理基本正确，但有几处并发与正确性隐患。**

1. **（重要/正确性）`ensureDocumentOpen` 的 TOCTOU 与"永不重同步"**
   - **重复 didOpen**：第 303–335 行先读 `openDocs[uri]`，若未打开则读文件、`didOpen`、再标记。两个并发调用可对同一 URI 都观察到"未打开"，从而发送两次 `didOpen`。LSP 对重复 didOpen 通常报错或忽略，属逻辑缺陷。建议用 `mu` 保护"检查+标记"为原子段，或发送前再校验。
   - **内容陈旧（更重要）**：`didOpen` 以 `Version: 0` 发送**磁盘当前内容**，且 `openDocs` 缓存后永不再同步。若模型先改文件再立刻查询 LSP，服务器仍持有首次打开的旧内容 → 返回基于陈旧文本的 goto/references 结果。多数服务器（如 gopls）会监听文件变动自行刷新，但依赖文件系统监听是**隐式约定**，并非所有服务器都如此。建议提供 `didChange` 重同步路径（或在每次请求前对"已打开但磁盘已变更"的文件重发 didChange），否则在"编辑后立即查询"的工作流下结果可能失真。

2. **（中等）`Start` 不自带握手超时，完全依赖调用方 ctx** — `client.startupTimeout` 字段被设置但**从未在 `Start` 内部使用**（第 150 行起的 `sendRequest(cctx, "initialize", ...)` 用的是 `cctx`，而 `cctx` 仅是 `ctx` 的 cancel 副本，无 deadline）。若某处直接用 `context.Background()` 调用 `Start`，initialize 握手可能无限阻塞。建议 `Start` 内部自行 `context.WithTimeout(ctx, c.startupTimeout)` 派生子上下文，而非只靠调用方。

3. **（中等）`Start` 并发等待循环无 ctx 感知** — 第 73–81 行的"等待别的 goroutine 启动"循环用 `time.Sleep(50ms)` 轮询，且不检查 `ctx.Done()`。若 ctx 已取消，仍会自旋直到 `starting` 变 false。建议循环中 `select { case <-ctx.Done(): return ...; default: }`。

4. **（低）`Client.Close` 未重置 `starting`** — 关闭后 `starting` 仍可能为 true（极端竞态），下次复用同一 Client 实例时会误判。鉴于 Manager 通常不复用已 Close 的 Client，影响很小，但建议 `Close` 里 `c.starting.Store(false)`。

5. **（低）`caps` 字段被写入但从未读取** — `c.caps = &initResult.Capabilities`（第 213 行）后从未用于"发送请求前先校验服务器是否支持该能力"。因此 `lsp_go_to_implementation` 等会无条件发送，不支持的服务器返回 method-not-found 再由友好错误兜底——能用但浪费一次 RPC。建议要么按 `caps` 短路（直接返回"服务器不支持"），要么删掉 `caps` 字段避免误导。

---

### 模块 6：`internal/lsp/manager.go`（生命周期编排）

**结论：较完整，但存在真实的并发竞态与资源泄漏风险。**

1. **（重要/并发）`clientForServer` 存在"删除—重建"竞态窗口** — 第 261–288 行：发现旧 client 已死 → `delete(m.clients, name)` → 释放锁 → 在锁外执行 `FindServerForConfig` + `client.Start(ctx)`（慢）→ 再次加锁 `m.clients[name] = client`。在这段**锁外的间隙**里，另一个并发的 `clientForServer`/`backgroundStart` 看到 `m.clients[name]` 已被删除，会**再创建一个同名客户端并启动**，两个客户端争抢同一服务器；后写入 map 者胜出，落败者成为"已启动但不在 map 中、永远不会被 Close"的**孤儿进程**。这是进程泄漏的真实来源。
   **建议**：仿照 `backgroundStart` 的做法——在 `Start` **之前**就 `m.clients[name] = client` 预留槽位（用 `isStarting()/alive()` 去重），避免锁外重建。两个函数应统一为同一套"预留—启动—失败回滚"逻辑。

2. **（重要/资源）`Manager.Close()` 从未被应用层调用** — `app_tools_init.go` 创建 `lspMgr` 后只 `Warmup()`，没有任何路径在应用退出/会话结束时调用 `lspMgr.Close()`（已用 grep 确认：除 Manager 内部自检外无外部调用方）。后果：gopls/rust-analyzer 等子进程在 whale 退出时**不会被优雅 shutdown**（`shutdown`→`exit`），只能被 OS 回收或僵死。
   **建议**：在应用/会话生命周期的 `Close`/`Shutdown` 中显式调用 `toolset.LSPManager().Close()`（需给 Toolset 增加一个暴露 Manager 的方法或让 Toolset 自身持有并在其 `Close` 中转发）。

3. **（中等）`IsReady(ext)` 有副作用且是 hack** — 第 331–334 行用 `"test"+ext` 假路径调 `ReadyClientForFile`，而后者会触发 `ensureAsync` → 可能**仅为了"探测就绪"就真的启动一个语言服务器**。且假路径 `test.go` 等纯属拼凑。
   **建议**：增加一个不触发启动的纯查询方法（如 `isConfigured(ext) bool`），`IsReady` 只做查询。

4. **（中等）崩溃重启可能死循环** — `maybeMonitorRestart`（第 196–240 行）在 `srv.MaxRestarts == 0`（默认值 `ShouldRestartOnCrash()==true` 且未设 max）时**无限重试**。若某服务器每次启动都立即崩溃（例如 find 时还在、启动后秒退），会每 1 秒 spawn 一次进程，长期占用。建议：对"启动后立即退出"的服务器加熔断（连续 N 次快速失败则放弃并置 `failed`）。

5. **（低）`scanDir` 仅扫描深度 ≤ 2** — 对超大/深目录会漏掉深层文件，但已用深度上限保护性能，属于合理取舍。注意 `node_modules`/`.git`/`vendor` 已被正确跳过。

6. **（低）`AvailableSummary` 在 `m.extCache==nil` 时返回"scanning..."**，但 Warmup 完成后 `extCache` 才赋值；并发下短暂返回扫描中提示，可接受。

---

### 模块 7：`internal/tools/catalog_lsp.go`（工具集成）

**结论：与既有工具范式一致，处理得当；主要问题在底层（已在上文）。**

1. **（低）`lspProvider()` 在 `lspOverride==nil` 时返回 nil** → `lspTools()` 返回 nil → 9 个工具不注册。这是合理的"按需启用"设计（仅在 `SetLSPManager` 后被启用），与 `AGENTS.md` "Keep new packages focused" 一致。✅

2. **（低）`runLSPPositionOp` 的 `includeDeclaration` 语义** — 默认 `true`（符合文档），但 `lspPositionParams` 生成的 JSON schema 把 `include_declaration` 标为可选且无默认值提示；模型可能不传。功能无碍，建议在前端描述里明确"默认 true"。

3. **（低）结果裁剪上限 50 条硬编码**（第 506、425 行）。对大型结果集是合理的，但建议提成包级常量便于调参。

4. 工具层对所有失败都通过 `lspFriendlyError` 给出"重试或回退 grep"的引导，错误体验设计良好。✅

---

### 模块 8：应用集成（`app_tools_init.go` / `toolset.go`）

**结论：装配正确，但生命周期收尾缺失。**

1. **（重要，同模块 6.2）`lspMgr` 创建后无对应 `Close`** — 见上。需在应用关闭路径补全 `Close()`。

2. **（低）`SetLSPManager` 会覆盖 `symbolOutline`**（toolset.go 第 176 行 `b.symbolOutline = m`）。由于 `SymbolOutline` 既用于 read_file 的 symbol outline 展示，又由 Manager 实现，当前无冲突；但若将来想"同时"挂两个 provider 会互相覆盖。建议明确优先级或合并。

3. **（低）`Warmup()` 在每次 `initAppTools` 都触发后台目录扫描** — 若同一进程内多次初始化（测试/复用场景）会重复扫描。受 `scanning` 原子量去重保护，影响有限。

---

## 三、跨模块主题评估

### 错误处理与边界情况
- **做得好的**：工具层统一了 `lsp_not_ready` / `lsp_call_failed` 错误码，并给出"回退 grep + read_file"的可操作提示；配置加载对非法用户配置有"回退默认"兜底。
- **待补强**：传输层对畸形/超大消息无保护（模块 1.4）；`URIToPath` 静默失败（模块 2.1）；服务器主动请求无应答（模块 1.2）；文件内容陈旧未重同步（模块 5.1）。

### 性能隐患
- 服务器进程未被关闭 → **进程/句柄泄漏**（模块 6.2）。
- 崩溃无限重启 → **空转 spawn**（模块 6.4）。
- `didOpen` 重复发送（模块 5.1）增加无谓 RPC。
- `ClientForFileQuick` 的 3 秒 `time.Sleep(100ms)` 轮询（manager.go 第 150–157 行）在热路径上，但属可接受范围。
- `scanDir` 已受深度上限保护 ✅。

### 并发安全
- `jsonrpc.rpcConn` 的 pending map 用 mutex 保护、channel 缓冲 1、shutdown 关闭通道——设计正确，未发现 send-on-closed-channel 风险 ✅。
- `Manager` 用 `RWMutex` + 原子量，整体方向对，但**模块 6.1 的"删除—重建"竞态**是真实缺陷，需统一预留槽位逻辑。
- `Client` 的 `ready/starting/exited` 用 `atomic.Bool`，但 `Close` 未重置 `starting`（模块 5.4）。

### 代码风格
- 与项目整体一致：tab 缩进、`gofmt` 友好、包注释与函数注释充分、错误用 `%w` 包装、命名清晰。
- `go vet` 与 `go build` 均通过，无编译级问题。
- 仅少量死代码/未消费字段（`headerTimeout`、`caps`、`ShouldPushDiagnostics`、多个 `startupTimeout` 未用）需清理。

### 测试覆盖
- **`internal/lsp` 包无任何测试文件**（`internal/lsp/*_test.go` 不存在）。最复杂的并发代码（rpcConn 关联、Client 启停、Manager 竞态、配置加载回退）完全没有单测，仅靠 `internal/tools/catalog_lsp_test.go`（948 行，含 mock provider）覆盖工具层。
- **建议优先补充**：`jsonrpc` 的帧解析/超时、配置加载回退、Manager 并发启停与崩溃重启、Client 握手失败路径的表驱动测试。

---

## 四、优先级排序的改进建议

| 优先级 | 问题 | 位置 | 建议 |
|---|---|---|---|
| P0 | 服务器进程退出时不关闭（泄漏/僵死） | `manager.go` / `app_tools_init.go` | 在应用/会话关闭路径显式调用 `Manager.Close()`；给 Toolset 暴露关闭转发 |
| P0 | 传输层不处理服务器主动请求 | `jsonrpc.go:201` | readLoop 区分请求/响应；至少应答 `workspace/configuration`、`client/registerCapability` |
| P1 | `clientForServer` 删除—重建竞态 → 孤儿进程 | `manager.go:261` | 统一为"先预留槽位再启动"，与 `backgroundStart` 一致 |
| P1 | 文件内容陈旧未重同步 | `client.go:297` | 增加 didChange 重同步，或对磁盘变更文件重发 |
| P1 | `Start` 无自带握手超时 | `client.go:150` | `Start` 内部 `WithTimeout(c.startupTimeout)` |
| P1 | 崩溃无限重启 | `manager.go:196` | 连续快速失败熔断，避免空转 spawn |
| P2 | `IsReady` 触发副作用启动 | `manager.go:331` | 拆分为纯查询 + 可选启动 |
| P2 | 缺 `internal/lsp` 单测 | `internal/lsp/*` | 补充 rpcConn/配置/Manager 并发/握手失败测试 |
| P2 | `ensureDocumentOpen` 重复 didOpen | `client.go:303` | 原子化"检查+标记"，或发送前再校验 |
| P3 | 分帧头大小写、Content-Length 上限、死代码清理 | `jsonrpc.go` / `config.go` | 见模块 1.1/1.4 与各 P3 项 |
| P3 | `caps` 未被消费、SymbolKind 缺映射 | `client.go` / `protocol.go` | 按 caps 短路或删除字段；补全枚举名 |

---

## 五、审查结论

LSP 功能的**架构分层合理、与既有工具范式集成自然、构建与 vet 干净**，已达"可用原型"水准。但作为将长期运行、管理外部子进程的功能，存在两个必须在合入前解决的 P0 级问题（**进程未优雅关闭**、**传输层不支持服务器主动请求**），以及数个 P1 级并发/正确性问题（竞态孤儿进程、内容陈旧、握手超时缺失、重启死循环）。`internal/lsp` 包**缺少单元测试**是合入前的显著风险点。

建议：先修 P0/P1，补 `internal/lsp` 单测（尤其并发与握手路径），再做合入；P2/P3 可作为后续清理。
