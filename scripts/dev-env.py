#!/usr/bin/env python3
"""Create local-only development secrets; never overwrite existing configuration."""
import base64
import pathlib
import secrets
p = pathlib.Path('.env')
if p.exists() and any('=' in line and not line.lstrip().startswith('#') for line in p.read_text().splitlines()):
    raise SystemExit('.env exists; left unchanged')
password = secrets.token_urlsafe(24)
text = pathlib.Path('.env.example').read_text()
text = text.replace('POSTGRES_PASSWORD=CHANGE_ME', 'POSTGRES_PASSWORD=' + password)
text = text.replace('kovar:CHANGE_ME@', 'kovar:' + password + '@')
text = text.replace('ENCRYPTION_KEY=CHANGE_ME', 'ENCRYPTION_KEY=' + base64.b64encode(secrets.token_bytes(32)).decode())
text = text.replace('GATEWAY_ADMIN_PASSWORD=CHANGE_ME', 'GATEWAY_ADMIN_PASSWORD=' + 'Aa123456')
with p.open('a') as f:
    p.chmod(0o600)
    f.write('\n' + text)
print('Created local .env (0600); development admin credentials are documented in README.')
