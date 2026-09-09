# Zip-Queue

基于队列的批量文件（解压 / 压缩）任务管理系统。提供图形化界面，把散落在各处的压缩包与文件夹统一排队处理，支持压缩效率档位、解压密码轮询、并发数调节，并在任务详情中回显每次压缩实际使用的效率。

## 功能特性

- **解压任务**：支持常见压缩格式（zip、tar、gzip 等），自动尝试配置页维护的解压密码列表（按序号轮询）。
- **压缩任务**：将文件或文件夹打包为 zip，提供四档压缩效率 —— 特快（仅打包不压缩）/ 快 / 中 / 慢，越慢压缩率越高、耗时越长。
- **任务队列**：每个文件 / 文件夹一条独立任务，可单选、批量创建、批量扫描目录创建，支持待处理 / 执行中 / 成功 / 失败 状态流转。
- **运行期配置**：并发任务数（1-4）、压缩效率、批量操作「穿透文件夹」开关均可在配置页修改并持久化，仅影响之后开始的任务。
- **任务详情回显**：基本信息、实时进度（当前文件 / 条目 / 字节）、时间戳、临时路径，压缩任务额外回显**实际使用的压缩效率档位**。
- **容错与恢复**：服务中断时自动清理临时文件，可安全重做的任务自动重新排队；支持手动重试失败任务（原记录保留为历史）。

## 技术栈

| 层 | 技术 |
| --- | --- |
| 后端 | Go 1.26、Gin、GORM、SQLite（`glebarez/sqlite`，纯 Go 无 CGO） |
| 前端 | Vue 3（`<script setup>`）、TypeScript、Quasar 2、Vue Router、Pinia、Vite |
| 打包 | 前端 `dist` 通过 `go:embed` 嵌入后端二进制，单文件分发 |

## 目录结构

```
zip-queue/
├── backend/                 # Go 后端（含嵌入的前端产物 web/dist）
│   ├── cmd/zip-queue/       # 程序入口：加载配置、启动 worker pool、优雅关闭、启动恢复
│   ├── internal/
│   │   ├── api/             # HTTP 接口（任务 / 配置 / 密码 / 文件浏览）
│   │   ├── archive/         # 压缩 / 解压核心（zip、tar、gzip、压缩效率档位）
│   │   ├── model/           # 数据模型（Task / Setting / Password）
│   │   ├── worker/          # 任务执行、并发池、进度回写、中断恢复
│   │   ├── setting/         # 运行期配置读写
│   │   └── db/              # SQLite 打开与自动迁移
│   └── config.yaml          # 运行配置（开发默认）
├── frontend/                # Vue 3 + Quasar 前端
│   └── src/
│       ├── pages/           # 文件浏览、任务列表、任务详情、配置页
│       └── api/             # 后端接口封装
├── Dockerfile               # 多阶段构建（前端构建 → 后端构建 → 运行时）
├── docker-compose.yml       # 容器编排，含数据卷与健康检查
└── README.md
```

## 快速开始

### 方式一：Docker 部署（推荐）

```bash
# 构建并启动
docker compose up -d --build

# 访问 http://localhost:8787
```

数据持久化在名为 `zip-queue-data` 的卷中（`/data`）。如需让容器处理宿主机目录，在 `docker-compose.yml` 中取消注释并修改挂载：

```yaml
volumes:
  - /host/path/to/files:/data/files
```

可选环境变量覆盖（`docker-compose.yml` 中已注释示例）：`ZQ_SERVER_PORT`、`ZQ_WORKER_MAX_CONCURRENT_TASKS`、`ZQ_LOG_LEVEL`。

国内网络构建加速（可选）：

```bash
docker compose build --build-arg NPM_REGISTRY=https://registry.npmmirror.com \
                     --build-arg GO_PROXY=https://goproxy.cn,direct \
                     --build-arg GO_SUMDB=sum.golang.google.cn
```

### 方式二：本地开发

需要 Go 1.26+ 与 Node 22+。

**后端**（默认监听 `:8787`）：

```bash
cd backend
go run ./cmd/zip-queue -config config.yaml
```

**前端**（Vite 开发服务器，默认 `:9000`，已配置 `/api` 代理到后端 `:8787`）：

```bash
cd frontend
npm install
npm run dev
```

开发时访问 `http://localhost:9000` 即可联调。前端独立构建：

```bash
npm run build      # 产出 frontend/dist，供 go:embed 嵌入
```

## 配置说明

后端运行配置见 `backend/config.yaml`：

| 项 | 说明 |
| --- | --- |
| `server.port` | HTTP 监听端口（默认 8787） |
| `database.path` | SQLite 文件路径（相对 backend 工作目录） |
| `worker.max_concurrent_tasks` | 同时执行任务数（1-4，默认 1） |
| `browse.default_path` | 文件浏览器起始路径 |
| `log.level` | 日志级别：debug / info / warn / error |

并发数与压缩效率也可在页面「配置」中调整，二者持久化于 `settings` 表，启动时读回并覆盖 yaml / 环境变量默认值。

## 压缩效率档位

| 档位 | 行为 | 适用场景 |
| --- | --- | --- |
| 特快 fastest | 仅打包不压缩（zip Store） | 追求最快速度、不关心体积 |
| 快 fast | Deflate 最低级别 | 速度优先 |
| 中 normal | Deflate 默认级别（回落值） | 通用 |
| 慢 slow | Deflate 最高级别 | 体积优先、耗时最长 |

每次压缩任务在执行开始时取一次档位并记录到任务，任务详情可回显「当时实际使用的效率」，即便之后在配置页改动也不受影响。

## 主要接口

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/health` | 健康检查 |
| POST | `/api/tasks` | 创建单个任务 |
| POST | `/api/tasks/batch` | 批量创建任务 |
| POST | `/api/tasks/wake` | 手动唤醒调度器（任务卡在待处理时的用户兜底） |
| POST | `/api/tasks/bulk-decompress` | 扫描目录批量解压 |
| POST | `/api/tasks/bulk-compress` | 扫描目录批量压缩 |
| GET | `/api/tasks` | 任务列表（分页 + 筛选） |
| GET | `/api/tasks/:id` | 任务详情 |
| POST | `/api/tasks/:id/retry` | 重试失败任务 |
| DELETE | `/api/tasks/:id` | 删除已结束任务 |
| GET/PUT | `/api/config` | 读取 / 更新运行期配置 |
| GET/POST/... | `/api/passwords` | 解压密码管理 |
| GET | `/api/fs/list` | 文件浏览 |

## 许可证

内部工具，按需使用。
