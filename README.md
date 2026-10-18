## 闲鱼自动回复系统

该项目基于 [闲鱼自动回复系统](https://github.com/zhinianboke/xianyu-auto-reply) 重构的 **Golang** 版本。

相比于原版的占用，该版本在常驻状态下仅占用不到 **40MB** 的空间。

相比原版，移除了对 **MySQL** 的支持，只使用内置 **SQLite**，节省大量资源。

同时，该版本仅为个人用户使用设计，删除了原项目里各种账号注册、广告显示等功能，只保留核心功能。

> 如果您需要的是开放给其他人使用，请使用原版项目。

**截图：**

<img width="1919" height="910" alt="image" src="https://github.com/user-attachments/assets/b4f6053a-d5d0-4280-a0e5-717add2e111b" />

## CI 与发布

推送到 `main`、提交 Pull Request 或手动运行 Actions 时，CI 会运行 Go 测试、`go vet` 和前端构建。发布时创建 `v` 开头的标签：

```bash
git tag v1.0.0
git push origin v1.0.0
```

标签构建会在 GitHub Release 上传 Linux amd64/arm64、Windows amd64、macOS amd64/arm64 二进制及 SHA-256 校验文件，并发布 `linux/amd64` 和 `linux/arm64` Docker 镜像：

```text
ghcr.io/heartestrella/xianyu-auto-reply-golang:latest
ghcr.io/heartestrella/xianyu-auto-reply-golang:<版本标签>
```

GHCR 登录使用 Actions 自动提供的短期 `GITHUB_TOKEN`，不需要在仓库中保存个人 Token。若改用 Docker Hub 等外部仓库，在仓库 `Settings -> Secrets and variables -> Actions` 配置 `DOCKERHUB_USERNAME` 和 `DOCKERHUB_TOKEN`，并通过 `${{ secrets.* }}` 引用；不要将凭据写入源码、Dockerfile 或 Compose 文件。

## Docker Compose 部署

服务器安装 Docker Engine 和 Docker Compose 插件后，创建部署目录并下载 Compose 文件：

```bash
mkdir -p xianyu-auto-go/data
cd xianyu-auto-go
curl -fsSLO https://raw.githubusercontent.com/Heartestrella/xianyu-auto-reply-golang/main/docker-compose.yml
```

生成随机 JWT 密钥并保存在仅本机可读的 `.env` 文件：

```bash
umask 077
printf 'JWT_SECRET=%s\nPORT=9000\n' "$(openssl rand -hex 32)" > .env
```

公开镜像无需登录 GHCR。若镜像是私有的，使用有 `read:packages` 权限的 GitHub Token 登录：

```bash
echo "$GHCR_TOKEN" | docker login ghcr.io -u YOUR_GITHUB_NAME --password-stdin
```

拉取并启动：

```bash
docker compose pull
docker compose up -d
docker compose ps
docker compose logs -f xianyu
```

打开 `http://服务器地址:9000`。首次登录默认账号密码为 `admin / admin123`，登录后立即修改密码。SQLite 数据与上传文件保存在部署目录的 `data/`，升级时请保留该目录。

升级到最新版本：

```bash
docker compose pull
docker compose up -d
```

部署指定标签时，在 `.env` 中设置 `IMAGE=ghcr.io/heartestrella/xianyu-auto-reply-golang:v1.0.0`，再执行上述升级命令。

## 二进制部署

从 GitHub Releases 下载与操作系统和 CPU 架构对应的程序及 `.sha256` 校验文件。Linux 示例：

```bash
sha256sum -c xianyu-go-linux-amd64.sha256
chmod +x xianyu-go-linux-amd64
mkdir -p data
JWT_SECRET="$(openssl rand -hex 32)" DATA_DIR=./data LISTEN_ADDR=:8089 ./xianyu-go-linux-amd64
```

运行时需要程序配套的 `frontend/dist` 静态文件目录；Release 如未附带该目录，请从源码执行 `cd frontend && npm ci && npm run build`，并将构建出的 `frontend/dist` 放在程序旁。Windows 使用 `xianyu-go-windows-amd64.exe`，启动前设置 `JWT_SECRET`、`DATA_DIR` 环境变量。建议用 systemd、Windows 服务管理器或容器编排工具托管进程。

## 本地开发

```bash
cd frontend
npm ci
npm run build
cd ..
go test ./...
go run ./cmd/server
```

默认监听 `:8089`。可通过 `LISTEN_ADDR`、`DATA_DIR`、`STATIC_DIR` 和 `JWT_SECRET` 配置；对外部署时不要使用默认 JWT 密钥。
