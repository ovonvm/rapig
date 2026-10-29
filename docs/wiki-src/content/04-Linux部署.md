<!-- slug: linux -->
<!-- chapter: 开始 -->
# 4. Linux 部署

Linux 上推荐用安装包自带的 `install.sh`：它会安装程序、创建受限用户、注册 systemd 服务、启动并打印初始管理员密码。也可以用便携方式直接前台运行。

## 4.1 安装包方式（推荐）

```bash
tar -xzf rapig-v2.0.0-linux-amd64.tar.gz
cd rapig-v2.0.0-linux-amd64
sha256sum -c SHA256SUMS
sudo ./install.sh
```

`install.sh` 是 `rapig install` 的薄包装，常用参数：

| 参数 | 作用 | 默认 |
|---|---|---|
| `--prefix <dir>` | 安装目录 | `/opt/rapig` |
| `--user <name>` | 运行服务的系统用户 | `rapig` |
| `--port <n>` | 监听端口 | `50011` |
| `--data <dir>` | 数据目录 | `<prefix>/data` |
| `--no-service` | 只复制文件，不注册 systemd | 关 |
| `--dry-run` | 只打印将要做什么（可不用 root） | 关 |
| `--no-credentials` | 安装摘要中不打印初始管理员密码 | 关 |

安装完成后：

```bash
rapig doctor      # 自检
rapig             # 运维菜单
```

安装摘要里会显示访问地址、数据目录、配置文件和 `admin` 的初始密码；密码同时写入 `<data>/bootstrap/initial-admin.txt`，首次修改密码后自动删除。

## 4.2 便携方式

```bash
chmod +x rapig
./rapig serve
```

- 不需要 root，也不需要 systemd。
- 数据默认写在程序旁边的 `data/` 目录（可用 `-data /path/to/data` 指定）。
- `Ctrl+C` 会优雅退出。
- 适合临时试用，或放进自己的进程管理器（如 supervisord）里托管。

## 4.3 服务管理

```bash
sudo systemctl status rapig     # 查看状态
sudo systemctl restart rapig    # 重启（改配置后）
sudo systemctl stop rapig       # 停止
sudo systemctl disable rapig    # 取消开机自启
journalctl -u rapig -f          # 跟随日志
```

如果安装时用了 `--no-service`，可以手动生成 unit 模板再放到 systemd：

```bash
rapig gen-unit --prefix /opt/rapig --port 50011 > rapig.service
sudo cp rapig.service /etc/systemd/system/rapig.service
sudo systemctl daemon-reload
sudo systemctl enable --now rapig
```

## 4.4 目录与权限

| 路径 | 内容 | 权限建议 |
|---|---|---|
| `/opt/rapig/rapig` | 程序 | 0755 |
| `/opt/rapig/rapig.env` | 配置（环境变量） | 0600 |
| `/opt/rapig/data/` | 数据库、文件、初始密码 | 0700，属主为运行用户 |
| `/opt/rapig/backup/` | 备份输出 | 运行用户可写 |

手工拷贝或移动过数据目录后，务必修正属主：

```bash
sudo chown -R rapig:rapig /opt/rapig/data
```

## 4.5 常用运维命令

| 目的 | 命令 |
|---|---|
| 自检 | `sudo rapig doctor` |
| 交互菜单 | `sudo rapig` |
| 修改地址/端口 | `sudo rapig config`（改完 `sudo rapig service restart`） |
| 备份 | `sudo /opt/rapig/scripts/backup.sh` |
| 恢复演练 | `sudo /opt/rapig/scripts/restore.sh --list` |
| 升级 | `sudo rapig upgrade --binary <新二进制>` |
| 重置管理员密码 | `sudo rapig reset-admin-password` |

## 4.6 升级

```bash
sudo ./scripts/backup.sh                      # 先备份
sudo rapig upgrade --binary ./rapig-v2.0.1 --prefix /opt/rapig --port 50011
```

`rapig upgrade` 会：停止服务 → 备份现有二进制 → 替换 → 启动 → 等健康检查通过；失败会自动换回旧二进制。旧的二进制会保留为 `rapig.bak-<时间戳>`，需要时手工回滚。

源码仓库里还有 `scripts/upgrade.sh <新二进制>`（不在安装包内），流程类似：自动备份 + 原子替换 + 失败回滚。

## 4.7 卸载

```bash
sudo rapig uninstall
```

会停止并禁用服务、删除 unit、删除安装目录（**包括 data 数据**）和系统用户。卸载前请先备份；如果只想保留数据后重装，请先手工拷走 `<prefix>/data`。
