# Plain HTTP on one host port, for the LAN and the tailnet

The service publishes one host port over plain HTTP and is reached directly on the LAN or through the Tailscale DSM package. The NAS firewall allows that port only from the LAN and `100.64.0.0/10`, and no router port is forwarded. Both LAN devices without Tailscale and tailnet devices must reach it, which file-api's no-host-port sidecar (its ADR-0002) cannot do alone. We accept that passwords and files cross the home LAN unencrypted, because the LAN is trusted and a real certificate would need a domain and DNS challenge. Traffic over Tailscale is encrypted by WireGuard.

## Considered Options

- **Host port plus a Tailscale sidecar for `*.ts.net` HTTPS**: rejected; the LAN stays plain HTTP anyway, for a second address and a second container.
- **DSM reverse proxy with HTTPS**: rejected for now; needs a real certificate, or every device sees a warning. Revisit if the LAN stops being trusted.

## Consequences

- Nothing may depend on a secure context: no `crypto.subtle`, service workers or File System Access API, and session cookies cannot be `Secure`.
- The tailnet policy must let Users' devices reach this port, not just the Mac.
