// Package web 通过 go:embed 嵌入前端构建产物（位于 dist/ 下）。
// 开发期 dist/ 仅有 .gitkeep 占位，生产构建会把 frontend/dist/* 拷入此目录。
package web

import "embed"

// FS 暴露嵌入的前端文件系统，根下含 "dist/" 前缀。
//
//go:embed all:dist
var FS embed.FS
