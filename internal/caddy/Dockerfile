FROM caddy:2.11.4-builder AS builder
RUN xcaddy build v2.11.4 \
    --with github.com/lucaslorentz/caddy-docker-proxy/v2@v2.13.1 \
    --with github.com/caddy-dns/hetzner/v2@v2.0.0

FROM caddy:2.11.4
COPY --from=builder /usr/bin/caddy /usr/bin/caddy
CMD ["caddy", "docker-proxy"]
