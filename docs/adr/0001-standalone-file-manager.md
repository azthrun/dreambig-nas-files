# A standalone file manager, separate from dreambig-nas-file-api

This is a new service rather than an extension of `dreambig-nas-file-api`, because people need to download and delete files, and that API's ADR-0001 deliberately forbids both so a leaked Credential can neither read nor destroy data. Its Root model, Trash and Hidden Entries follow `dreambig-nas-mcp` (ADR-0001, ADR-0003); its Uploads follow file-api's ADR-0003, except that a Conflict asks the User because a person is present. Code from the siblings is copied, not shared as a dependency.

## Considered Options

- **Extend file-api and drop its ADR-0001**: rejected; it gives up a guarantee that still matters for its scripted Callers.
- **A UI here calling file-api plus a second API**: rejected; two services for what one process can do.
