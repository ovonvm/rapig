# Rapig 2.0

单文件自托管网盘：一个可执行文件提供网页界面、文件管理、在线播放、分享、离线下载与多账号管理。数据保存在本地磁盘（SQLite + 对象块），前面挂一个反向代理即可对外提供服务。

- **单文件部署**：Go 编译的单个二进制，前端资源已内嵌，无外部依赖。
- **数据自己掌握**：SQLite WAL + 本地对象存储；支持多本地存储根（管理员可配）。
- **多账号**：支持注册（可关）、三标识登录（用户名/邮箱/手机号）、账号注销（3 天宽限）。
- **文件管理**：目录、上传/断点续传、重名冲突处理、复制、移动、重命名、回收站（7 天）、搜索、批量操作。
- **分享**：密码、有效期、次数上限、合并分享，公开页无需登录。
- **在线播放**：全站浮层播放器；音频（频谱、歌词、倍速、记忆续播）、视频（字幕、全屏、播放列表），浏览器原生解码。
- **离线下载**：HTTP/HTTPS 直链 + qBittorrent 磁力/种子（需自备 qB，部署方通过环境变量配置）。
- **组共享空间**：管理员/被委派用户创建组、分配成员权限，组内文件在成员间共享。
- **管理后台**：用户、组、审计日志、系统设置、存储策略；数据可通过脚本备份/恢复。

## 快速开始

### Windows

1. 下载安装包并安装，或解压便携压缩包后双击 `Rapig.exe`。
2. 首次启动会创建管理员账号；记下界面显示的初始密码。
3. 浏览器打开 <http://127.0.0.1:50011> 登录，进入「设置」修改密码。

> 桌面面板需要 .NET 8 桌面运行时（安装包会检测并引导安装）。
> 关闭窗口即停止服务；再次双击 `Rapig.exe` 继续使用原数据。

### Linux

```bash
# 解包（包内含 rapig / install.sh / systemd unit / 配置模板 / 运维脚本）
tar xzf rapig-<ver>-linux-amd64.tar.gz
cd rapig-<ver>-linux-amd64

# 自动安装为 systemd 服务（推荐，需 root）
sudo ./install.sh

# 或者便携直跑（无需 root，数据在可执行文件旁的 data/）
./rapig serve
```

安装完成后会打印访问地址、数据目录和管理员初始密码。默认只监听 `127.0.0.1:50011`，外部访问请参考手册配置 Nginx/Caddy 反向代理。

## 数据与配置

- 数据目录默认是 `<可执行文件目录>/data`（Windows 面板为 `%ProgramData%\Rapig\data`）。
- 初始管理员密码保存在 `<data>/bootstrap/initial-admin.txt`；修改密码后该文件自动删除。
- 主要配置项（flag 或环境变量，`RAPIG_` 前缀）：`-addr`/`RAPIG_ADDR`、`-data`/`RAPIG_DATA_DIR`、`-profile`/`RAPIG_PROFILE`、`-cookie-secure`/`RAPIG_COOKIE_SECURE`、`RAPIG_ALLOW_ORIGINS`；离线下载需要 `RAPIG_QB_URL`、`RAPIG_QB_USERNAME`、`RAPIG_QB_PASSWORD`。
- Linux 安装后可运行 `rapig` 打开运维菜单（服务、doctor、日志、备份、升级、重置管理员密码、卸载）。

## 构建

仓库不提交二进制，发布产物由 GitHub Actions 在打 `v*` tag 时构建（`.github/workflows/release.yml`）。本地构建：

```bash
./scripts/build.sh        # 前端 + Go 核 + Linux 包
./scripts/build-gui.sh    # Windows 桌面面板（需要 .NET 8 SDK）
```

工具链：Go 1.27+、Node 24 + pnpm 11（前端）、.NET 8 SDK（仅 Windows 面板）。

## 文档

完整使用手册（安装、代理、账号、文件、播放、分享、离线下载、管理后台、FAQ）：

**<https://ovonvm.github.io/rapig/>**

手册源码在 [`docs/wiki-src/`](docs/wiki-src/)（Markdown），生成结果在 [`docs/wiki/`](docs/wiki/)。

## 目录结构

```
cmd/            rapig 主程序与启动器
internal/       服务端（HTTP、领域模型、SQL、存储、任务）
web/            Vue 3 前端（构建产物内嵌进二进制）
gui/            Windows 桌面面板（.NET 8 WPF）
migrations/     SQLite 迁移（内嵌）
scripts/        构建、安装、备份、反向代理示例
docs/wiki/      用户手册（GitHub Pages 发布）
```

## 发布

- 当前版本：`v2.0.0`
- Release：<https://github.com/ovonvm/rapig/releases>
