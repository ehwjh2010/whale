# LSP (Language Server Protocol) 配置与使用指南

## 概述

Whale 内置 LSP 客户端，通过启动和管理语言服务器子进程，为代码智能功能提供支持。对外暴露 **9 个 LSP 工具**。

### 提供的工具

| 工具名 | 功能 | 参数 |
|---|---|---|
| `lsp_goto_definition` | 跳转到符号定义 | `file_path`, `line` (0-based), `character` (0-based) |
| `lsp_find_references` | 查找符号的所有引用 | `file_path`, `line`, `character`, `include_declaration`(可选) |
| `lsp_hover` | 获取悬停信息（类型、文档） | `file_path`, `line`, `character` |
| `lsp_document_symbol` | 列出文件中的所有符号 | `file_path` |
| `lsp_workspace_symbol` | 跨工作区搜索符号 | `query` |
| `lsp_go_to_implementation` | 查找接口/抽象方法的实现 | `file_path`, `line`, `character` |
| `lsp_prepare_call_hierarchy` | 准备调用层次（函数/方法） | `file_path`, `line`, `character` |
| `lsp_incoming_calls` | 查找所有调用者 | `file_path`, `line`, `character` |
| `lsp_outgoing_calls` | 查找所有被调用者 | `file_path`, `line`, `character` |

所有工具均为只读，需要 `lsp.read` 权限。

---

## 内置默认语言服务器

Whale 内置了 10 种常用语言的默认配置。**无需任何配置文件**，只要对应语言的 LSP 二进制文件在 `PATH` 或已知安装目录中，即可自动启用。

| 语言 | 默认命令 | 文件扩展名 | 安装方式 |
|---|---|---|---|
| **Go** | `gopls` | `.go` | `go install golang.org/x/tools/gopls@latest` |
| **Rust** | `rust-analyzer` | `.rs` | `rustup component add rust-analyzer` |
| **Python** | `pyright` | `.py`, `.pyi` | `npm install -g pyright` |
| **TypeScript** | `typescript-language-server` | `.ts`, `.tsx`, `.js`, `.jsx`, `.mjs`, `.cjs` | `npm install -g typescript-language-server typescript` |
| **C/C++** | `clangd` | `.c`, `.h`, `.cpp`, `.hpp`, `.cc`, `.cxx`, `.hxx` | [安装 clangd](https://clangd.llvm.org/installation.html) |
| **HTML** | `vscode-html-language-server` | `.html`, `.htm` | `npm install -g vscode-langservers-extracted` |
| **CSS** | `vscode-css-language-server` | `.css`, `.scss`, `.less` | `npm install -g vscode-langservers-extracted` |
| **JSON** | `vscode-json-language-server` | `.json`, `.jsonc` | `npm install -g vscode-langservers-extracted` |
| **YAML** | `yaml-language-server` | `.yaml`, `.yml` | `npm install -g yaml-language-server` |
| **Vue** | `vue-language-server` | `.vue` | `npm install -g @vue/language-server` |

---

## 服务器发现机制

Whale 按以下优先级查找 LSP 二进制文件：

1. **`exec.LookPath`** — 标准 PATH 搜索
2. **已知安装目录** — 按操作系统扫描：
   - **Windows**: `~/go/bin`, `~/.cargo/bin`, `~/AppData/Roaming/npm`, `~/AppData/Local/Programs/LLVM/bin`, `C:\Program Files\LLVM\bin`
   - **Linux/macOS**: `~/go/bin`, `~/.cargo/bin`, `~/.local/bin`, `/usr/local/bin`, `/usr/bin`
3. **VS Code 扩展目录** (`~/.vscode/extensions`) — 从已安装的 VS Code 扩展中查找：
   - Pylance → pyright
   - rust-analyzer 扩展
   - Red Hat YAML 扩展
   - Volar (Vue) 扩展

如果二进制文件未找到，工具调用时会返回 `lsp_not_ready` 错误，并附带安装帮助信息。

---

## 自定义配置 (lsp.json)

如需覆盖默认配置或添加新语言服务器，在数据目录（通常为 `~/.whale/`）中创建 `lsp.json` 文件。

### 配置文件位置

```
<dataDir>/lsp.json
```

默认路径：`~/.whale/lsp.json`（Linux/macOS）或 `%USERPROFILE%\.whale\lsp.json`（Windows）。

### 完整配置示例

```json
{
  "servers": {
    "go": {
      "command": "gopls",
      "args": ["-remote=auto"],
      "extensionToLanguage": {".go": "go"},
      "env": {
        "GOPLS_DEBUG": "true"
      },
      "initializationOptions": {},
      "settings": {
        "gopls": {
          "staticcheck": true,
          "completeUnimported": true
        }
      },
      "startupTimeout": 60000,
      "shutdownTimeout": 10000,
      "restartOnCrash": true,
      "maxRestarts": 3,
      "diagnostics": true,
      "install_help": "go install golang.org/x/tools/gopls@latest"
    },
    "python": {
      "command": "pyright",
      "args": ["--stdio"],
      "extensionToLanguage": {".py": "python", ".pyi": "python"},
      "install_help": "pip install pyright"
    },
    "custom-lang": {
      "command": "/usr/local/bin/my-lang-server",
      "args": ["--stdio"],
      "extensionToLanguage": {".myext": "my-lang"},
      "install_help": "see https://example.com/install"
    }
  }
}
```

### ServerConfig 字段说明

| 字段 | 类型 | 必填 | 默认值 | 说明 |
|---|---|---|---|---|
| `command` | string | ✅ | — | 可执行文件名或绝对路径 |
| `args` | []string | ❌ | `[]` | 传递给 LSP 服务器的命令行参数 |
| `extensionToLanguage` | map[string]string | ✅ | — | 文件扩展名到语言 ID 的映射 |
| `transport` | string | ❌ | — | 传输方式（当前仅支持 stdio） |
| `env` | map[string]string | ❌ | — | 注入到服务器进程的环境变量（合并到 OS 环境） |
| `initializationOptions` | any | ❌ | — | LSP `initialize` 请求中的 `initializationOptions` |
| `settings` | any | ❌ | — | LSP `workspace/didChangeConfiguration` 设置 |
| `workspaceFolder` | string | ❌ | 工作区根目录 | 语言服务器的工作区目录 |
| `startupTimeout` | int | ❌ | `30000` (30秒) | 启动超时（毫秒） |
| `shutdownTimeout` | int | ❌ | `5000` (5秒) | 关闭超时（毫秒） |
| `restartOnCrash` | bool | ❌ | `true` | 崩溃后是否自动重启 |
| `maxRestarts` | int | ❌ | `0`（无限） | 最大重启次数，0 表示无限制 |
| `diagnostics` | bool | ❌ | `true` | 是否启用 LSP 诊断推送 |
| `install_help` | string | ❌ | — | 二进制未找到时显示的安装帮助信息 |

### 配置格式兼容性

`lsp.json` 同时支持以下三种历史格式（优先级从高到低）：

1. **`servers` 格式（推荐）**：`{ "servers": { "go": {...}, "python": {...} } }`
2. **`languages` 旧格式**：`{ "languages": [{ "name": "go", "command": "gopls", "extensions": [".go"] }] }`
3. **平铺格式**：`{ "go": {...}, "python": {...} }`

### 注意事项

- 同一文件扩展名只能被一个服务器注册，重复注册会报错并跳过
- 用户配置中的同名语言会**覆盖**内置默认配置
- 验证失败的服务器条目会被跳过并输出日志

---

## 生命周期管理

### 懒启动流程

LSP 服务器采用**非阻塞懒启动**设计：

1. 工具 handler 首先调用 `ReadyClientForFile` 快速检查服务器是否已就绪
2. 若就绪 → 直接使用
3. 若未就绪：
   - **首次访问**：触发后台启动，handler 等待最多 **3 秒**
     - 3 秒内就绪 → 直接使用
     - 3 秒未就绪 → 返回 `lsp_not_ready`，服务器继续在后台启动
   - **后续访问**（服务器仍在启动中）：**立即返回** `lsp_not_ready`，不再等待

### Warmup 预热

`Warmup()` 在应用启动时后台运行：扫描工作区前两层目录的文件扩展名，为匹配的语言预启动服务器。这是纯粹的优化——即使跳过，懒启动也能正常工作。

### 崩溃重启

当 `restartOnCrash` 为 `true`（默认），语言服务器进程异常退出后会自动重新启动。`maxRestarts` 控制最大重启次数（0 表示无限）。

### 服务器状态

| 状态 | 含义 |
|---|---|
| `not_installed` | 二进制未找到 |
| `not used` | 已安装但工作区中无匹配文件 |
| `indexing` | 正在启动或索引中 |
| `loaded` | 就绪可用 |
| `failed` | 启动失败（附带错误原因） |

### 关闭

`Manager.Close()` 依次关闭所有运行中的客户端，每个客户端使用配置的 `shutdownTimeout` 超时。

---

## 错误处理

LSP 工具调用失败时会返回友好错误，指导模型采取后续行动：

| 错误类型 | 错误码 | 描述 | 建议操作 |
|---|---|---|---|
| 无配置 | `lsp_not_ready` | 没有语言服务器配置 | 安装对应语言的 LSP |
| 索引中 | `lsp_not_ready` | 服务器仍在索引工作区 | 等待几秒后重试，或用 `grep` + `read_file` |
| 未安装 | `lsp_not_ready` | 二进制文件未找到 | 按 `install_help` 提示安装 |
| 启动中 | `lsp_not_ready` | 服务器正在后台启动 | 等待几秒后重试，或用 `grep` + `read_file` |
| 连接断开 | `lsp_call_failed` | 连接意外关闭 | 重试（服务器会自动重启），或回退到 `grep` |
| 超时 | `lsp_call_failed` | 请求超时（可能仍在索引） | 重试，或回退到 `grep` + `read_file` |
| 参数非法 | `invalid_args` | `line`/`character` 为负数 | 修正参数 |
| 路径越权 | `permission_denied` | 文件路径逃逸工作区 | 使用工作区内的路径 |
| 通用错误 | `lsp_call_failed` | 其他 LSP 错误 | 重试或回退到 `grep` + `read_file` |

---

## 工具详情

### lsp_goto_definition

```json
{
  "file_path": "src/main.go",
  "line": 42,
  "character": 5
}
```

返回定义位置列表，包含文件路径和行列范围。

### lsp_find_references

```json
{
  "file_path": "src/util.go",
  "line": 10,
  "character": 2,
  "include_declaration": true
}
```

返回所有引用位置。结果上限 50 条。

### lsp_hover

```json
{
  "file_path": "src/types.ts",
  "line": 15,
  "character": 8
}
```

返回 markdown 格式的类型信息和文档。

### lsp_document_symbol

```json
{
  "file_path": "src/app.go"
}
```

返回层级符号树，符号类型包括：`function`、`method`、`class`、`struct`、`interface`、`variable`、`constant` 等 23 种。

### lsp_workspace_symbol

```json
{
  "query": "handleRequest"
}
```

跨所有运行中的语言服务器搜索符号，返回合并结果。上限 50 条。

### lsp_go_to_implementation

```json
{
  "file_path": "src/types.go",
  "line": 5,
  "character": 10
}
```

查找接口或抽象方法的具体实现，返回实现位置列表。

### lsp_prepare_call_hierarchy

```json
{
  "file_path": "src/main.go",
  "line": 20,
  "character": 5
}
```

准备函数/方法的调用层次信息，返回 CallHierarchyItem 列表。

### lsp_incoming_calls

```json
{
  "file_path": "src/main.go",
  "line": 20,
  "character": 5
}
```

查找所有调用该位置函数/方法的调用者（内部链式调用 prepareCallHierarchy + incomingCalls）。

### lsp_outgoing_calls

```json
{
  "file_path": "src/main.go",
  "line": 20,
  "character": 5
}
```

查找该位置函数/方法调用的所有被调用者（内部链式调用 prepareCallHierarchy + outgoingCalls）。

---

## 调试与排查

### 检查 LSP 状态

使用 `lsp_workspace_symbol` 工具（任意查询）可间接获取所有语言服务器的可用状态。

### 获取可用摘要

`Manager.AvailableSummary()` 返回格式化的状态报告，列出每个语言、状态、扩展名和二进制路径。

### 常见问题

1. **工具返回 `lsp_not_ready`** — 确认对应的 LSP 二进制已安装且在 PATH 中。首次启动可能需要数秒，重试即可。
2. **首次调用慢** — 语言服务器在首次工具调用时后台启动，可能需要等待 3 秒。后续调用立即返回。
3. **索引期间超时** — 大型项目首次打开时 LSP 服务器需要索引。建议等待数秒后重试。
4. **Windows 上路径问题** — URI 转换自动处理 `C:\path\to\file` ↔ `file:///C:/path/to/file` 的转换。

---

## 架构概览

```
app/app_tools_init.go
  ├── lsp.LoadLSPConfig(dataDir/lsp.json)  →  LSPConfig
  ├── lsp.NewManager(cfg, workspaceRoot)   →  Manager
  │     ├── config.Servers[lang]           →  ServerConfig
  │     ├── clients[lang]                  →  Client (subprocess)
  │     │     ├── rpcConn (stdin/stdout JSON-RPC 2.0)
  │     │     ├── LSP methods (9 operations)
  │     │     └── crash restart monitor
  │     └── discovery.go                   →  FindServerForConfig()
  ├── toolset.SetLSPManager(mgr)
  └── mgr.Warmup()                         →  后台扫描 + 启动服务器

tools/catalog_lsp.go
  └── lspTools() → 9 个 toolFn
        ├── lsp_goto_definition
        ├── lsp_find_references
        ├── lsp_hover
        ├── lsp_document_symbol
        ├── lsp_workspace_symbol
        ├── lsp_go_to_implementation
        ├── lsp_prepare_call_hierarchy
        ├── lsp_incoming_calls
        └── lsp_outgoing_calls
```

### 关键文件

| 文件 | 职责 |
|---|---|
| `internal/lsp/config.go` | 配置结构、加载、默认值、旧格式兼容 |
| `internal/lsp/manager.go` | 服务器生命周期管理、懒启动、崩溃重启、状态追踪 |
| `internal/lsp/client.go` | 单语言服务器进程管理、LSP 方法实现、env/settings 传递 |
| `internal/lsp/protocol.go` | LSP 协议类型定义（Position, Range, Location, Symbol, Hover 多格式等） |
| `internal/lsp/jsonrpc.go` | JSON-RPC 2.0 传输层（Content-Length 帧、请求/响应关联、fast-fail） |
| `internal/lsp/discovery.go` | 二进制文件发现（PATH、已知目录、VS Code 扩展目录） |
| `internal/tools/catalog_lsp.go` | LSP 工具注册、参数验证、错误友好化、结果格式化 |
| `internal/app/app_tools_init.go` | 应用启动时的 LSP 初始化入口 |
