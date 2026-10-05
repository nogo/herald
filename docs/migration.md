# Migration

## 3.x to 4.0

Herald 4.0 stops guessing how to route a stack and refuses configs that would leave a stack without a route. A 3.x config that relied on the guesses needs one or two fields added. Nothing else in `config.yml`, the secrets store or the data dir changes.

### What changed

**Routing is explicit.** 3.x preferred a service named `app` or named after the stack, took the alphabetically first service when none matched, fell back to `app` when the compose file did not parse, and assumed port 3000 (repo stacks) or 80 (path stacks) when the service declared none. 4.0 routes to:

- the only service in the compose file, or the one named in `service:`
- the one container port the service declares in `expose`/`ports`, or the one in `port:`

Anything else fails with a message that names the field to set. See [Routing](config.md#routing-service-and-port).

**A config that leaves a path stack without a route is refused as a whole.** The daemon keeps the previous config and deploys nothing until it is fixed. In bare-repo mode the push itself is rejected.

**A GitHub token needs `server.deploy_domain`,** also when the token comes from `herald auth login` rather than the config. GitHub webhooks need a public Herald site to reach.

### Before you upgrade

For each stack, open its compose file:

1. More than one service? Add `service:` with the one Caddy should route to.
2. Does that service declare no container port in `expose` or `ports`, or more than one? Add `port:`. The 3.x default was 3000 for repo stacks and 80 for path stacks.
3. Range ports (`8000-8010`) and `${VAR}` ports are not read. Set `port:` for those.

If the server uses a GitHub token, check that `server.deploy_domain` is set.

Push the changed `config.yml` before upgrading. 3.x does not know `service:` and `port:` on a stack and ignores them, so the push is safe.

### Upgrade

```sh
curl -fsSL https://raw.githubusercontent.com/nogo/herald/main/scripts/install.sh | sudo sh
sudo -iu herald herald sync
```

The installer restarts a running daemon on the new binary.

`herald sync` runs the same pass the daemon runs. A path stack without a route shows up as a config error naming the stack and the field.

### If something still fails

- **The daemon does not start** and the journal says `server.deploy_domain is required`: add `deploy_domain` to `server:`, push, then `sudo systemctl restart herald`.
- **A repo stack's deploy fails** with "set `service:`" or "set `port:`": add the field and push. The failed deploy changed nothing; the running containers keep running.
- **The server repo's config is refused:** `herald sync` and `sudo journalctl -u herald` name the stack. Fix it and push; the daemon applies the next valid config.

`herald` on its own shows where setup stands; `herald doctor` checks the rest.
