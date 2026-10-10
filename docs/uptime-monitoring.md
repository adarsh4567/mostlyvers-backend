# UptimeRobot monitoring

MOSTLYVERS exposes a public, database-independent monitor endpoint:

```text
https://mostlyvers-api.onrender.com/health/uptime
```

It returns HTTP `200`, `Cache-Control: no-store`, and the stable plain-text
keyword `MOSTLYVERS_UP`. It does not query PostgreSQL, object storage, email, or
other paid/quota-limited services.

Create the UptimeRobot monitor with these values:

- Monitor type: `Keyword`
- Friendly name: `MOSTLYVERS API`
- URL: `https://mostlyvers-api.onrender.com/health/uptime`
- Keyword: `MOSTLYVERS_UP`
- Alert when: keyword does not exist
- HTTP method: `GET`
- Monitoring interval: `5 minutes` (the free-plan minimum)
- Request timeout: the largest free-plan value available
- Follow redirects: enabled
- SSL expiry notification: enabled

Attach the Owner's email alert contact. After saving, use **Test notification**
and confirm the monitor changes to `Up`.

Do not monitor `/health/ready` for keep-alive traffic. That route intentionally
queries PostgreSQL and is reserved for deployment readiness. Render's own
`healthCheckPath` remains `/health/ready`.

Render currently spins down a free web service after 15 minutes without inbound
traffic. A five-minute external check prevents idle spin-down while the monitor
is running. Keeping one instance active continuously consumes nearly the full
750 free instance-hour monthly allowance, so do not keep additional free web
services awake from the same Render workspace.

Verify manually:

```bash
curl --fail --show-error \
  https://mostlyvers-api.onrender.com/health/uptime
```

Expected response:

```text
MOSTLYVERS_UP
```
