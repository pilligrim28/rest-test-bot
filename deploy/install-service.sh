#!/usr/bin/env bash
# Устанавливает бота как службу systemd: работает в фоне, сам перезапускается
# после падения и после перезагрузки сервера. Заодно ставит автоперезапуск Xray.
#
# Запуск из папки проекта:   sudo bash deploy/install-service.sh
# Обновить бота потом:       bash deploy/update.sh
set -euo pipefail

if [ "$(id -u)" -ne 0 ]; then
  echo "Запустите через sudo: sudo bash deploy/install-service.sh" >&2
  exit 1
fi

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
RUN_USER="${SUDO_USER:-$(stat -c %U "$DIR")}"
GO_BIN="$(command -v go || echo /usr/local/go/bin/go)"
SERVICE=restquiz

echo "Папка бота:   $DIR"
echo "Пользователь: $RUN_USER"

[ -f "$DIR/.env" ] || { echo "Нет файла $DIR/.env — создайте его и запустите снова." >&2; exit 1; }

# 1. Сборка (от имени пользователя, чтобы файлы остались его).
if [ -x "$GO_BIN" ]; then
  echo "Собираю бота…"
  sudo -u "$RUN_USER" env PATH="$(dirname "$GO_BIN"):$PATH" HOME="$(getent passwd "$RUN_USER" | cut -d: -f6)" \
    bash -c "cd '$DIR' && go build -o bot ./cmd/bot"
fi
[ -x "$DIR/bot" ] || { echo "Нет собранного файла $DIR/bot" >&2; exit 1; }

# Если бот был запущен вручную (nohup ./bot), останавливаем, чтобы не было двух копий.
pkill -u "$RUN_USER" -x bot 2>/dev/null || true

# 2. Служба бота.
HAS_XRAY=0
systemctl list-unit-files xray.service >/dev/null 2>&1 && systemctl cat xray.service >/dev/null 2>&1 && HAS_XRAY=1
AFTER="network-online.target"
WANTS="network-online.target"
if [ "$HAS_XRAY" = 1 ]; then AFTER="$AFTER xray.service"; WANTS="$WANTS xray.service"; fi

cat > /etc/systemd/system/$SERVICE.service <<UNIT
[Unit]
Description=Rest test Telegram bot
After=$AFTER
Wants=$WANTS
StartLimitIntervalSec=0

[Service]
User=$RUN_USER
WorkingDirectory=$DIR
ExecStart=$DIR/bot
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
UNIT

# 3. Xray: перезапуск после любого падения.
if [ "$HAS_XRAY" = 1 ]; then
  mkdir -p /etc/systemd/system/xray.service.d
  cat > /etc/systemd/system/xray.service.d/20-restart-always.conf <<UNIT
[Unit]
StartLimitIntervalSec=0

[Service]
Restart=always
RestartSec=5
UNIT
fi

systemctl daemon-reload
if [ "$HAS_XRAY" = 1 ]; then
  systemctl enable --now xray >/dev/null
  systemctl restart xray
fi
systemctl enable $SERVICE >/dev/null
systemctl restart $SERVICE
sleep 3

echo
if [ "$HAS_XRAY" = 1 ]; then
  echo "Xray: $(systemctl is-active xray)"
else
  echo "Xray не установлен — бот пойдёт в Telegram напрямую (или через HTTPS_PROXY из .env)."
fi
echo "Бот:  $(systemctl is-active $SERVICE)"
echo
journalctl -u $SERVICE -n 5 --no-pager -o cat || true
cat <<TXT

Готово. Бот и Xray работают в фоне и сами перезапускаются после падения и перезагрузки.
  Лог бота:       journalctl -u $SERVICE -f
  Перезапустить:  sudo systemctl restart $SERVICE
  Остановить:     sudo systemctl stop $SERVICE
  Обновить:       bash deploy/update.sh
TXT
