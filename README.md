# Wildcard Tunnel Manager

**English** · [繁體中文](README.zh-TW.md)

Manage `name.example.com/path → localhost:port` routes from a local web UI on macOS. Each host runs its own Cloudflare Tunnel. A proxied wildcard CNAME (`*.example.com`) can point to one host's tunnel at a time; explicit DNS records take precedence over the wildcard.

The domain is set with `DDMAN_BASE_DOMAIN` in `.env`. `example.com` is used only as an example below. The `DDMAN_` setting names and socket directory name remain for compatibility with earlier versions; they do not restrict the domain to `ddman.cc`.

## Quick start

You need macOS, Go 1.26+, `cloudflared`, a domain in Cloudflare, and permission to manage its tunnel and DNS records. Install [`cloudflared`](https://developers.cloudflare.com/tunnel/get-started/) (for example, `brew install cloudflared`), then:

```bash
git clone https://github.com/ddman/wildcard-tunnel-manager.git
cd wildcard-tunnel-manager
cp .env.example .env
chmod 600 .env
# Edit .env: set DDMAN_BASE_DOMAIN, DDMAN_ADMIN_HOST, and a unique password of at least 16 characters.
go run .
```

Open `http://127.0.0.1:8787`, sign in, and add a route such as `app → http → 3000`. To publish `app.example.com`, create a scoped API Token as described under [Cloudflare setup](#cloudflare-setup), enter the Account ID, Zone ID, and token in the UI, and select **Set up and connect**. This creates or reuses a tunnel and creates the `*.example.com` DNS record. If that wildcard already points to another tunnel, switching it requires the explicit force bind action.

## How it works

```text
Browser → Cloudflare DNS / Universal SSL → Cloudflare Tunnel
        → cloudflared → Unix socket → Go reverse proxy
        → 127.0.0.1:<service port>
```

The admin UI listens at `http://127.0.0.1:<DDMAN_ADMIN_PORT>` (8787 in the example). Set `DDMAN_ADMIN_TAILSCALE_IP` to also listen on that device's Tailscale IPv4 address. Browser login uses a time limited session cookie; the CLI can use HTTP Basic authentication. The admin UI and API are never published through the tunnel. Only allowed tailnet devices should reach the optional Tailscale listener.

The proxy listens on `/tmp/ddman-home-tunnel-<uid>/<config-hash>.sock`, not a TCP port. The UI shows the exact path. Each config file gets its own socket. An older tunnel still configured for TCP may temporarily use `127.0.0.1:8788`; that listener closes after the tunnel is migrated to the socket. The Go process provides the UI, configuration, reverse proxy for HTTP/HTTPS origins, and Cloudflare API integration. `cloudflared` is its only additional runtime dependency; there is no frontend build, Caddy, or Docker requirement.

## Configuration and routing

Create `.env` from `.env.example` before the first run. Set `DDMAN_BASE_DOMAIN` to the Cloudflare zone you want to use, and set `DDMAN_ADMIN_HOST` to an unused single label under that domain (for example, `admin.example.com`). Also set `DDMAN_ADMIN_PORT`, `DDMAN_ADMIN_USERNAME`, and a unique `DDMAN_ADMIN_PASSWORD` of at least 16 characters. The host name reserves that label against public routes; it does not publish the admin UI. The file contains a plaintext password, must be owned by the current user with mode `0600`, and is ignored by Git. Account ID, Zone ID, and API Token can be entered in the UI or placed in `.env` as `CF_ACCOUNT_ID`, `CF_ZONE_ID`, and `CF_API_TOKEN` for binding checks at startup.

You can change the admin username and password from the **Admin credentials** page after entering the current password. Changes take effect immediately and are saved to `.env`. If you forget the password, edit `.env` on the host and restart the program; there is no unauthenticated remote reset.

After adding a route, test the local proxy using the socket path displayed in the UI:

```bash
curl --unix-socket '<socket path shown in the UI>' http://app.example.com/
```

A hostname can have multiple path routes. For example, `app.example.com/` may go to `http://127.0.0.1:3000` while `app.example.com/api` goes to `http://127.0.0.1:4000`. The longest path prefix wins, and matches only at segment boundaries: `/api/users` matches `/api`, but `/apix` does not. The prefix is kept by default, so the origin sees `/api/users`; with **Strip prefix**, it sees `/users`. Older routes without a `path` still represent `/`. Applications using root-relative assets or redirects may need their own base path configured when stripping prefixes.

### Cloudflare setup

In Cloudflare Dashboard, create a scoped API Token with **Account → Cloudflare Tunnel → Edit** and **Zone → DNS → Edit** for the chosen domain. DNS Settings Edit is a different permission and cannot manage DNS records. Copy the Account ID and Zone ID from Dashboard. You may supply the ID of an empty remotely managed tunnel; the tool verifies that it has no existing ingress before reusing it. Otherwise, it creates a dedicated tunnel.

The tool configures `*.example.com` ingress to the local Unix socket, creates a proxied wildcard CNAME, and starts `cloudflared`. It does not create admin ingress or an admin DNS record. A normal bind reports a conflict if the wildcard points to another tunnel. Only **Force bind** changes that record to this host's tunnel. You do not need to run the Cloudflare Dashboard installation command, which contains a tunnel token.

If upgrading a tunnel that used the old `8788` TCP listener, enter the API Token again in the UI and select **Set up and connect**. This updates the existing tunnel's origin address while retaining its tunnel and DNS record. The CLI can perform the same migration while the admin process is running. It reads stored Account ID, Zone ID, and Tunnel ID from the local admin API; the API Token comes through standard input, not a command argument:

```bash
read -s "CF_API_TOKEN?Cloudflare API Token: "
printf '%s' "$CF_API_TOKEN" | go run . tunnel use-socket --token-stdin
unset CF_API_TOKEN
```

`config.json` contains the tunnel token and uses mode `0600`. `.env` stores the API Token, Account ID, and Zone ID for periodic binding checks and also uses mode `0600`. Both files are ignored by Git. Never share them. A tunnel token can connect to the tunnel; it cannot query or edit published hostnames. On restart, the program uses the stored tunnel token to start `cloudflared` again.

## Multiple hosts and limitations

- Each host has its own tunnel and `config.json`. The wildcard CNAME points to one tunnel at a time. A force bind switches DNS but does not stop the old host remotely. Explicit DNS records are unaffected.
- The UI checks the wildcard DNS every 30 seconds. A normal bind does not overwrite a conflict; **Force bind** switches the CNAME and verifies it. **Unbind** removes the wildcard record only if it still points to this host's tunnel, and does not delete the tunnel. Existing connections may briefly remain on the previous host.
- Only one level of subdomain is routed: `app.example.com`, not `app.home.example.com`. The latter also needs separate HTTPS certificate coverage.
- Path routes use fixed prefixes, without regular expressions, upstream redirect rewriting, or response body rewriting.
- Public routes have no built in access control. Do not publish an unauthenticated admin UI or private service.
- Origins must listen on loopback. HTTPS origins need a certificate Go can verify. Browser to Cloudflare HTTPS and cloudflared to the local HTTP socket are separate connections. `cloudflared` must be able to access the socket; this tool starts it as the same user.
- Both this process and `cloudflared` must keep running. Services are unavailable while the Mac sleeps or the process stops. No macOS background service is installed.
- If setup fails partway through, retrying **Set up and connect** reuses the saved tunnel. Delete a tunnel or DNS record manually in Cloudflare Dashboard if needed.
- To simulate two instances on one computer, use separate config directories and different `DDMAN_ADMIN_PORT` values. Automated tests mock the Cloudflare API.

## References

- [Cloudflare Tunnel setup](https://developers.cloudflare.com/tunnel/get-started/)
- [Wildcard DNS priority](https://developers.cloudflare.com/dns/manage-dns-records/reference/wildcard-dns-records/)
- [Universal SSL coverage](https://developers.cloudflare.com/ssl/edge-certificates/universal-ssl/)
- [Tunnel run parameters](https://developers.cloudflare.com/tunnel/reference/run-parameters/)
