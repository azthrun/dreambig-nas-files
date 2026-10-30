# Local Users in a file, no Keycloak or OIDC

Users are stored in an htpasswd-format file with bcrypt hashes, mounted read-only and edited in File Station; each User has an Access per Mount, defaulting to none. Entries can be generated with `htpasswd` on a Mac, so no Docker, shell on the NAS or database is needed, and a leaked config does not expose reused passwords. Sign-in sits behind one seam so an external identity provider can replace it later.

## Considered Options

- **Keycloak**: rejected; a JVM service of roughly 1 GB RAM plus a database on a 2 GB NAS, it expects HTTPS (see ADR-0002), OIDC's single issuer URL fits poorly with separate LAN and tailnet addresses, and it pays off only when many apps share sign-in. An outage would also lock everyone out of their files.
- **DSM SSO Server or Authelia**: deferred; the lighter first choices if shared sign-in is ever needed, once HTTPS exists.
- **Plaintext passwords in `.env`**: rejected; unlike generated tokens, passwords get reused.
- **Admin page with SQLite**: rejected; a database and extra UI for a few Users.
- **One shared password, or no login**: rejected; any LAN device could delete files, and audit lines could not name a User.
