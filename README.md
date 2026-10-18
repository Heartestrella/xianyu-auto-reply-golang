# 闲鱼自动回复 Go 版

这是一个用于个人闲鱼账号的自动化管理工具，提供：

- 自动回复和聊天管理
- 商品管理、发布和擦亮
- 订单同步、发货和自动评价
- 商品监控和消息通知
- 图片、视频和素材管理

本版本使用 Go 和 SQLite，安装简单，不需要 MySQL 或 Redis。

## 推荐安装方式：Docker

### 1. 安装 Docker

在服务器安装 Docker 和 Docker Compose。Windows、macOS 用户可以安装 [Docker Desktop](https://www.docker.com/products/docker-desktop/)。Linux 用户请参考 [Docker 官方安装文档](https://docs.docker.com/engine/install/)。

### 2. 创建目录

```bash
mkdir -p xianyu-auto-go/data
cd xianyu-auto-go
```

### 3. 下载配置文件

```bash
curl -LO https://raw.githubusercontent.com/Heartestrella/xianyu-auto-reply-golang/main/docker-compose.yml
```

Windows 用户也可以在浏览器打开上面的链接，将文件保存为 `docker-compose.yml`。

### 4. 启动服务

```bash
docker compose up -d
```

程序会自动下载镜像，并在 `data/` 中保存数据库、上传文件和登录密钥。首次启动不需要手动生成 JWT 密钥。

### 5. 打开管理页面

在浏览器访问：

```text
http://服务器IP:9000
```

首次登录账号：

```text
用户名：admin
密码：admin123
```

登录后请立即修改密码。

## 常用操作

查看运行状态：

```bash
docker compose ps
```

查看运行日志：

```bash
docker compose logs -f
```

停止服务：

```bash
docker compose down
```

升级到最新版本：

```bash
docker compose pull
docker compose up -d
```

升级或重启时不要删除 `data/` 目录，否则会丢失数据库、上传文件和登录密钥。

## 修改端口

默认访问端口是 `9000`。如果端口被占用，在 `docker-compose.yml` 所在目录创建 `.env` 文件：

```env
PORT=8080
```

然后重新启动：

```bash
docker compose up -d
```

此时访问 `http://服务器IP:8080`。

## 使用指定版本

默认使用最新版。需要固定版本时，在 `.env` 中写入：

```env
IMAGE=ghcr.io/heartestrella/xianyu-auto-reply-golang:v1.0.0
```

然后执行：

```bash
docker compose pull
docker compose up -d
```

## 直接运行程序

不使用 Docker 的用户可以从 [Releases](https://github.com/Heartestrella/xianyu-auto-reply-golang/releases) 下载对应系统的压缩包。

解压后，在程序目录运行：

```bash
./xianyu-go-linux-amd64
```

Windows 用户双击 `xianyu-go.exe` 即可启动。程序会自动创建 `data/` 目录和登录密钥。运行时不要删除该目录。

## 安全提示

- 不要把 `data/` 目录上传到 GitHub 或发送给其他人。
- 不要公开 Cookie、账号密码或 Token。
- 不要删除 `data/.jwt_secret`，否则已有登录会话会失效。
- 建议只在自己的服务器或电脑上使用，并通过防火墙限制访问。

## 问题反馈

提交问题时请附上操作系统、安装方式、错误现象和相关日志。请先删除日志中的 Cookie、Token、密码和个人信息。
