# 构建与发布

## 本地构建

```bash
# 当前平台
make build                 # 输出 ./bin/hymREC

# 交叉编译（GitHub Releases 常用）
make build-linux
make build-darwin
make build-windows
```

版本信息通过 `-ldflags` 注入：

```bash
go build -ldflags "-X main.version=v1.0.0" -o bin/hymREC ./cmd/hymREC
```

## 文档构建

```bash
cd docs
npm install
npm run dev       # 本地预览 http://localhost:5173/hymREC/
npm run build     # 输出 docs/.vitepress/dist
```

## CI

GitHub Actions（`.github/workflows/ci.yml`）：

- **test**：`go vet` + `go test ./...`（含 race）+ VitePress 文档构建；
- 需要 ffmpeg 的端到端测试在 CI 中自动跳过（无 ffmpeg 环境）或按需启用。

## 发布流程

1. 更新 `version` 并打 tag：`git tag v1.0.0 && git push --tags`；
2. GitHub Actions 自动构建三平台二进制并附加到 Release；
3. 同步更新文档（docs 目录随主仓库发布）。

## Docker（可选）

```dockerfile
FROM golang:1.25 AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -o /hymREC ./cmd/hymREC

FROM alpine:3.20
RUN apk add --no-cache ffmpeg
COPY --from=build /hymREC /usr/local/bin/hymREC
ENTRYPOINT ["hymREC"]
```
