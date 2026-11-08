import { useCallback, useEffect, useRef, useState } from "react";
import { Plus, RefreshCw } from "lucide-react";
import { api, roles, type Actor, type Resource } from "./api";
import { useWorkspace } from "./context";
import { Badge, Button, Form, SectionHeading } from "./ui";

const humanRoles = Object.entries(roles).filter(
  ([role]) => !["analysis", "design"].includes(role),
);

function TeamForm({
  team,
  users,
  saved,
}: {
  team?: Resource;
  users: Actor[];
  saved: () => Promise<void>;
}) {
  const w = useWorkspace();
  const [chosen, setChosen] = useState<string[]>(team?.data.memberIds || []);
  const [key] = useState(() => crypto.randomUUID());
  const choices = [
    ...users,
    ...chosen
      .filter((id) => !users.some((user) => user.id === id))
      .map((id) => ({ id, name: "已停用或不可用账户 " + id })),
  ];
  return (
    <Form
      fields={[
        {
          name: "title",
          label: "团队名称",
          required: true,
          defaultValue: team?.title,
        },
        {
          name: "description",
          label: "团队说明",
          type: "textarea",
          defaultValue: team?.data.description,
        },
        ...(team
          ? [{ name: "reason", label: "变更原因", required: true }]
          : []),
      ]}
      submit={team ? "保存团队" : "创建团队"}
      onSubmit={async (values) => {
        await w.run(team ? "teams.update" : "teams.create", {
          ...values,
          memberIds: chosen,
          key,
          ...(team ? { id: team.id, expectedRevision: team.revision } : {}),
        });
        await saved();
        w.close();
        w.notify("组织团队已保存");
      }}
    >
      <fieldset className="target-picker team-member-picker">
        <legend>团队成员（已选择 {chosen.length} 位）</legend>
        {choices.map((user) => (
          <label key={user.id}>
            <input
              type="checkbox"
              aria-label={user.name}
              checked={chosen.includes(user.id)}
              onChange={(event) =>
                setChosen(
                  event.target.checked
                    ? [...chosen, user.id]
                    : chosen.filter((id) => id !== user.id),
                )
              }
            />
            <span>
              {user.name}
              <small className="mono">{user.id}</small>
            </span>
          </label>
        ))}
        {!choices.length && (
          <p className="muted">请先在项目成员中开通企业账户。</p>
        )}
      </fieldset>
      <p className="form-note">
        团队成员变更会动态影响已授权的项目；受影响成员在这些项目签发的 Agent 和
        Worker 凭据会失效。团队最多 1000 人，可以为空。
      </p>
      {team && (
        <p className="form-note">
          团队版本 {team.revision}
          。停用账户的历史成员需移除后才能保存或启用团队。
        </p>
      )}
    </Form>
  );
}

function TeamStateForm({
  team,
  saved,
}: {
  team: Resource;
  saved: () => Promise<void>;
}) {
  const w = useWorkspace();
  const enable = team.status === "disabled";
  const [key] = useState(() => crypto.randomUUID());
  return (
    <Form
      fields={[
        { name: "reason", label: "变更原因", type: "textarea", required: true },
      ]}
      submit={enable ? "确认启用团队" : "确认停用团队"}
      onSubmit={async (values) => {
        await w.run(enable ? "teams.enable" : "teams.disable", {
          id: team.id,
          expectedRevision: team.revision,
          reason: values.reason,
          key,
        });
        await saved();
        w.close();
        w.notify(
          enable
            ? "团队访问已恢复，旧凭据仍然失效"
            : "团队已停用，项目授权与历史记录保留",
        );
      }}
    >
      <p className="form-note">
        {enable ? "启用" : "停用"} {team.title}（版本 {team.revision}）。
        {enable
          ? "当前成员会重新获得已绑定的项目角色；旧凭据不会恢复。"
          : "所有通过该团队继承的项目角色会暂停；直接授权和其他团队角色仍然有效。"}
      </p>
    </Form>
  );
}

function GrantForm({
  team,
  grant,
  revoke = false,
}: {
  team: Resource;
  grant?: Resource;
  revoke?: boolean;
}) {
  const w = useWorkspace();
  const [key] = useState(() => crypto.randomUUID());
  const [chosen, setChosen] = useState<string[]>(
    grant?.data.roles || ["developer"],
  );
  return (
    <Form
      fields={[
        { name: "reason", label: "授权原因", type: "textarea", required: true },
      ]}
      submit={revoke ? "确认撤销授权" : "确认团队授权"}
      onSubmit={async (values) => {
        if (!revoke && !chosen.length) throw new Error("至少选择一个项目角色");
        await w.run(
          revoke ? "teams.revoke" : "teams.grant",
          revoke
            ? {
                id: grant!.id,
                expectedRevision: grant!.revision,
                reason: values.reason,
                key,
              }
            : {
                projectId: w.project.id,
                teamId: team.id,
                expectedTeamRevision: team.revision,
                ...(grant ? { expectedRevision: grant.revision } : {}),
                roles: chosen,
                reason: values.reason,
                key,
              },
        );
        w.close();
        w.notify(revoke ? "团队项目授权已撤销" : "团队项目授权已保存");
      }}
    >
      <p className="form-note">
        {team.title} · 团队版本 {team.revision}
        {grant && ` · 授权版本 ${grant.revision}`}
      </p>
      {!revoke && (
        <fieldset className="target-picker member-role-picker">
          <legend>团队项目角色（可多选）</legend>
          {humanRoles.map(([role, label]) => (
            <label key={role}>
              <input
                type="checkbox"
                checked={chosen.includes(role)}
                onChange={(event) =>
                  setChosen(
                    event.target.checked
                      ? [...chosen, role]
                      : chosen.filter((value) => value !== role),
                  )
                }
              />
              {label}
            </label>
          ))}
        </fieldset>
      )}
      <p className="form-note">
        {revoke
          ? "撤销后，直接授权和其他团队角色仍然有效。此团队成员在当前项目签发的凭据会失效。"
          : "授权适用于团队当前成员及后续成员变更。团队角色与直接授权合并生效，项目必须保留至少一位直接授权的负责人。"}
      </p>
    </Form>
  );
}

export function OrganizationTeams() {
  const w = useWorkspace();
  const admin = w.actor.kind === "human" && w.actor.admin;
  const manage = w.actor.kind === "human" && w.can("owner");
  const grants = w.select("team_grant");
  const signature = grants
    .map(
      (grant) =>
        `${grant.id}:${grant.revision}:${grant.data.currentTeamRevision}:${grant.data.teamStatus}`,
    )
    .join("|");
  const [teams, setTeams] = useState<Resource[]>([]);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const [opening, setOpening] = useState(false);
  const generation = useRef(0);
  const catalogPath = admin ? "teams" : "teams?projectId=" + w.project.id;
  const load = useCallback(async () => {
    const ticket = ++generation.current;
    if (!manage) {
      setTeams([]);
      setError("");
      setLoading(false);
      return;
    }
    setLoading(true);
    setError("");
    try {
      const values = await api<Resource[]>(catalogPath);
      if (ticket === generation.current) setTeams(values);
    } catch (error) {
      if (ticket === generation.current) setError((error as Error).message);
    } finally {
      if (ticket === generation.current) setLoading(false);
    }
  }, [catalogPath, manage]);
  useEffect(() => {
    void load();
    return () => {
      generation.current++;
    };
  }, [load, signature]);
  async function edit(team?: Resource) {
    if (opening) return;
    setOpening(true);
    setError("");
    try {
      const [users, exact] = await Promise.all([
        api<Actor[]>("identities"),
        team
          ? api<Resource>("resources/" + team.id)
          : Promise.resolve(undefined),
      ]);
      w.open(
        team ? "编辑组织团队" : "新建组织团队",
        <TeamForm team={exact} users={users} saved={load} />,
      );
    } catch (error) {
      setError((error as Error).message);
    } finally {
      setOpening(false);
    }
  }
  const unavailable = loading || opening || !!error;
  return (
    <>
      <SectionHeading
        title="项目团队授权"
        description="团队成员变更会动态生效；直接角色与团队角色共同决定项目访问。"
      />
      <div className="member-list">
        {grants.map((grant) => {
          const team = teams.find((team) => team.id === grant.data.teamId);
          return (
            <article
              className="resource-row member-row team-grant-row"
              aria-label={"项目团队 " + grant.data.teamId}
              key={grant.id}
            >
              <div className="member-identity">
                <strong>{grant.title}</strong>
                <small>
                  团队版本 {grant.data.currentTeamRevision} · 授权版本{" "}
                  {grant.revision} · {grant.data.memberCount} 位启用成员
                </small>
                <small>团队成员变更会动态生效</small>
                {grant.status === "active" &&
                  grant.data.teamStatus === "disabled" && (
                    <small>团队已停用，继承访问暂停</small>
                  )}
                {grant.data.reason && <small>{grant.data.reason}</small>}
              </div>
              <div className="member-roles">
                {grant.data.roles
                  .map((role: string) => roles[role] || role)
                  .join(" · ")}
              </div>
              <Badge status={grant.status} />
              {manage && (
                <div className="button-group member-actions">
                  <Button
                    secondary
                    disabled={unavailable || !team || team.status !== "active"}
                    onClick={() =>
                      team &&
                      w.open(
                        "团队项目角色",
                        <GrantForm team={team} grant={grant} />,
                      )
                    }
                  >
                    {grant.status === "revoked"
                      ? "重新授权团队"
                      : "编辑团队角色"}
                  </Button>
                  {grant.status === "active" && (
                    <Button
                      secondary
                      disabled={opening}
                      onClick={() =>
                        w.open(
                          "撤销团队项目授权",
                          <GrantForm
                            team={
                              team || {
                                ...grant,
                                id: grant.data.teamId,
                                revision: grant.data.currentTeamRevision,
                              }
                            }
                            grant={grant}
                            revoke
                          />,
                        )
                      }
                    >
                      撤销团队授权
                    </Button>
                  )}
                </div>
              )}
            </article>
          );
        })}
        {!grants.length && <p className="muted">当前项目尚未绑定组织团队。</p>}
      </div>
      {manage && (
        <>
          <SectionHeading
            title="组织团队目录"
            description={
              admin
                ? "管理企业账户的团队归属，再明确授予每个项目的角色。"
                : "项目负责人可选择团队授权；全局成员名单由企业管理员管理。"
            }
            action={
              <div className="button-group">
                <Button
                  secondary
                  disabled={loading || opening}
                  onClick={() => void load()}
                >
                  <RefreshCw size={15} />
                  刷新团队目录
                </Button>
                {admin && (
                  <Button
                    disabled={loading}
                    aria-disabled={opening}
                    aria-busy={opening}
                    onClick={() => void edit()}
                  >
                    <Plus size={15} />
                    新建组织团队
                  </Button>
                )}
              </div>
            }
          />
          {loading && (
            <p className="muted" role="status">
              正在加载团队目录…
            </p>
          )}
          {error && (
            <p className="form-error" role="alert">
              {error}
            </p>
          )}
          <div className="member-list">
            {teams.map((team) => (
              <article
                className="resource-row member-row organization-team-row"
                key={team.id}
                aria-label={"组织团队 " + team.id}
              >
                <div className="member-identity">
                  <strong>{team.title}</strong>
                  <small>{team.data.description || "尚无团队说明"}</small>
                  <small>{team.data.memberCount} 位启用成员</small>
                  <small>
                    团队版本 {team.revision}
                    {team.data.recordedMemberCount !== team.data.memberCount &&
                      ` · 另有 ${team.data.recordedMemberCount - team.data.memberCount} 位已停用成员`}
                  </small>
                </div>
                <Badge status={team.status} />
                <div className="button-group member-actions">
                  {admin && (
                    <>
                      <Button
                        secondary
                        disabled={loading || !!error}
                        aria-disabled={opening}
                        aria-busy={opening}
                        onClick={() => void edit(team)}
                      >
                        编辑团队
                      </Button>
                      <Button
                        secondary
                        disabled={unavailable}
                        onClick={() =>
                          w.open(
                            team.status === "disabled"
                              ? "启用组织团队"
                              : "停用组织团队",
                            <TeamStateForm team={team} saved={load} />,
                          )
                        }
                      >
                        {team.status === "disabled" ? "启用团队" : "停用团队"}
                      </Button>
                    </>
                  )}
                  {!grants.some((grant) => grant.data.teamId === team.id) && (
                    <Button
                      secondary
                      disabled={unavailable || team.status !== "active"}
                      onClick={() =>
                        w.open("授权团队到项目", <GrantForm team={team} />)
                      }
                    >
                      授权到当前项目
                    </Button>
                  )}
                </div>
              </article>
            ))}
            {!teams.length && !loading && !error && (
              <p className="muted">企业尚无组织团队。</p>
            )}
          </div>
        </>
      )}
    </>
  );
}
