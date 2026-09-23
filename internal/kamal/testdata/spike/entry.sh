#!/bin/sh
# Serves what this container received: each variable's exact value, and
# whether $DB_HOST resolves.
mkdir -p /www/env
for name in PLAIN ERB DOLLAR DB_HOST HOSTILE DATABASE_URL KAMAL_VERSION; do
  printenv "$name" > "/www/env/$name"
done
nslookup "$DB_HOST" > /www/env/lookup 2>&1 || echo "lookup failed" >> /www/env/lookup
echo ok > /www/up
exec httpd -f -p 80 -h /www
