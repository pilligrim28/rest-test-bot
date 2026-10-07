#!/usr/bin/env python3
"""Настраивает Xray на сервере из VPN-подписки и открывает локальный прокси
для бота: HTTP 127.0.0.1:10809 и SOCKS 127.0.0.1:10808.

Запуск (нужен root, Xray должен быть установлен):
    sudo python3 deploy/xray-from-sub.py 'https://ссылка-на-подписку'

Скрипт по очереди пробует серверы из подписки и оставляет первый, через
который открывается api.telegram.org. Ключи из подписки на экран не выводятся.
Потом в .env бота: HTTPS_PROXY=http://127.0.0.1:10809
"""
import base64
import json
import subprocess
import sys
import time
import urllib.parse
import urllib.request

CONFIG_PATH = "/usr/local/etc/xray/config.json"
HTTP_PORT, SOCKS_PORT = 10809, 10808
TEST_URL = "https://api.telegram.org"


def b64decode(s):
    s = s.strip().replace("-", "+").replace("_", "/")
    s += "=" * (-len(s) % 4)
    return base64.b64decode(s).decode("utf-8", "replace")


def fetch(url):
    req = urllib.request.Request(url, headers={"User-Agent": "v2rayN/7.0"})
    body = urllib.request.urlopen(req, timeout=30).read().decode("utf-8", "replace").strip()
    if "://" not in body:
        try:
            body = b64decode(body)
        except Exception:
            pass
    return [l.strip() for l in body.splitlines() if "://" in l]


def q1(qs, key, default=""):
    return qs.get(key, [default])[0]


def stream(net, security, qs, addr=""):
    net = (net or "tcp").lower()
    if net in ("h2", "http"):
        net = "h2"
    if net == "splithttp":
        net = "xhttp"
    s = {"network": net, "security": security or "none"}
    host, path = q1(qs, "host"), urllib.parse.unquote(q1(qs, "path", "/"))
    if net == "ws":
        s["wsSettings"] = {"path": path, "host": host} if host else {"path": path}
    elif net == "grpc":
        s["grpcSettings"] = {"serviceName": urllib.parse.unquote(q1(qs, "serviceName")),
                             "multiMode": q1(qs, "mode") == "multi"}
    elif net == "httpupgrade":
        s["httpupgradeSettings"] = {"path": path, "host": host}
    elif net == "xhttp":
        s["xhttpSettings"] = {"path": path, "host": host, "mode": q1(qs, "mode", "auto")}
    elif net == "h2":
        s["httpSettings"] = {"path": path, "host": [host] if host else []}
    elif net == "tcp" and q1(qs, "headerType") == "http":
        s["tcpSettings"] = {"header": {"type": "http", "request": {
            "path": [path], "headers": {"Host": [host] if host else []}}}}
    sni, fp = q1(qs, "sni") or q1(qs, "peer") or host or addr, q1(qs, "fp")
    if security == "tls":
        t = {"serverName": sni, "allowInsecure": q1(qs, "allowInsecure") in ("1", "true")}
        if fp:
            t["fingerprint"] = fp
        if q1(qs, "alpn"):
            t["alpn"] = urllib.parse.unquote(q1(qs, "alpn")).split(",")
        s["tlsSettings"] = t
    elif security == "reality":
        s["realitySettings"] = {"serverName": sni, "fingerprint": fp or "chrome",
                                "publicKey": q1(qs, "pbk"), "shortId": q1(qs, "sid"),
                                "spiderX": urllib.parse.unquote(q1(qs, "spx"))}
    return s


def outbound(link):
    """Возвращает (название, outbound Xray) или None для неподдерживаемых ссылок."""
    scheme = link.split("://", 1)[0].lower()
    if scheme == "vmess":
        j = json.loads(b64decode(link[8:]))
        qs = {k: [str(v)] for k, v in j.items() if v not in (None, "")}
        qs.setdefault("headerType", [j.get("type", "")])
        sec = "tls" if j.get("tls") == "tls" else ("reality" if j.get("tls") == "reality" else "none")
        return j.get("ps", "vmess"), {"protocol": "vmess", "settings": {"vnext": [{
            "address": j["add"], "port": int(j["port"]), "users": [{
                "id": j["id"], "alterId": int(j.get("aid") or 0), "security": j.get("scy") or "auto"}]}]},
            "streamSettings": stream(j.get("net"), sec, qs, j["add"])}
    u = urllib.parse.urlsplit(link)
    qs = urllib.parse.parse_qs(u.query)
    name = urllib.parse.unquote(u.fragment) or scheme
    if scheme == "vless":
        user = {"id": urllib.parse.unquote(u.username), "encryption": q1(qs, "encryption", "none")}
        if q1(qs, "flow"):
            user["flow"] = q1(qs, "flow")
        return name, {"protocol": "vless", "settings": {"vnext": [{
            "address": u.hostname, "port": u.port, "users": [user]}]},
            "streamSettings": stream(q1(qs, "type"), q1(qs, "security", "none"), qs, u.hostname)}
    if scheme == "trojan":
        return name, {"protocol": "trojan", "settings": {"servers": [{
            "address": u.hostname, "port": u.port, "password": urllib.parse.unquote(u.username)}]},
            "streamSettings": stream(q1(qs, "type"), q1(qs, "security", "tls"), qs, u.hostname)}
    if scheme == "ss":
        rest = link[5:].split("#", 1)[0].split("?", 1)[0]
        if "@" in rest:
            cred, hostport = rest.rsplit("@", 1)
            cred = urllib.parse.unquote(cred)
            if ":" not in cred:
                cred = b64decode(cred)
        else:
            cred, hostport = b64decode(rest).rsplit("@", 1)
        method, password = cred.split(":", 1)
        host, port = hostport.rsplit(":", 1)
        return name, {"protocol": "shadowsocks", "settings": {"servers": [{
            "address": host.strip("[]"), "port": int(port), "method": method, "password": password}]}}
    return None


def config(ob):
    ob = dict(ob, tag="proxy")
    return {"log": {"loglevel": "warning"},
            "inbounds": [
                {"listen": "127.0.0.1", "port": HTTP_PORT, "protocol": "http", "tag": "http"},
                {"listen": "127.0.0.1", "port": SOCKS_PORT, "protocol": "socks",
                 "settings": {"udp": True}, "tag": "socks"}],
            "outbounds": [ob, {"protocol": "freedom", "tag": "direct"}]}


def works():
    r = subprocess.run(["curl", "-s", "-o", "/dev/null", "-m", "12", "-w", "%{http_code}",
                        "-x", f"http://127.0.0.1:{HTTP_PORT}", TEST_URL], capture_output=True, text=True)
    return r.stdout.strip() not in ("", "000")


def main():
    if len(sys.argv) < 2:
        sys.exit(__doc__)
    links = fetch(sys.argv[1])
    candidates = []
    for link in links:
        try:
            parsed = outbound(link)
        except Exception:
            parsed = None
        if parsed:
            candidates.append(parsed)
    print(f"В подписке ссылок: {len(links)}, поддерживаемых: {len(candidates)}")
    if not candidates:
        sys.exit("Не нашёл поддерживаемых серверов (vless/vmess/trojan/ss). "
                 "Пришлите первые 30 символов ответа подписки.")
    for i, (name, ob) in enumerate(candidates, 1):
        with open(CONFIG_PATH, "w") as f:
            json.dump(config(ob), f, ensure_ascii=False, indent=2)
        subprocess.run(["systemctl", "restart", "xray"], check=False)
        time.sleep(2)
        print(f"[{i}/{len(candidates)}] {name}: ", end="", flush=True)
        if works():
            print("Telegram доступен ✅")
            subprocess.run(["systemctl", "enable", "xray"], check=False, capture_output=True)
            print(f"\nГотово. Добавьте в .env бота:\nHTTPS_PROXY=http://127.0.0.1:{HTTP_PORT}")
            return
        print("не работает")
    sys.exit("Ни один сервер не открыл Telegram. Проверьте: systemctl status xray; journalctl -u xray -n 30")


if __name__ == "__main__":
    main()
