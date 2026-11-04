# 校园请假通行系统（campus-leave-pass）

面向校园的请假与门卫核验系统：学生在网页提交请假，班主任/辅导员审批，通过后生成二维码供门卫扫码核验；社区老师可查看所负责楼栋的已批准请假并维护学生宿舍。系统为单二进制 + SQLite，服务端渲染，无外网 CDN 依赖。

> 仓库包含应用代码、构建脚本和部署样例。文档中的部署步骤均为操作说明；实际部署状态、域名、证书与密钥以目标环境为准。

## 1. 角色与核心流程

| 角色 | 创建方式 | 主要能力 |
| --- | --- | --- |
| 学生 `student` | 只能由本班班主任/辅导员导入 XLSX 创建 | 提交/取消请假、查看二维码、查看通知与历史；不能自助注册、不能改宿舍 |
| 班主任 `teacher` | 管理员发放注册码后自助注册 | 管理一个班：审批/驳回/改时间、代本班学生提交请假并可自己审批、生成代理审批链接、导入学生、改班级信息与学生宿舍、导出本班请假 Excel |
| 辅导员 `counselor` | 管理员发放注册码后自助注册 | 管理多个班（每班最多一人），职责同班主任（按班），可导出名下全部班级的请假 Excel |
| 社区老师 `community` | 管理员发放注册码后自助注册 | 自选负责楼栋，查看本楼栋已批准请假（含请假原因和审批意见，不显示联系电话）；按学号搜索并修改学生宿舍；可导出负责楼栋的已批准请假 Excel |
| 管理员 `admin` | 首次启动 seed，或由现有管理员创建 | 报表与查询、按班级/楼栋/专业系或全部导出请假 Excel、注册码管理、班级/用户维护、异常处理；**不参与审批** |

一个账号只有一个角色，学生与教职工入口分离：

- 学生登录 `/login`（学号 + 密码）
- 教职工登录 `/staff/login`（用户名或手机号 + 密码）
- 管理员登录 `/admin/login`（用户名或手机号 + 密码）

### 1.1 学生导入

1. 班主任/辅导员创建班级，并完成“班主任 + 辅导员”配置（导入前两者必须齐全）。
2. 进入 `/t/classes/{id}/students`，下载系统生成的 `.xlsx` 模板（两个 sheet：填写说明、学生名单）。
3. 老师填写姓名、学号、初始密码、宿舍号；班级/专业系/班主任/辅导员列由系统预填，不要修改。
4. 上传导入：**全有或全无**，任一行校验失败则不会创建任何账号；学号全局唯一（大小写不敏感），姓名允许重复。
5. 学生用学号 + 初始密码登录，首次登录会提示尽快改密。

### 1.2 教职工注册码

- 管理员在 `/admin/settings/registration-codes` 生成注册码，可用复选框指定一个或多个角色（班主任/辅导员/社区老师），并设置共享的可用次数、生效与失效时间。
- 明文只在创建时显示一次，数据库只保存 HMAC-SHA256；丢失可删除后重建。
- 列表中的“更新”默认收起，点击后才展开次数、有效期、停用/启用和删除操作；删除注册码不会删除已注册账号，账号与注册码的关联会置空。
- 教职工在 `/staff/register` 输入注册码完成注册；单角色码自动固定角色，多角色码只能在该码允许的角色中选择。
- 管理员不能通过注册码获得，学生没有注册入口。

### 1.3 请假审批

1. 学生提交请假（原因、起止时间；姓名/学号/班级/专业系/班主任/辅导员/宿舍为提交时快照）。
2. 班主任或辅导员审批，先到先得；超过 24 小时未处理由后台任务自动驳回。
3. 通过后生成二维码，学生可查看/打印；驳回、取消、超时、删除都会使二维码与代理链接失效。
4. 审批人可生成代理审批链接 `/a/<token>`（绑定单张假条，有效期到审批截止时间），代理页无需登录，联系电话默认隐藏。

### 1.4 请假记录导出

- 入口：管理员 `/admin/export`，班主任/辅导员 `/t/export`，社区老师 `/c/export`；对应列表页与班级详情页也有“导出 Excel”按钮。
- 默认导出**最近 6 个月**；可选择最近 1 周、1 个月、3 个月、1 年、全部时间，或自定义开始/结束日期。时间按请假开始时间计算。
- 可按学生筛选：填写完整学号或完整姓名；配合“全部时间”即可导出该学生的全部记录，配合自定义范围即可只导出某段时间。
- 权限范围：班主任限本班；辅导员限名下班级；社区老师限负责楼栋的**已批准**请假且**不含联系电话**；管理员可选全部记录、指定班级、指定楼栋或整个专业系。
- 导出为 `.xlsx`，第一个工作表是请假明细，第二个工作表记录导出时间、导出人、范围、时间窗口与筛选条件；单次最多 10 万条。
- 已清理归档但仍在 40 天保留期内的假条也会纳入导出；导出动作写入管理员审计日志。

### 1.5 二维码与门卫核验

- 二维码内容为 `APP_BASE_URL/p/<token>`，门卫打开 `/p/<token>` 查看姓名、学号、班级、宿舍、起止时间与状态（未开始/有效/已过期/无效）。
- 门卫页永远不显示联系电话和请假原因，也不记录扫码日志；nginx 样例对 `/p/`、`/a/` 关闭 access log。

### 1.6 清理与保留策略

- **7 天**：班级无班主任即转“待认领”，满 7 天清理班级与该班全部学生账号（假条另行保留）；管理员软删除的账号保留 7 天，期间可只读查看自己的历史。
- **40 天**：上述清理产生的假条与归档班级保留 40 天，教职工可在 `/archived/leaves` 只读查看；软删除学生的假条同样保留 40 天，到期物理删除。
- 二维码/代理链接随假条状态失效；24 小时超时审批、过期 session、过期注册码校验记录等由后台任务定期清理。

### 1.7 管理员批量管理

- 用户管理：勾选多个账号后可批量停用，统一填写停用原因；仍遵守“不能停用自己、不能停用最后一个可用管理员”的规则，每个账号独立进入 7 天只读保留期。
- 班级管理：勾选多个班级后输入“确认解散”并勾选确认，可批量解散；学生账号保留并转为暂无班级，待审批假条取消，已批准二维码按原规则保留到请假结束。
- 注册码管理：勾选多个注册码后可批量停用、启用或删除；批量删除需勾选确认，删除只移除注册码，已注册账号和注册尝试保留。
- 批量操作逐项复用单条事务与审计语义，部分失败会汇总“成功 / 跳过 / 失败”并列出前几项原因，不会因为一项失败回滚其他项。表单使用原生复选框与 POST，无 JavaScript 时仍可逐项勾选并提交；全选控件仅在 JavaScript 可用时显示。

## 2. 技术栈与目录结构

- Go 1.26（标准库 `net/http` + `html/template` + 原生 JavaScript 渐进增强，静态资源 `go:embed`）
- SQLite（WAL 模式，纯 Go 驱动 `modernc.org/sqlite`，无需 CGO）
- Tailwind CSS v4（构建期编译为 `internal/web/static/app.css`，运行时不加载 CDN）
- 二维码服务端生成、XLSX 导入导出使用 `excelize`、密码使用 bcrypt

```
campus-leave-pass/
  cmd/leavepass/          程序入口（配置加载、HTTP 服务、后台任务）
  internal/app/           数据访问、迁移、seed、业务事务
  internal/config/        环境变量加载与校验
  internal/jobs/          后台任务（超时驳回、待认领清理、TTL）
  internal/secure/        密码哈希、token 等安全工具
  internal/validate/      用户名/手机号/学号/宿舍等校验
  internal/web/           HTTP 路由、handler、中间件、模板与静态资源
    templates/            html/template 页面
    static/               app.css（构建产物，需入库）、app.js（交互）、motion.js（动效）、layout.js（布局过渡）
  web/tailwind/           Tailwind 构建管线（input.css、package.json）
  deploy/                 systemd 与 nginx 样例、部署检查清单
  PLAN.md                 设计与规则说明
  Makefile                构建/测试入口
```

## 3. 本地开发

### 3.1 环境

- Go **1.26**（`go.mod` 要求 1.26.10）
- Node.js **24+** / npm 11+（仅用于编译 Tailwind CSS）

### 3.2 构建样式

```bash
make css
```

等价于 `cd web/tailwind && npm install --no-package-lock && npm run build`，产物写入 `internal/web/static/app.css`。修改模板类名或 `input.css` 后必须重新执行，并把 `app.css` 一起提交（它通过 `go:embed` 编进二进制）。

开发时可另开终端监听：

```bash
cd web/tailwind && npm run watch
```

前端脚本 `app.js`（交互）、`motion.js`（入场/反馈动效）与 `layout.js`（FLIP 布局过渡）都是无依赖的原生 JavaScript，随二进制内嵌。动效参考 anime.js 的 stagger、timeline 与 spring 缓动，布局过渡参考其 `createLayout()` 的 FLIP 思路（先测量、再改 DOM、再反向补偿位移），但都用 Web Animations API 实现，避免引入额外的运行时依赖；`prefers-reduced-motion` 下会自动关闭。

### 3.3 环境变量

至少导出以下变量后再启动（`SESSION_SECRET` 与 `REGISTRATION_CODE_SECRET` 少于 16 字节会拒绝启动）：

```bash
export SESSION_SECRET="$(openssl rand -base64 32)"
export REGISTRATION_CODE_SECRET="$(openssl rand -base64 32)"
export ADMIN_INITIAL_PHONE="13800000000"
make run
```

| 变量 | 必填 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `SESSION_SECRET` | 是 | 无 | CSRF/二维码/重置限流等派生密钥，**至少 16 字节**；轮换会使这些派生值失效，但不会撤销已登录会话；强制下线请用个人资料页“退出其他设备”或改密码 |
| `REGISTRATION_CODE_SECRET` | 是 | 无 | 注册码 HMAC 密钥，**至少 16 字节**；轮换会使全部注册码失效 |
| `ADMIN_INITIAL_PHONE` | 首次启动必填 | 无 | 初始管理员手机号（11 位）；仅当库中还没有管理员时使用 |
| `ADMIN_INITIAL_PASSWORD` | 否 | `AA` | 初始管理员密码，1–19 字符；已有管理员时不会覆盖 |
| `ADMIN_INITIAL_NAME` | 否 | `LOLPK` | 初始管理员用户名 |
| `APP_BASE_URL` | 否 | `http://127.0.0.1:8080` | 对外访问地址，用于二维码内容与绝对链接；必须以 `http://` 或 `https://` 开头 |
| `APP_LISTEN` | 否 | `127.0.0.1:8080` | HTTP 监听地址；生产由 nginx 反代，不要直接暴露 |
| `DB_PATH` | 否 | `leavepass.db` | SQLite 文件路径；启动时自动建目录、迁移并把文件权限设为 0600 |
| `CAMPUS_BUILDINGS` | 否 | 空 | 逗号分隔的楼栋列表，作为社区老师选择楼栋的候选 |
| `APP_INSECURE_COOKIES` | 否 | `0`（即 Secure） | 设为 `1` 时 Cookie 不带 Secure，**仅限本地 HTTP 调试**，生产必须为 `0` |
| `LOG_LEVEL` | 否 | `info` | `debug` / `info` / `warn` / `error` |
| `TZ` | 否 | 系统时区 | 建议 `Asia/Shanghai`；业务日期边界固定按 CST 计算 |

`.env.example` 提供了可直接复制的模板（敏感值请自行生成，不要提交真实密钥）。

### 3.4 启动

```bash
make run
# 等价于 go run ./cmd/leavepass
```

首次启动会：

1. 创建/迁移 SQLite 数据库；
2. 若库中没有任何管理员，则用 `ADMIN_INITIAL_NAME` / `ADMIN_INITIAL_PASSWORD` / `ADMIN_INITIAL_PHONE` 创建初始管理员（`password_is_initial=1`）；若此时未配置 `ADMIN_INITIAL_PHONE`，启动失败并提示；
3. 启动后台任务与 HTTP 服务。

seed 是幂等的：已有管理员时不会重置密码，也会忽略 `ADMIN_INITIAL_PHONE`。初始管理员登录后请尽快改密；系统不会打印任何 bootstrap 注册码。

健康检查：`GET /healthz`。

### 3.5 首次使用顺序

1. 用初始管理员账号登录 `/admin/login`。
2. 打开 `/admin/settings/registration-codes`，分别为班主任、辅导员、社区老师生成注册码（设置次数与有效期），私下分发。
3. 教职工在 `/staff/register` 注册；班主任创建班级，并邀请辅导员加入。
4. 班主任/辅导员在班级页下载模板、填写并导入学生。
5. 学生用学号登录提交请假；审批通过后出示二维码。

## 4. 构建与测试

```bash
make css     # 编译 Tailwind（build 的前置）
make test    # go test ./... -count=1
make vet     # go vet ./...
make build   # css + 静态编译，产物 bin/leavepass
make clean   # 仅删除 bin/，不会删除数据库或备份
```

`make build` 使用 `CGO_ENABLED=0 go build -trimpath -ldflags="-s -w"`，产出单文件 `bin/leavepass`（模板与静态资源已内嵌）。部署时把该二进制放到 `/opt/leavepass/leavepass`。

## 5. 备份与恢复

SQLite 运行在 WAL 模式，**不要直接复制 `.db` 文件**做备份（可能缺少 WAL 中的数据），使用 SQLite 自带的在线备份：

```bash
# 方式一：.backup
sqlite3 /opt/leavepass/leavepass.db ".backup '/opt/leavepass/backups/leavepass-$(date +%F).db'"

# 方式二：VACUUM INTO（要求目标文件不存在）
sqlite3 /opt/leavepass/leavepass.db "VACUUM INTO '/opt/leavepass/backups/leavepass-$(date +%F).db'"
```

要点：

- 备份目录 `backups/` 与备份文件权限设为 `0600`（目录 `0700`），仅服务用户可读。
- 保留最近 **7 天**，过期文件及时删除（可用 cron/systemd timer，自行确认后再配置）。
- 恢复：停止服务 → 把选定的备份复制为 `DB_PATH` 指向的文件 → 确保属主为服务用户、权限 0600 → 启动服务。恢复前先保留当前库的副本，避免误覆盖。
- 备份期间服务可以继续运行；`.backup` / `VACUUM INTO` 会生成一致快照。

## 6. 安全注意事项

- `SESSION_SECRET` 轮换会使 **CSRF token、门卫二维码和重置限流键立即失效**，但**不会撤销已经登录的会话**。需要让某个账号的其他设备下线时，该账号可在个人资料页执行“退出其他设备”；改密码会清掉该账号全部会话。系统不提供跨账号的全局撤销。
- `REGISTRATION_CODE_SECRET` 轮换会**使所有已发放的注册码立即失效**，只能由管理员重新生成；该密钥需长期妥善保存。
- 两个密钥都不要写进仓库、日志或截图；使用 `openssl rand -base64 32` 生成并保存在 `.env`（0600）。
- 生产必须使用 HTTPS，并保持 `APP_INSECURE_COOKIES` 未设置（默认 Secure Cookie）。
- 应用只监听 `127.0.0.1:8080`，由 nginx 终止 TLS 并反代；`APP_BASE_URL` 必须是外部可访问的 https 地址，否则二维码指向错误。
- **Cloudflare 橙云（代理开启）时必须处理真实 IP**：在 nginx 配置 `real_ip_header CF-Connecting-IP` 与 Cloudflare 官方 `set_real_ip_from` 段，否则所有请求共享 Cloudflare 出口 IP，登录/注册限流会误伤全站。也可以把 DNS 改为 **DNS-only（灰云）** 直接回源，二者选其一。样例见 `deploy/nginx-leave.lolpk.icu.conf` 中注释块。
- 注册码明文只显示一次，数据库只存 HMAC；不要通过公开渠道分发。
- 学生导入文件只在内存解析、不落盘；请勿在模板中复用弱初始密码，并提醒学生首次登录后改密。
- 门卫页 `/p/` 与代理页 `/a/` 不含联系电话与请假原因，nginx 样例关闭了这两个路径的 access log，请勿额外记录 token。

## 7. 部署（仅步骤说明，需人工确认后执行）

样例文件已放在 `deploy/`，部署动作需在目标环境人工执行：

- `deploy/leavepass.service`：systemd 单元样例（独立用户 `leavepass`、`/opt/leavepass`、`EnvironmentFile=/opt/leavepass/.env`、`Restart=always` 等加固项）。
- `deploy/nginx-leave.lolpk.icu.conf`：`leave.lolpk.icu` 的 server block 样例（80→443、TLS 占位符、反代 `127.0.0.1:8080`、1MB 体积限制、导入路由 6MB、`/p/` 与 `/a/` 关闭日志、Cloudflare real_ip 注释块）。
- `deploy/README.md`：部署前检查清单（DNS/证书、密钥生成、目录权限、systemd、`nginx -t`、备份定时器、real_ip 二选一）。

建议顺序（每一步都需要人工确认后再执行）：

1. 确认 `leave.lolpk.icu` 的 DNS 与证书（复用现有 Cloudflare Origin 通配符证书时，先验证覆盖该子域；橙云需配 real_ip，或改灰云）。
2. 创建服务用户与目录：`/opt/leavepass`（二进制、`.env`、数据库、`backups/`）。
3. 生成 `SESSION_SECRET`、`REGISTRATION_CODE_SECRET`，填写 `.env`（0600），首次部署务必设置 `ADMIN_INITIAL_PHONE`。
4. 本机构建 `make build`，把 `bin/leavepass` 上传到 `/opt/leavepass/leavepass`。
5. 安装 systemd 单元 → `systemctl daemon-reload` → 启动并查看日志。
6. 安装 nginx server block → `nginx -t` → reload。
7. 配置每日备份与 7 天保留，验证一次恢复流程。
8. 登录 `/admin`，生成三类教职工注册码并分发。

## 8. 相关文档

- `PLAN.md`：完整设计（角色、数据模型、路由、后台任务、默认值）。
- `web/tailwind/README.md`：样式构建细节与状态类约定。
- `deploy/README.md`：部署检查清单。

## 9. 许可证

本项目采用 **双许可（dual licensing）**：

| 使用方式 | 适用许可 |
| --- | --- |
| 学习、研究、内部使用、按 AGPL-3.0 开源分发 | [GNU AGPL-3.0](LICENSE) |
| 闭源分发、闭源 SaaS、嵌入专有产品 | [商业许可](LICENSE-COMMERCIAL.md) |

Copyright (C) 2026 1581lzh

- 开源许可：[LICENSE](LICENSE)（AGPL-3.0 全文）。
- 商业许可说明与联系方式：[LICENSE-COMMERCIAL.md](LICENSE-COMMERCIAL.md)，邮箱 **lolpk4134@qq.com**。
- 贡献授权（双许可所需）：[CONTRIBUTING.md](CONTRIBUTING.md)。
- 第三方组件与许可证：[THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md)。
- 安全问题请按 [SECURITY.md](SECURITY.md) 私下报告。

简单说：**能遵守 AGPL-3.0 就可以免费使用；不能遵守（例如要做闭源产品）就需要商业许可。** 如果你把修改后的版本作为网络服务提供给用户，AGPL-3.0 要求你向这些用户提供对应的完整源码。
