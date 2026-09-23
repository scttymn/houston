# shellcheck shell=bash
# Sourced by the tunnel stages. A new record on the base domain doesn't win
# over the wildcard (the other server) at every Cloudflare edge at once: until
# it does, some answers are the other server's. settle waits, up to 5 minutes,
# for 10 answers in a row with the wanted code. Callers define ok and bad.
settle() { # want url
  local want="$1" url="$2" start=$SECONDS streak=0
  while [ "$streak" -lt 10 ] && [ $((SECONDS - start)) -lt 300 ]; do
    if [ "$(curl -s -o /dev/null -w '%{http_code}' --max-time 10 "$url")" = "$want" ]; then streak=$((streak + 1)); else streak=0; fi
    sleep 1
  done
  if [ "$streak" = 10 ]; then ok "$url: 10 ${want}s in a row through Cloudflare (after $((SECONDS - start))s)"; else bad "$url never settled on $want through Cloudflare in 5 minutes"; fi
}
