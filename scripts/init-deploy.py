#!/usr/bin/env python3
"""Create a private first-start configuration without replacing existing files."""
import argparse
from pathlib import Path
import os
import secrets
from urllib.parse import urlsplit

parser = argparse.ArgumentParser()
parser.add_argument('--public-url', required=True)
parser.add_argument('--admin-email', required=True)
args = parser.parse_args()
url = urlsplit(args.public_url)
if url.scheme != 'https' or not url.hostname or url.username or url.password or url.path not in ('', '/') or url.query or url.fragment:
    parser.error('public-url must be an HTTPS origin')
if any(c in args.admin_email for c in '\r\n#=') or '@' not in args.admin_email:
    parser.error('admin-email must be a single email address')
root = Path(__file__).resolve().parents[1]
config, secret_dir = root / '.env', root / '.secrets'
password_file = secret_dir / 'bootstrap-password'
if config.exists() or secret_dir.exists():
    parser.error('configuration or secret directory already exists; no files changed')
secret_dir.mkdir(mode=0o700)
password_file.write_text(secrets.token_urlsafe(32))
# Compose's local secrets are bind mounts: mode in compose.yaml is not applied.
# A readable file in an owner-only directory is readable by container uid10001,
# while other host users cannot traverse the parent directory.
password_file.chmod(0o444)
body = (root / '.env.example').read_text().replace('https://substrate.company.example', args.public_url.rstrip('/')).replace('admin@company.example', args.admin_email).replace('replace-before-starting', secrets.token_hex(32))
fd = os.open(config, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
with os.fdopen(fd, 'w') as out:
    out.write(body)
print('Created .env (0600) and .secrets/ (0700). Bootstrap password is only in .secrets/bootstrap-password.')
