BIN       := bin/floor
VERSION   := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS   := -s -w -X main.version=$(VERSION)
FLOOR_DIR := $(HOME)/.floor
PLIST     := com.floor.bot.plist

.PHONY: build run test lint install-launchd uninstall-launchd logs deploy-fly

build: ## Собрать бинарь
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/floor

run: build ## Запустить локально с .env
	@test -f .env || (echo "Нет .env — скопируй .env.example" && exit 1)
	set -a && . ./.env && set +a && ./$(BIN)

test: ## Тесты
	go test -race ./...

lint: ## go vet + staticcheck
	go vet ./...
	go run honnef.co/go/tools/cmd/staticcheck@latest ./...

install-launchd: build ## Поставить фоновым сервисом на Маке
	@test -f .env || (echo "Нет .env — скопируй .env.example" && exit 1)
	mkdir -p $(FLOOR_DIR)/bin $(FLOOR_DIR)/logs
	cp $(BIN) $(FLOOR_DIR)/bin/floor
	cp .env $(FLOOR_DIR)/.env && chmod 600 $(FLOOR_DIR)/.env
	sed "s|__HOME__|$(HOME)|g" deploy/launchd/$(PLIST) > $(HOME)/Library/LaunchAgents/$(PLIST)
	launchctl bootout gui/$$(id -u)/com.floor.bot 2>/dev/null || true
	launchctl bootstrap gui/$$(id -u) $(HOME)/Library/LaunchAgents/$(PLIST)
	@echo "Floor запущен. Логи: make logs"

uninstall-launchd: ## Остановить и удалить сервис
	launchctl bootout gui/$$(id -u)/com.floor.bot 2>/dev/null || true
	rm -f $(HOME)/Library/LaunchAgents/$(PLIST)

logs: ## Смотреть логи сервиса
	tail -f $(FLOOR_DIR)/logs/floor.log

deploy-fly: ## Деплой на Fly.io
	fly deploy --config deploy/fly/fly.toml --dockerfile Dockerfile

help: ## Список команд
	@grep -E '^[a-z-]+:.*##' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-20s %s\n", $$1, $$2}'
