#!/usr/bin/env bash
# Обновляет бота с GitHub, пересобирает и перезапускает службу.
# Запуск из папки проекта: bash deploy/update.sh
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
export PATH="/usr/local/go/bin:$PATH"
git pull --ff-only
go build -o bot ./cmd/bot
sudo systemctl restart restquiz
sleep 3
systemctl is-active restquiz
journalctl -u restquiz -n 5 --no-pager -o cat || true
