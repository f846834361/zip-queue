# === Stage 1: 前端构建 ===
FROM node:22-alpine AS frontend-builder
WORKDIR /app/frontend
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci
COPY frontend/ ./
RUN npm run build

# === Stage 2: 后端构建（嵌入前端产物） ===
FROM golang:1.26-alpine AS backend-builder
WORKDIR /app/backend
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
# 将前端构建产物拷入 web/dist 供 go:embed 嵌入
COPY --from=frontend-builder /app/frontend/dist ./web/dist/
RUN CGO_ENABLED=0 go build -tags netgo -ldflags "-s -w" -o /out/zip-queue ./cmd/zip-queue

# === Stage 3: 运行时 ===
FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=backend-builder /out/zip-queue ./zip-queue
RUN mkdir -p /etc/zip-queue /data && \
    printf 'server:\n  port: 8787\n  read_timeout: 60s\n  write_timeout: 60s\n\ndatabase:\n  path: /data/zip-queue.db\n\nworker:\n  max_concurrent_tasks: 1\n\nbrowse:\n  default_path: "/"\n\nlog:\n  level: info\n' > /etc/zip-queue/config.yaml
EXPOSE 8787
VOLUME ["/data"]
ENTRYPOINT ["/app/zip-queue", "-config", "/etc/zip-queue/config.yaml"]
