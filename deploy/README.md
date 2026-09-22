# AWS EC2 Docker 生产部署手册

本手册把 `kovar-agent-gateway` 和 `kovar-agent-admin-webside` 部署到同一台 EC2：Docker Compose
运行 PostgreSQL、Go Gateway 和 Next.js 管理后台，宿主机 Nginx 只负责公网 HTTP/HTTPS。

## 1. 最终架构与前提

```text
Internet :80/:443
        |
   host Nginx
        |
127.0.0.1:3000 -> admin-web:3000 -> gateway:8080 -> postgres:5432
```

- 推荐 Ubuntu Server 24.04 LTS、`t3.medium`/`t4g.medium` 或更高、4 GiB 内存、30 GiB 加密 gp3。
- 给 EC2 绑定 Elastic IP；否则停机/开机后公网 IP 可能改变，IP HTTPS 证书也会失效。
- 安全组只开 `22` 给你的管理 IP，`80` 给 `0.0.0.0/0` 用于 ACME，`443` 建议仅给管理员 IP。
- 不要公开 `3000`/`8080`/`5432`；Gateway 和 DB 只在 Docker 网络中通信。
- 宿主机不需安装 Go、Node.js、pnpm 或 PostgreSQL。

## 2. Ubuntu 裸机初始化

```bash
sudo apt-get update
sudo DEBIAN_FRONTEND=noninteractive apt-get upgrade -y
sudo apt-get install -y ca-certificates curl git jq nginx openssl snapd unattended-upgrades
sudo timedatectl set-timezone UTC

sudo install -m 0755 -d /etc/apt/keyrings
sudo curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc
sudo chmod a+r /etc/apt/keyrings/docker.asc

. /etc/os-release
echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/ubuntu ${UBUNTU_CODENAME:-$VERSION_CODENAME} stable" \
  | sudo tee /etc/apt/sources.list.d/docker.list >/dev/null

sudo apt-get update
sudo apt-get install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
sudo systemctl enable --now docker nginx
sudo usermod -aG docker "$USER"
sudo dpkg-reconfigure -f noninteractive unattended-upgrades
```

退出 SSH 并重新登录，使 Docker 组权限生效，然后验证：

```bash
docker version
docker compose version
sudo nginx -t
systemctl is-active docker nginx
```

> 如果 EC2 是 Amazon Linux 2023，Docker 用 `sudo dnf update -y && sudo dnf install -y docker git jq nginx openssl`
> 安装，再执行 `sudo systemctl enable --now docker nginx` 和
> `sudo usermod -aG docker ec2-user`。Compose v2 与 Certbot 5.4+ 的安装路径与 Ubuntu 不同；下文命令按
> Ubuntu 24.04 验证。

## 3. 获取两个仓库

```bash
sudo install -d -o "$USER" -g "$USER" /opt/kovar
cd /opt/kovar

git clone https://github.com/LukeDevX/kogit-github.com-LukeDevX-kovar-agent-gateway.git kovar-agent-gateway
git clone https://github.com/LukeDevX/kovar-agent-admin-webside.git kovar-agent-admin-webside
```

目录必须是兄弟关系：

```text
/opt/kovar/
├── kovar-agent-gateway/
└── kovar-agent-admin-webside/
```

## 4. 生成生产环境变量

```bash
cd /opt/kovar/kovar-agent-gateway
cp deploy/production.env.example deploy/production.env
chmod 600 deploy/production.env

printf 'POSTGRES_PASSWORD=%s\n' "$(openssl rand -hex 32)"
printf 'GATEWAY_ADMIN_PASSWORD=%s\n' "$(openssl rand -base64 48 | tr -d '\n')"
printf 'ENCRYPTION_KEY=%s\n' "$(openssl rand -base64 32 | tr -d '\n')"
```

把输出分别写入 `deploy/production.env`，不要把该文件提交到 Git：

```bash
nano deploy/production.env
```

必须复核：

- `POSTGRES_PASSWORD` 使用上述 hex 值，避免 DSN URL 转义问题。
- `GATEWAY_ADMIN_PASSWORD` 是管理后台的登录密码，长度必须为 8..72。
- `ENCRYPTION_KEY` 必须是 32 字节 Base64；产生数据后不能随意更换，否则旧密文无法解密。
- `KOVAR_BASE_URL`/Manage/Model 上游在 production 必须是 HTTPS。
- 三个 `DEFAULT_*_LIMIT` 是 Kovar quota 整数单位，应按业务风险设置。
- `GATEWAY_TIMEOUT_MS` 应大于 Gateway `REQUEST_TIMEOUT`（默认 135000 ms > 130 s）。

## 5. 上线前自动检查

```bash
cd /opt/kovar/kovar-agent-gateway

# 仅检查 Compose 结构；不要在共享终端输出完整 config，其中有展开后的 Secret。
docker compose --env-file deploy/production.env -f docker-compose.prod.yml config --quiet

# Go：编译、go vet、全部测试。
docker build --pull --target check -t kovar-agent-gateway-check .

# Next.js：格式、Lint、类型、Vitest、生产构建、Knip。
docker build --pull --target check -t kovar-agent-admin-check \
  /opt/kovar/kovar-agent-admin-webside
```

人工上线检查：

- [ ] 两个仓库均为要发布的 commit，生产机没有未追踪/未提交文件。
- [ ] `configs/router.json` 中的模型、pricing pointer 和 multiplier 与真实 Kovar 上游一致。
- [ ] 管理员密码已放入受控的 Secret/Password Manager，没有通过聊天或工单发送。
- [ ] EBS 已加密，已定义 PostgreSQL 备份、保留期和恢复演练。
- [ ] 安全组不对公网开放 3000/8080/5432，SSH/443 尽可能限制管理 IP。
- [ ] 准备使用 Elastic IP，并且 80/443 入站在证书签发时可达。

## 6. 构建、迁移和启动

```bash
cd /opt/kovar/kovar-agent-gateway

docker compose --env-file deploy/production.env -f docker-compose.prod.yml build --pull
docker compose --env-file deploy/production.env -f docker-compose.prod.yml up -d postgres
docker compose --env-file deploy/production.env -f docker-compose.prod.yml run --rm migrate
docker compose --env-file deploy/production.env -f docker-compose.prod.yml up -d gateway admin-web
docker compose --env-file deploy/production.env -f docker-compose.prod.yml ps
```

本机验证：

```bash
curl -fsS http://127.0.0.1:8080/health | jq .
curl -fsS http://127.0.0.1:8080/ready | jq .
curl -fsSI http://127.0.0.1:3000/login

docker compose --env-file deploy/production.env -f docker-compose.prod.yml logs \
  --tail=100 postgres gateway admin-web
```

`postgres`/`gateway`/`admin-web` 应为 `healthy`，`/health` 和 `/ready` 应返回 2xx。

## 7. 配置 Nginx HTTP

```bash
cd /opt/kovar/kovar-agent-gateway
export PUBLIC_HOST=203.0.113.10

sudo install -d -m 0755 /var/www/certbot
sed "s/__PUBLIC_HOST__/${PUBLIC_HOST}/g" deploy/nginx/kovar-http.conf.template \
  | sudo tee /etc/nginx/sites-available/kovar >/dev/null
sudo ln -sfn /etc/nginx/sites-available/kovar /etc/nginx/sites-enabled/kovar
sudo rm -f /etc/nginx/sites-enabled/default
sudo nginx -t
sudo systemctl reload nginx

curl -fsSI "http://${PUBLIC_HOST}/login"
```

HTTP 此时可用于健康验证和 ACME challenge，但不能作为正式登录方式：生产会话 Cookie
是 `Secure`，浏览器只会通过 HTTPS 发送。

## 8A. 有域名的 HTTPS（首选）

先将域名 A 记录指向 Elastic IP，并确认 `dig +short admin.example.com` 返回正确 IP：

```bash
sudo snap install --classic certbot
sudo ln -sfn /snap/bin/certbot /usr/local/bin/certbot

export DOMAIN=admin.example.com
cd /opt/kovar/kovar-agent-gateway
sed "s/__PUBLIC_HOST__/${DOMAIN}/g" deploy/nginx/kovar-http.conf.template \
  | sudo tee /etc/nginx/sites-available/kovar >/dev/null
sudo nginx -t
sudo systemctl reload nginx

sudo certbot --nginx -d "$DOMAIN" --redirect
sudo certbot renew --dry-run
curl -fsSI "https://${DOMAIN}/login"
```

## 8B. 没有域名：公网 IP HTTPS

可以。Let's Encrypt 已支持公开信任的 IPv4/IPv6 证书，但 IP 证书只能使用
`shortlived` profile，有效期约 160 小时（约 6 天），因此自动续期和 Elastic IP 是必须的。
Certbot 的 IP webroot 模式需要 5.4 或更高版本。

```bash
sudo snap install --classic certbot
sudo snap refresh certbot
sudo ln -sfn /snap/bin/certbot /usr/local/bin/certbot
certbot --version

export PUBLIC_IP=203.0.113.10
export ACME_EMAIL=you@example.com

# 首次先用 staging 检查 challenge，它不会产生浏览器可信证书。
sudo certbot certonly --staging \
  --preferred-profile shortlived \
  --webroot --webroot-path /var/www/certbot \
  --ip-address "$PUBLIC_IP" \
  --email "$ACME_EMAIL" --agree-tos --no-eff-email

# staging 成功后删除测试证书记录，再申请正式证书。
sudo certbot delete --cert-name "$PUBLIC_IP" --non-interactive
sudo certbot certonly \
  --preferred-profile shortlived \
  --webroot --webroot-path /var/www/certbot \
  --ip-address "$PUBLIC_IP" \
  --email "$ACME_EMAIL" --agree-tos --no-eff-email

cd /opt/kovar/kovar-agent-gateway
sed "s/__PUBLIC_IP__/${PUBLIC_IP}/g" deploy/nginx/kovar-ip-https.conf.template \
  | sudo tee /etc/nginx/sites-available/kovar >/dev/null
sudo nginx -t
sudo systemctl reload nginx

sudo install -d -m 0755 /etc/letsencrypt/renewal-hooks/deploy
sudo tee /etc/letsencrypt/renewal-hooks/deploy/reload-nginx >/dev/null <<'EOF'
#!/bin/sh
systemctl reload nginx
EOF
sudo chmod 0755 /etc/letsencrypt/renewal-hooks/deploy/reload-nginx

sudo systemctl enable --now snap.certbot.renew.timer
sudo certbot renew --dry-run
systemctl list-timers --all | grep certbot
curl -fsSI "https://${PUBLIC_IP}/login"
```

自签名证书虽然能加密，但每台客户端都会报警或需手动安装信任根，不建议用于公网管理后台。

## 9. 完整验收

```bash
cd /opt/kovar/kovar-agent-gateway
export PUBLIC_URL=https://203.0.113.10

curl -fsSI "$PUBLIC_URL/login"
curl -fsS http://127.0.0.1:8080/ready | jq .
docker compose --env-file deploy/production.env -f docker-compose.prod.yml ps
docker compose --env-file deploy/production.env -f docker-compose.prod.yml logs \
  --since=10m postgres gateway admin-web
```

浏览器人工验收：

- [ ] HTTPS 证书可信且匹配域名/公网 IP，无 mixed-content 或 Server Action Origin 错误。
- [ ] 用 `GATEWAY_ADMIN_USERNAME`/`GATEWAY_ADMIN_PASSWORD` 登录成功，Cookie 具有 Secure/HttpOnly/SameSite=Strict。
- [ ] Agent 列表、详情、白名单、批准/拒绝/暂停/恢复/撤销、预算更新正常。
- [ ] Gateway 日志不包含密码、Cookie、Authorization、Kovar Key 或完整 DSN。
- [ ] `sudo reboot` 后 Nginx 和三个容器自动恢复且仍为 healthy。
- [ ] `sudo certbot renew --dry-run` 成功，已监控短效 IP 证书的续期失败。

## 10. 备份、发版、回滚和运维

发版前备份：

```bash
cd /opt/kovar/kovar-agent-gateway
install -d -m 0700 /opt/kovar/backups
docker compose --env-file deploy/production.env -f docker-compose.prod.yml exec -T postgres \
  pg_dump -U kovar -d kovar_gateway -Fc \
  > "/opt/kovar/backups/kovar_gateway_$(date -u +%Y%m%dT%H%M%SZ).dump"
```

更新：

```bash
git -C /opt/kovar/kovar-agent-gateway pull --ff-only
git -C /opt/kovar/kovar-agent-admin-webside pull --ff-only

cd /opt/kovar/kovar-agent-gateway
docker compose --env-file deploy/production.env -f docker-compose.prod.yml build --pull
docker compose --env-file deploy/production.env -f docker-compose.prod.yml run --rm migrate
docker compose --env-file deploy/production.env -f docker-compose.prod.yml up -d gateway admin-web
docker compose --env-file deploy/production.env -f docker-compose.prod.yml ps
docker image prune -f
```

常用运维命令：

```bash
docker compose --env-file deploy/production.env -f docker-compose.prod.yml ps
docker compose --env-file deploy/production.env -f docker-compose.prod.yml logs -f --tail=200 gateway admin-web
docker compose --env-file deploy/production.env -f docker-compose.prod.yml restart gateway admin-web
docker stats
df -h
free -h
```

不要在生产执行 `migrate down`，它会删除本项目业务表和数据。回滚应先恢复上一版 Git commit/镜像；
如果新 migration 不向后兼容，只能在已演练的恢复流程中使用发布前备份。

## 11. 常见故障

- `gateway` 不 healthy：先看 migration 是否成功、`ENCRYPTION_KEY` 是否为 32 字节 Base64、上游 URL 是否 HTTPS。
- `admin-web` 不 healthy：检查 `GATEWAY_API_URL=http://gateway:8080`、Gateway `/ready` 和容器日志。
- HTTP 能打开但登录不保持：这是 Secure Cookie 的预期行为，必须使用 HTTPS。
- Server Action 报 Origin 不匹配：确认 Nginx 传递了 `Host` 和 `X-Forwarded-Host`，不要先扩大 `ALLOWED_ORIGINS`。
- IP 证书无法签发：确认 Certbot >= 5.4、Elastic IP 未变、安全组/NACL 允许 80，且
  `http://PUBLIC_IP/.well-known/acme-challenge/...` 可从公网访问。
