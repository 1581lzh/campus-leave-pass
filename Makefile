# 校园请假通行系统
#
# 常用目标：
#   make css     编译 Tailwind 到 internal/web/static/app.css
#   make build   编译静态二进制到 bin/leavepass（依赖 css）
#   make run     本地运行 go run ./cmd/leavepass
#   make test    运行全部测试
#   make vet     运行 go vet
#   make clean   删除 bin/（不会删除数据库或备份）

.PHONY: all build css test vet run clean

# 默认目标
all: build

# 构建期样式：当前仓库没有 package-lock.json，因此使用 install --no-package-lock。
css:
	cd web/tailwind && npm install --no-package-lock && npm run build

# 静态单二进制；模板与静态资源通过 go:embed 内嵌。
build: css
	mkdir -p bin && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/leavepass ./cmd/leavepass

test:
	go test ./... -count=1

vet:
	go vet ./...

run:
	go run ./cmd/leavepass

# 只清理构建产物，保留数据库、备份与 .env。
clean:
	rm -rf bin
