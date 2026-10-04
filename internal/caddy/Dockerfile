FROM caddy:2.10.2-builder AS builder
RUN xcaddy build v2.10.2 \
    --with github.com/lucaslorentz/caddy-docker-proxy/v2@v2.10.0 \
    --with github.com/caddy-dns/hetzner/v2@v2.0.0-preview-3

FROM caddy:2.10.2
COPY --from=builder /usr/bin/caddy /usr/bin/caddy
CMD ["caddy", "docker-proxy"]
