# Tailwind CSS 构建

本目录是 `campus-leave-pass` 的构建期样式管线。页面使用语义化 class，Tailwind 只负责在构建时把 `input.css` 中的设计系统编译成一份静态 CSS；运行时不加载 Tailwind JS，也不生成内联样式。

## 环境

- Node.js 24+ / npm 11+
- Tailwind CSS v4（`tailwindcss` + `@tailwindcss/cli`，固定 4.3.3）

## 构建

```bash
cd web/tailwind
npm install
npm run build
```

构建产物写入：

```
internal/web/static/app.css
```

该文件由 `internal/web` 的 `go:embed` 打包进二进制，属于入库产物。修改 `input.css`、模板或类名后必须重新构建并提交 `app.css`。

## 开发监听

```bash
cd web/tailwind
npm run watch
```

## 说明

- `input.css` 通过 `@source "../../internal/web/templates"` 扫描 Go 模板。
- 状态类由模板动态拼接（`badge-{{.Status}}`、`flash-{{.FlashKind}}`、`gate-{{.Gate.State}}`），因此在 `input.css` 中显式定义全部变体：`badge-pending/approved/rejected/cancelled`、`flash-ok/error/info/warn`、`gate-valid/invalid/expired/not_started/pending`。新增状态时必须同步补充样式。
- 页面 CSP 为 `style-src 'self'`，不要向模板添加 `style` 属性或内联样式；所有视觉规则都应进入本目录的 `input.css`。
- `node_modules/` 已被 `.gitignore` 忽略，不要提交。
