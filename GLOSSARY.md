# NAS Files

A lightweight web file manager on a Synology NAS that lets people on the home network or the tailnet browse, download, upload and delete files in designated NAS folders.

## Language

### Files

**Root**:
The virtual top level whose entries are exactly the Mounts; it is read-only and is the only part of the NAS the service can see.
_Avoid_: Virtual root, share, volume, base dir

**Mount**:
One NAS shared folder or sub-folder exposed as a folder at the top of the Root; its name is the first segment of every Path inside it. A read-only Mount refuses every change.
_Avoid_: Mounted folder, share, volume

**Path**:
A location written relative to the Root, starting with a Mount (e.g. `documents/tax/2025.pdf`); never an absolute NAS path.
_Avoid_: Full path, NAS path

**Trash**:
A service-owned place in each writable Mount that holds deleted and replaced content until it is purged.
_Avoid_: Recycle bin, `#recycle` (that is Synology's own, separate bin)

**Hidden Entry**:
Anything in a Mount that belongs to the NAS or to a service rather than to Users, such as the Trash and DSM's system folders; never shown and never a valid Path.
_Avoid_: System file, ignored file

### Transfers

**Upload**:
The transfer of one file to one Path, which may span many requests and survive dropped connections before it completes.
_Avoid_: Transfer, job

**Folder Upload**:
A set of Uploads that together rebuild a local folder tree under a chosen Path.
_Avoid_: Batch, sync, archive upload

**Conflict**:
An Upload whose Path already holds a file; the User decides whether to skip it, keep both, or replace the existing file.
_Avoid_: Collision, duplicate

**Staging Area**:
A Hidden Entry in each writable Mount where an Upload's data waits until it completes.
_Avoid_: Temp dir, cache, incoming

### People

**User**:
A named person who signs in to the service; every change is attributed to one.
_Avoid_: Account, client, Caller

**Access**:
What a User may do in one Mount: none, read, or write. A read-only Mount limits every User to read at most.
_Avoid_: Role, permission, Scope
