# Развёртывание «встречалки»

Отдельный пользователь, отдельная папка и systemd-юнит: сервис никак не
пересекается с другими приложениями на сервере (ни папками, ни Docker, ни
reverse-proxy) и не затрагивается их деплоем.

```bash
# локально: статичный бинарник
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o rendezvous ./cmd/rendezvous

# на сервере, от root (первая установка)
id voice2us >/dev/null 2>&1 || useradd --system --no-create-home --shell /usr/sbin/nologin voice2us
install -d -o root -g root -m 755 /opt/voice2us
install -o root -g root -m 755 rendezvous /opt/voice2us/rendezvous
install -m 644 voice2us-rendezvous.service /etc/systemd/system/
systemctl daemon-reload && systemctl enable --now voice2us-rendezvous
ufw allow 8787/tcp          # если включён ufw

# проверка
curl -s http://127.0.0.1:8787/healthz     # ok
```

Обновление: заменить `/opt/voice2us/rendezvous` и `systemctl restart voice2us-rendezvous`
(состояние хранится только в памяти — перезапуск прерывает лишь текущие приглашения).
