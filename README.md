## 闲鱼自动回复系统

该项目基于 [闲鱼自动回复系统](https://github.com/zhinianboke/xianyu-auto-reply) 重构的 **Golang** 版本。

相比于原版的占用，该版本在常驻状态下仅占用不到 **40MB** 的空间。

相比原版，移除了对 **MySQL** 的支持，只使用内置 **SQLite**，节省大量资源。

同时，该版本仅为个人用户使用设计，删除了原项目里各种账号注册、广告显示等功能，只保留核心功能。

> 如果您需要的是开放给其他人使用，请使用原版项目。

> **⚠️ Warning**
> 
> 该项目处于 **测试阶段**，可能存在诸多 bug。
> 
> 不建议直接接替原项目使用。如果您有问题，请返回 [issue](https://github.com/Heartestrella/xianyu-auto-reply-golang/issues) 反馈。

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

## 问题反馈

提交问题时请附上操作系统、安装方式、错误现象和相关日志。请先删除日志中的 Cookie、Token、密码和个人信息。
