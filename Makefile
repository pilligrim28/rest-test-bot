BINARY := bin/bot
PKG    := ./cmd/bot

.PHONY: help build run test test-race vet fmt tidy lint clean docker-build docker-up docker-down docker-logs

help: ## Показать список команд
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

build: ## Собрать бинарник в bin/bot
	@mkdir -p bin
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o $(BINARY) $(PKG)

run: build ## Запустить бота (читает .env автоматически)
	@set -a; [ -f .env ] && . ./.env; set +a; ./$(BINARY)

test: ## Прогнать тесты
	go test ./...

test-race: ## Прогнать тесты с детектором гонок
	go test -race -count=1 ./...

vet: ## Статический анализ
	go vet ./...

fmt: ## Форматирование кода
	gofmt -w .

tidy: ## Привести go.mod в порядок
	go mod tidy

lint: vet test ## Быстрая проверка перед коммитом

clean: ## Удалить артефакты сборки
	rm -rf bin

docker-build: ## Собрать образ
	docker build -t rest-test-bot:latest .

docker-up: ## Поднять контейнер в фоне
	docker compose up -d --build

docker-down: ## Остановить контейнер
	docker compose down

docker-logs: ## Смотреть логи контейнера
	docker compose logs -f --tail=100
