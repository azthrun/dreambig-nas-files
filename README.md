# NAS Files

A small web file manager for a Synology NAS. People on the home network or the tailnet sign in, browse designated NAS folders, and download, upload and delete files. One Go container, plain HTTP on one host port (see `docs/adr/`, `GLOSSARY.md` for the terms used here).

All names, addresses and paths below in `<angle brackets>` are placeholders. Replace them with your own and never commit real ones: this repository is public.

Steps below were written for DSM 7.2 with Container Manager. Menu names can differ slightly on other versions.

## What you need

- A Synology NAS with Container Manager installed (Intel, `linux/amd64`).
- A Mac (or any machine with `htpasswd`) to generate password hashes.
- Optional: the Tailscale package on the NAS, for access from the tailnet.

## 1. Create the DSM user

The container runs as a dedicated DSM user, so it can only touch the folders you give that user.

1. DSM: **Control Panel > User & Group > User** tab > **Create**.
2. Name: `nas-files`. Set a long random password (nobody signs in with it). Finish the wizard:
   - Groups: leave `users`.
   - Shared folder permissions: leave everything on **No access** for now (step 3 sets them).
   - Application permissions: set every application to **Deny**.
3. Find its numeric IDs. Enable SSH briefly: **Control Panel > Terminal & SNMP > Terminal** tab > tick **Enable SSH service** > **Apply**. From the Mac run `ssh <admin-user>@<nas-address> id nas-files`. The output looks like `uid=<UID>(nas-files) gid=<GID>(users) ...`. Write down `<UID>` and `<GID>`. Then untick **Enable SSH service** and **Apply** again if you do not normally use it.

## 2. Pick the shared folders (Mounts)

Each shared folder (or sub-folder) you expose becomes a **Mount**: a folder at the top of the service, named after the last part of its container path. The service sees only what you list.

- Choose names without spaces if possible, such as `documents`.
- Each Mount must be its own bind mount in `compose.yaml` (step 6). Do not mount a sub-folder of another Mount.

## 3. Give `nas-files` folder permissions

For each shared folder:

1. **Control Panel > Shared Folder** > select the folder > **Edit** > **Permissions** tab.
2. In the drop-down at the top left choose **Local users**.
3. In the `nas-files` row tick **Read/Write** for a Mount Users may change, or only **Read only** for one that is read-only. Click **Save**.

If you use a sub-folder, the same permission on the parent shared folder applies (DSM permissions are set per shared folder).

Also create a small shared folder for the config files, for example `<config>`, and give `nas-files` **Read only** on it. Create a sub-folder `nas-files` inside it with File Station.

## 4. Create the users file and Access file

The **users file** holds one `name:bcrypt-hash` line per User. The **Access file** says what each User may do in each Mount.

### Users file (on the Mac)

1. Open Terminal on the Mac.
2. For each User run (it asks for the password twice and prints one line):

   ```sh
   htpasswd -nBC 10 <user>
   ```

   Output looks like `<user>:$2y$10$...`.
3. Paste the lines, one per User, into a plain text file named `users.htpasswd`. Lines starting with `#` and blank lines are ignored. Hashes must be bcrypt (`-B`); other formats make the service refuse to start.

### Access file

Create `access.json`:

```json
{
  "<user>": { "<mount>": "write", "<archive-mount>": "read" },
  "<other-user>": { "<mount>": "read" }
}
```

- Levels are `"none"`, `"read"` and `"write"`. Anything not listed is `none`, and a Mount a User has no Access to does not exist for them (it is absent from their Root, and its Paths answer not found).
- A Mount mounted `:ro` caps everyone at `read`.
- Every `<user>` must be in the users file and every `<mount>` must be a Mount in `compose.yaml`, or the service refuses to start with a message naming the problem.

### Put both on the NAS

1. DSM: **File Station** > `<config>` > `nas-files` folder > **Upload > Upload - Skip** (or drag and drop). Upload `users.htpasswd` and `access.json`.
2. Right-click each file > **Properties** > **Permission** tab: check that `nas-files` can read it (inherited from the folder is fine).

To add or change a User later: edit the file on the Mac, upload it again over the old one in File Station, then restart the Project (step 6; the files are read only at start).

## 5. Create the project files

Create a folder for the project, for example `<config>/nas-files-project`, and put two files in it:

1. `compose.yaml`: copy it from this repository.
2. `.env`: copy `.env.example` from this repository to `.env` and edit it: set `NAS_FILES_UID` and `NAS_FILES_GID` to the numbers from step 1, and `NAS_FILES_HOST_PORT` to the port you want.

Edit the `volumes:` section of `compose.yaml`: one line per Mount.

```yaml
volumes:
  - /volume1/<share>:/data/<mount>
  - /volume1/<archive>:/data/<archive-mount>:ro
  - /volume1/<config>/nas-files/users.htpasswd:/config/users.htpasswd:ro
  - /volume1/<config>/nas-files/access.json:/config/access.json:ro
```

- Left of the first colon is the NAS path (`/volume1/` plus the shared folder name; it can be a sub-folder). Right of it is `/data/<mount>`, where `<mount>` is the name Users see.
- Add `:ro` at the end to make a Mount read-only for everyone. It then has no Trash and no Staging Area, and every change is refused.
- `image:` pins a release tag (`v1.0.0`). Keep it pinned; do not use `latest` on the NAS.

## 6. Start it in Container Manager

1. DSM: **Container Manager > Project > Create**.
2. **Project name**: `dreambig-nas-files`. **Path**: **Set path** and choose the project folder from step 5 (it must contain `compose.yaml` and `.env`). **Source**: **Use existing docker-compose.yml**. **Next**.
3. Skip the web portal settings (**Next**), then **Done**. The first start pulls the image from GHCR, which needs internet access.
4. Expect: the Project shows **Running** and the container turns **healthy** within about a minute. If it exits, open the Project > **Log**. The service refuses to start, with a clear message, when: there are no Mounts, a Mount is not its own mount point, the users file or Access file is missing or malformed, or the Access file names an unknown User or Mount.
5. Open `http://<nas-address>:<port>/` in a browser on the LAN and sign in.

To restart after editing config files: **Container Manager > Project** > select the project > **Action > Stop**, then **Action > Start**.

## 7. NAS firewall

The service is plain HTTP (ADR-0002). Only the LAN and the tailnet may reach its port, and no router port is forwarded.

1. **Control Panel > Security > Firewall** tab > tick **Enable firewall** > **Apply**.
2. **Edit Rules** (profile `default`) > **Create**. Make these rules, in this order, each with **Ports: Custom > Type: Destination port(s) > Port: `<port>`**, protocol TCP:
   1. **Source IP: Specific IP > Subnet**, IP `<lan-subnet-address>`, mask `<lan-subnet-mask>` (for example your router's LAN range). Action: **Allow**.
   2. **Source IP: Specific IP > Subnet**, IP `100.64.0.0`, mask `255.192.0.0` (this is `100.64.0.0/10`, the tailnet range). Action: **Allow**.
   3. **Source IP: All**. Action: **Deny**.
3. Keep your existing rules for DSM itself above or below as they are; these three only cover this port. **OK**, then **Apply**.
4. Verify that the rules really filter the port. A phone on mobile data is **not** a valid test: behind your router's NAT the NAS is unreachable from the internet anyway, so it passes even with no firewall. Test with a source that can reach the NAS but is in neither allowed range:
   1. Temporarily edit rule 1 (the LAN allow) so its subnet is a range your test device is **not** in, for example one address `<other-lan-address>` that is not your test device, and **Apply**.
   2. From your test device on the LAN (no Tailscale), open `http://<nas-address>:<port>/`. It must **not** connect. If the page loads, the firewall is not filtering the port: go to the fallback below.
   3. Restore rule 1 to `<lan-subnet-address>` / `<lan-subnet-mask>`, **Apply**, and check that the page loads again from the LAN and from a Tailscale device.

DSM's firewall may not filter the ports that Container Manager publishes (Docker adds its own forwarding rules ahead of DSM's). This repository cannot confirm it for your DSM version, so step 4 is required, not optional. If the port is reachable when it should not be, do not rely on these rules: put the same three rules (allow LAN, allow `100.64.0.0/10`, deny all) in the router or in front of the NAS, or publish the port on a single interface address (`"<nas-lan-address>:<port>:8080"` in `compose.yaml`) so it is not reachable from other networks.

### Client address and the sign-in limit

The sign-in rate limit counts failures per User name and per source address. Through Docker's normal port forwarding (DNAT) the real client address is kept. If a userland proxy ends the connection instead, every client shows up as the Docker gateway address, so one device's failed sign-ins would lock everybody out. The address is not written to the audit log, so check by behaviour:

1. On device A (LAN), try to sign in 5 times in a row with a wrong password (any name).
2. On device B (a different LAN device or a Tailscale device), sign in with a correct name and password.
3. Expect: B signs in. If B is refused as well, all clients share one address: tell the owner before giving the service to more Users. The lock on A lasts 15 minutes.

## 8. Tailnet policy

Tailscale on the NAS is a separate step (install the package, sign in, note the NAS's tailnet name `<nas-tailnet-name>`). Users' devices must then be allowed to reach this port, not just your Mac.

1. Tailscale admin console > **Access controls**.
2. Add a rule that allows the Users' devices to reach the NAS on this port only, for example in the policy file's `acls` list:

   ```json
   { "action": "accept", "src": ["<users-group-or-tag>"], "dst": ["<nas-tailnet-name>:<port>"] }
   ```

3. **Save**. Do not use `*:*` or a wide port range. Users then open `http://<nas-tailnet-name>:<port>/` while Tailscale is connected.

## Configuration

Set in `.env` next to `compose.yaml`, except the five that `compose.yaml` fixes itself (`NAS_FILES_ADDR`, `NAS_FILES_ROOT`, `NAS_FILES_REQUIRE_MOUNT_POINTS`, `NAS_FILES_USERS_FILE`, `NAS_FILES_ACCESS_FILE`): changing those in `.env` has no effect unless you edit `compose.yaml`. `.env.example` documents each one; a test keeps the two files and this table in step.

| Variable | Default | Meaning |
| --- | --- | --- |
| `NAS_FILES_UID` | none (required) | DSM user id the container runs as. Compose only. |
| `NAS_FILES_GID` | none (required) | DSM group id the container runs as. Compose only. |
| `NAS_FILES_HOST_PORT` | `8080` | The one published host port. Compose only. |
| `NAS_FILES_ADDR` | `0.0.0.0:8080` | Listen address inside the container. Leave as is. |
| `NAS_FILES_ROOT` | `/data` | Folder inside the container whose sub-folders are the Mounts. Leave as is. |
| `NAS_FILES_REQUIRE_MOUNT_POINTS` | `true` | Refuse a Mount that is not its own mount point. `false` only for running without Docker. |
| `NAS_FILES_USERS_FILE` | `/config/users.htpasswd` | Users file inside the container. |
| `NAS_FILES_ACCESS_FILE` | `/config/access.json` | Access file inside the container. |
| `NAS_FILES_TRASH_RETENTION_DAYS` | `30` | Days deleted and replaced files stay in the Trash. `0` keeps them forever. |
| `NAS_FILES_UPLOAD_EXPIRY_DAYS` | `7` | Days an idle, unfinished Upload is kept before its partial data is removed. `0` keeps them forever. |
| `NAS_FILES_FREE_SPACE_RESERVE_GB` | `5` | Free space (decimal GB) a Mount's volume must keep after a new Upload's declared size. `0` turns the check off. |

## Using it

- Folder Upload recreates a local folder's files and sub-folders, but **empty folders are not created**. Make an empty folder with the "new folder" form.
- Sessions live in memory: **restarting the container signs everyone out**.
- Downloading several selected items streams a zip.

### Trash and recovering files

Delete and replace never destroy data: the item moves to `<Mount>/.nas-files-trash/<YYYY-MM-DD>/<original path>` and stays for `NAS_FILES_TRASH_RETENTION_DAYS` days. The Trash is not shown in the web app. To recover something:

1. DSM: **File Station** > the shared folder behind the Mount > `.nas-files-trash`. If you do not see it, turn on **File Station > Settings > General > Show hidden files and folders** (or similarly named option on your DSM version).
2. Open the dated folder, find the item, then drag it back (or right-click > **Move to / Copy to**).

Synology's own `#recycle` is separate and unrelated. A `:ro` Mount has no Trash.

## Upgrade and rollback

Releases are published to `ghcr.io/azthrun/dreambig-nas-files` when a `v*` tag is pushed and CI (tests, secret scan, smoke test) has passed. Each carries `:<version>` and `:sha-<short>`; stable releases also move `:latest` (the compose file never uses it).

Upgrade:

1. No backup of the service is needed: all data lives in your shared folders; the container keeps only in-memory sessions and each Mount's `.nas-files-staging` folder of unfinished Uploads.
2. Edit `compose.yaml` in the project folder (File Station, or the Container Manager Project's YAML editor) and change the tag after `image: ghcr.io/azthrun/dreambig-nas-files:` to the new version.
3. **Container Manager > Project** > select the project > **Action > Stop**, then **Action > Build** (this pulls the new image and recreates the container), then **Action > Start** if it is not running.
4. Expect the container healthy again in under a minute; everyone must sign in again.

Rollback: set the old tag back in `compose.yaml` and repeat step 3. Nothing on disk is migrated by a release, so rolling back is safe.

The exact label of the Build action can differ by Container Manager version; this has not been checked on a real NAS.

## Development

```sh
go vet ./... && go test ./... && CGO_ENABLED=0 go build -o /dev/null ./cmd/...
```

To run natively without Docker, point `NAS_FILES_ROOT`, `NAS_FILES_USERS_FILE` and `NAS_FILES_ACCESS_FILE` at temp folders and set `NAS_FILES_REQUIRE_MOUNT_POINTS=false`. `scripts/smoke-test.sh <image>` checks a built image (needs Docker, curl); it drives `scripts/smoke-flow.sh`, which can also be pointed at a native run.
