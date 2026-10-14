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

GitHub Actions：

- **`ci.yml`**（push / PR 到 `main`）：`go vet` + `go test -race ./...` + `go build`，以及 VitePress 文档构建；环境预装 ffmpeg，端到端测试可实际运行；
- **`pages.yml`**（push 到 `main`）：构建文档并部署到 GitHub Pages（`https://maicarons.github.io/hymREC/`）；
- **`release.yml`**（推送 `v*` tag，或手动指定 tag）：先 `go vet` + `go test`，再交叉编译五个平台二进制并创建 GitHub Release。

## 发布流程

1. 打 tag 并推送，Release workflow 随即触发：

   ```bash
   git tag v0.1
   git push origin v0.1
   ```

2. 交叉编译产物（文件名含项目名与版本号，7.4 MB 左右）：

   | 平台 | 文件名 |
   | --- | --- |
   | Linux x86_64 | `hymREC_v0.1_linux-amd64` |
   | Linux ARM64 | `hymREC_v0.1_linux-arm64` |
   | macOS Intel | `hymREC_v0.1_darwin-amd64` |
   | macOS Apple Silicon | `hymREC_v0.1_darwin-arm64` |
   | Windows x86_64 | `hymREC_v0.1_windows-amd64.exe` |

   均为 `CGO_ENABLED=0` 静态构建，运行前需自行安装 ffmpeg。

3. 本地复现与验证版本号：

   ```bash
   make build-windows VERSION=v0.1
   ./bin/hymREC.exe -version        # 输出 hymREC v0.1
   ```

4. 文档随 `main` 自动部署，无需手动发布。

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
