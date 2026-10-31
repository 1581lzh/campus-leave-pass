# 部署检查清单

本目录只包含 **样例文件与步骤说明**。当前仓库未执行任何部署：没有安装 systemd 单元、没有改 nginx、没有启动服务、没有创建 `/opt/leavepass`。

部署目标：`/opt/leavepass`，域名 `leave.lolpk.icu`，应用监听 `127.0.0.1:8080`，nginx 终止 TLS 后反代。

样例文件：

| 文件 | 用途 |
| --- | --- |
| `deploy/leavepass.service` | systemd 单元样例（用户 `leavepass`、`Restart=always`、`ProtectSystem=strict` 等） |
| `deploy/nginx-leave.lolpk.icu.conf` | nginx server block 样例（80→443、证书占位符、反代、上传 6MB、`/p/` `/a/` 关闭日志、Cloudflare real_ip 注释块） |
| `deploy/README.md` | 本清单 |

## 1. 部署前确认（全部为人工操作）

- [ ] **DNS**：`leave.lolpk.icu` 已解析到目标服务器；确认走 Cloudflare 橙云还是 DNS-only 灰云（见第 8 项）。
- [ ] **证书**：确认现有 Cloudflare Origin 通配符证书覆盖 `leave.lolpk.icu`：
      `openssl x509 -in <cert> -noout -text | grep -A1 "Subject Alternative Name"`。
      把 `deploy/nginx-leave.lolpk.icu.conf` 中的 `CHANGE_ME_ORIGIN_CERT.pem` / `CHANGE_ME_ORIGIN_KEY.pem` 换成真实路径。
- [ ] **端口**：确认 `127.0.0.1:8080` 未被占用（`ss -ltnp | grep 8080`），且 80/443 仍由现有 nginx 使用。
- [ ] **构建**：本机执行 `make build`，确认 `bin/leavepass` 生成；不要把 `go run` 或源码目录当生产运行方式。

## 2. 服务用户与目录

```bash
useradd --system --home-dir /opt/leavepass --shell /sbin/nologin leavepass
install -d -o leavepass -g leavepass -m 0750 /opt/leavepass
install -d -o leavepass -g leavepass -m 0700 /opt/leavepass/backups
```

- [ ] `/opt/leavepass` 属主 `leavepass:leavepass`，权限 `0750`（数据库、`.env`、二进制都在此目录内）。
- [ ] 上传二进制：`install -o leavepass -g leavepass -m 0755 bin/leavepass /opt/leavepass/leavepass`。
- [ ] 服务以非 root 运行，监听 8080 非特权端口，**不需要** `AmbientCapabilities`。

## 3. 密钥与环境变量

```bash
openssl rand -base64 32   # SESSION_SECRET
openssl rand -base64 32   # REGISTRATION_CODE_SECRET
```

- [ ] 以 `.env.example` 为模板创建 `/opt/leavepass/.env`，填入两个随机密钥（各至少 16 字节）。
- [ ] **首次部署必须设置 `ADMIN_INITIAL_PHONE`**（11 位）；库中无管理员时缺失会导致启动失败。已有管理员后会忽略该变量。
- [ ] 设置 `ADMIN_INITIAL_PASSWORD`（默认 `AA`，建议改掉）、`APP_BASE_URL=https://leave.lolpk.icu`、`APP_LISTEN=127.0.0.1:8080`、`DB_PATH=/opt/leavepass/leavepass.db`。
- [ ] 生产不要设置 `APP_INSECURE_COOKIES`（保持 Secure Cookie）；本地 HTTP 调试才用 `=1`。
- [ ] 权限：`chown leavepass:leavepass /opt/leavepass/.env && chmod 0600 /opt/leavepass/.env`。
- [ ] 两个密钥长期保存：`SESSION_SECRET` 轮换会使 CSRF/二维码/重置限流键失效，但不会撤销已登录会话；强制下线请用个人资料页“退出其他设备”或改密码。`REGISTRATION_CODE_SECRET` 轮换使全部注册码失效。

## 4. systemd

```bash
install -m 0644 deploy/leavepass.service /etc/systemd/system/leavepass.service
systemctl daemon-reload
systemctl enable --now leavepass
systemctl status leavepass --no-pager
journalctl -u leavepass -n 50 --no-pager
curl -fsS http://127.0.0.1:8080/healthz
```

- [ ] 单元内 `WorkingDirectory=/opt/leavepass`、`EnvironmentFile=/opt/leavepass/.env`、`ExecStart=/opt/leavepass/leavepass` 与实际路径一致。
- [ ] 首次启动后确认数据库已创建且权限为 `0600`；日志无配置错误。
- [ ] 用初始管理员登录 `/admin/login`，立即改密；随后在“设置 → 注册码”生成班主任/辅导员/社区老师注册码（明文只显示一次）。

## 5. nginx

```bash
install -m 0644 deploy/nginx-leave.lolpk.icu.conf /etc/nginx/conf.d/leave.lolpk.icu.conf
# 先替换证书占位符，再：
nginx -t
systemctl reload nginx
curl -I https://leave.lolpk.icu/healthz
```

- [ ] `nginx -t` 通过后才 reload；失败时不要强制重启。
- [ ] 默认 `client_max_body_size 1m`，仅 `/t/classes/{id}/students/import` 放宽到 `6m`（应用层仍限制 5MB、仅 `.xlsx`）。
- [ ] `/p/`、`/a/` 的 `access_log off` 生效，避免一次性 token 写入日志。
- [ ] 现有 Next.js 等其他站点配置不受影响。

## 6. 备份与保留

```bash
# 手动验证一次（备份文件 0600，目录 0700，保留 7 天）
install -d -o leavepass -g leavepass -m 0700 /opt/leavepass/backups
sqlite3 /opt/leavepass/leavepass.db \
  ".backup '/opt/leavepass/backups/leavepass-$(date +%F).db'"
chmod 0600 /opt/leavepass/backups/*.db
find /opt/leavepass/backups -name 'leavepass-*.db' -mtime +7 -delete
```

- [ ] 用 cron 或 systemd timer 每日执行（内容需人工确认后再启用），保留最近 7 天。
- [ ] 备份使用 `.backup` 或 `VACUUM INTO`，**不要**直接 `cp` WAL 模式下的 `.db`。
- [ ] 至少演练一次恢复：停服务 → 保留当前库副本 → 用备份覆盖 `DB_PATH` → 属主/权限 0600 → 启动并验证 `/healthz` 与登录。
- [ ] 业务侧保留期与备份不同：班级待认领 7 天清理、软删除账号 7 天只读、相关假条与归档班级保留 40 天（详见 README）。

## 7. 上线后检查

- [ ] `/healthz` 返回正常；`/login`、`/staff/login`、`/admin/login` 三个入口可访问。
- [ ] 用手机扫一张测试二维码，确认 `/p/<token>` 只显示必要信息（无联系电话、无请假原因）。
- [ ] 确认注册码可用次数/有效期符合预期；测试一次过期码被拒。
- [ ] `journalctl` 无 panic、无重复报错；磁盘与 `/opt` 余量充足。
- [ ] 确认 `APP_BASE_URL` 为 https 外部地址，否则二维码指向错误。

## 8. Cloudflare 真实 IP（二选一，必须处理）

橙云（代理开启）时，源站看到的客户端 IP 是 Cloudflare 出口地址；不处理会导致所有用户共享同一 IP，登录/注册/IP 限流误伤全站。

- **方案 A（橙云 + real_ip）**：在 nginx `http` 块启用 `deploy/nginx-leave.lolpk.icu.conf` 顶部注释的 `real_ip_header CF-Connecting-IP;` 与 `set_real_ip_from ...;`，按 <https://www.cloudflare.com/ips/> 的最新官方段填写，然后 `nginx -t && systemctl reload nginx`。
- **方案 B（灰云）**：把该子域 DNS 改为 DNS-only，直接回源；此时不要启用 Cloudflare real_ip 段，并确保源站防火墙只允许预期来源。

应用只信任 nginx 覆盖写入的 `X-Real-IP`；不要直接把 8080 暴露到公网。

## 9. 回滚

- 保留上一版二进制（如 `/opt/leavepass/leavepass.prev`）与对应数据库备份。
- 回滚：`systemctl stop leavepass` → 换回二进制/恢复备份 → `systemctl start leavepass` → 验证 `/healthz`。
- 若只是 nginx 配置问题：恢复上一份 conf，`nginx -t` 通过后 reload。

> 再次说明：以上均为待执行步骤，本仓库当前未做任何部署或线上变更。
