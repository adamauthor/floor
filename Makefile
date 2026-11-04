BIN       := bin/floor
VERSION   := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS   := -s -w -X main.version=$(VERSION)
FLOOR_DIR := $(HOME)/.floor
PLIST     := com.floor.bot.plist

.PHONY: build run test cover lint habits debug-day google-auth install-launchd uninstall-launchd install-tunnel uninstall-tunnel logs deploy-fly help

build: ## Собрать бинарь
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/floor

run: build ## Запустить локально с .env
	@test -f .env || (echo "Нет .env — скопируй .env.example" && exit 1)
	set -a && . ./.env && set +a && ./$(BIN)

test: ## Тесты
	go test -race ./...

cover: ## Покрытие; metrics и render должны держать 80 %
	go test -cover ./...

habits: build ## Показать привычки и якоря, проверить habits.json
	@set -a; [ -f .env ] && . ./.env; set +a; ./$(BIN) habits

google-auth: build ## Войти в Google Health (HEALTH_SOURCE=google)
	@set -a; [ -f .env ] && . ./.env; set +a; ./$(BIN) google-auth

debug-day: build ## Метрики дня: make debug-day DAY=2026-10-31
	@set -a; [ -f .env ] && . ./.env; set +a; ./$(BIN) debug-day $(DAY)

lint: ## go vet + staticcheck
	go vet ./...
	go run honnef.co/go/tools/cmd/staticcheck@latest ./...

install-launchd: build ## Поставить фоновым сервисом на Маке
	@test -f .env || (echo "Нет .env — скопируй .env.example" && exit 1)
	mkdir -p $(FLOOR_DIR)/bin $(FLOOR_DIR)/logs
	@# Новый файл, а не запись поверх: macOS убивает процесс, если подписанный бинарь поменяли на месте.
	cp $(BIN) $(FLOOR_DIR)/bin/floor.new && mv -f $(FLOOR_DIR)/bin/floor.new $(FLOOR_DIR)/bin/floor
	cp .env $(FLOOR_DIR)/.env && chmod 600 $(FLOOR_DIR)/.env
	sed "s|__HOME__|$(HOME)|g" deploy/launchd/$(PLIST) > $(HOME)/Library/LaunchAgents/$(PLIST)
	launchctl bootout gui/$$(id -u)/com.floor.bot 2>/dev/null || true
	@# bootout асинхронный: bootstrap до выгрузки падает с ошибкой 5.
	@for i in 1 2 3 4 5 6 7 8 9 10; do launchctl print gui/$$(id -u)/com.floor.bot >/dev/null 2>&1 || break; sleep 1; done
	launchctl bootstrap gui/$$(id -u) $(HOME)/Library/LaunchAgents/$(PLIST)
	@echo "Floor запущен. Логи: make logs"

uninstall-launchd: ## Остановить и удалить сервис
	launchctl bootout gui/$$(id -u)/com.floor.bot 2>/dev/null || true
	rm -f $(HOME)/Library/LaunchAgents/$(PLIST)

install-tunnel: ## Туннель ngrok, чтобы кнопка меню могла сохранять настройки
	@command -v ngrok >/dev/null || (echo "Нет ngrok: brew install ngrok и ngrok config add-authtoken …" && exit 1)
	mkdir -p $(FLOOR_DIR)/logs
	sed "s|__HOME__|$(HOME)|g" deploy/launchd/com.floor.tunnel.plist > $(HOME)/Library/LaunchAgents/com.floor.tunnel.plist
	launchctl bootout gui/$$(id -u)/com.floor.tunnel 2>/dev/null || true
	@# bootout асинхронный: bootstrap до выгрузки падает с ошибкой 5.
	@for i in 1 2 3 4 5 6 7 8 9 10; do launchctl print gui/$$(id -u)/com.floor.tunnel >/dev/null 2>&1 || break; sleep 1; done
	launchctl bootstrap gui/$$(id -u) $(HOME)/Library/LaunchAgents/com.floor.tunnel.plist
	@echo "Туннель запущен. Бот сам подхватит адрес за минуту."

uninstall-tunnel: ## Остановить и удалить туннель
	launchctl bootout gui/$$(id -u)/com.floor.tunnel 2>/dev/null || true
	rm -f $(HOME)/Library/LaunchAgents/com.floor.tunnel.plist

logs: ## Смотреть логи сервиса
	tail -f $(FLOOR_DIR)/logs/floor.log

deploy-fly: ## Деплой на Fly.io
	fly deploy --config deploy/fly/fly.toml --dockerfile Dockerfile

help: ## Список команд
	@grep -E '^[a-z-]+:.*##' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-20s %s\n", $$1, $$2}'
