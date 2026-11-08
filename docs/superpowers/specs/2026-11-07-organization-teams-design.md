# Organization teams and project authority

This increment implements the approved enterprise organization/project permissions requirement. The current instance is one enterprise; this is not a multi-enterprise isolation claim. Enterprise administrators manage flat teams of existing identities; project owners explicitly bind a team's human roles to their project. Nested groups and directory/SCIM synchronization remain separate acceptance.

## Authority and lifecycle

Human enterprise administrators may create, revise, disable and explicitly enable a team. Names are1–160 bytes, descriptions at most4000 bytes, change reasons1–2048 bytes. A team has at most1000 unique, registered, enabled member identities; an empty team is valid. Disabled identities retain history but receive no inherited access. A disabled team grants no access. A change always requires its exact expected revision and a stable request key. Exact replays authenticate current authority and return the current resource without repeating mutation; conflicting reuse is rejected. Immutable team revisions and transactional audits preserve membership history.

Project human owners grant/revise/revoke one binding per team/project. Binding fixes explicit roles and the team revision reviewed when granting; subsequent administrator membership changes apply dynamically and are disclosed in the UI. Grant/regrant requires the current enabled team revision. Binding edits/revocation require their exact current revision and reason/key. Direct and inherited roles form a union; removing a direct grant does not remove team access. Teams never receive platform-admin authority. Existing project-independent Agent/Worker and human approval/quality rules remain.

Every project continues to require an enabled, registered, directly assigned owner. Team owners do not replace this recovery anchor. Existing last-direct-owner checks apply to direct role changes/removal and global identity disable. Global disable also stops team access and revokes all credentials without rewriting historical team membership.

## Transactional enforcement

One SQL transaction changes team/binding state, stores immutable history/replay keys, writes audits and revokes all affected issuers' Agent/Worker tokens in the affected project. A member added/removed from an active bound team loses their old delegated project credentials even when another grant still supplies a role. Binding role/status changes revoke that team's members' delegation in this project. Unrelated projects, members and human sessions remain. Name/description-only changes and exact replays do not revoke tokens. Existing cached HTTP actors and concurrent issuance must pass transaction-local current identity, group and grant checks.

Authorization derives inherited roles from active team/binding records and enabled identity state inside the same governed transaction. Project/Workspace/Git/model and command paths share it. Project workspace projects an actor's effective roles and each bound team's name/current revision/enabled member count; these are read views, never persisted authority. Organization catalog is administrator-only, or metadata-only for owners selecting a team for their own project; it does not disclose other project bindings or global member lists. Raw global team/history resources are administrator-only. Project audits record permission changes, including global offboarding of inherited-only members, so project clients refresh. Human message recipients use the same derived roles.

## API and workbench

Versioned commands: teams.create/update/disable/enable/grant/revoke. GET /api/v1/teams returns an administrator catalog; projectId selects an owner-authorized catalog. Global detail/history use existing resource routes with explicit administrator guards. Worker/Agent callers cannot mutate organization membership or team grants, including through MCP. Regular project users can view their project's active/revoked bindings and effective access sources.

The governance workspace adds organization teams and project bindings with revision-aware forms, explicit role selection, source display, disable/revoke reasons, retry/loading/errors and keyboard/focus/mobile support. Direct membership removal explains any retained inherited access. No IdP synchronization, external messages, production deployment or existing preview update is authorized by this increment.

## Acceptance

Real SQLite/HTTP tests cover inheritance/isolation, token issuance and revocation, disabled identity/team, direct-plus-team union, stale revisions, exact/conflicting replay, rollback, project catalog privacy, human/admin boundaries and last-direct-owner protection. Dedicated PostgreSQL tests use two clients for mutation/issuance contention, backup and restore without credentials, then independently read back exact team/binding/history/replay/audit state. Desktop/390px browser workflows exercise real APIs, nonadministrator inherited roles, direct removal, changes/revocation and keyboard/focus/overflow. Full Go race/vet/build/tidy, frontend build, preservation inventories and exported deliverable readback close this increment. Native/device/signing/store/production and full P3 remain open.
