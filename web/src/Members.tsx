import { useState } from "react";
import { Plus } from "lucide-react";
import { api, date, post, roles, type Actor, type Resource } from "./api";
import { useWorkspace } from "./context";
import { Badge, Button, Form, SectionHeading } from "./ui";

const humanRoles = Object.entries(roles).filter(
  ([role]) => !["analysis", "design"].includes(role),
);

function MembershipForm({
  member,
  users = [],
}: {
  member?: Resource;
  users?: Actor[];
}) {
  const w = useWorkspace();
  const [chosen, setChosen] = useState<string[]>(
    member?.data.roles || ["developer"],
  );
  const regrant = member?.status === "revoked";
  return (
    <Form
      fields={
        member
          ? []
          : [
              {
                name: "identityId",
                label: "企业成员",
                type: users.length ? "select" : undefined,
                options: Object.fromEntries(
                  users.map((user) => [user.id, user.name]),
                ),
                required: true,
                hint: "使用已开通且启用的企业账户。",
              },
            ]
      }
      submit={regrant ? "确认重新授权" : member ? "保存角色" : "授予项目角色"}
      onSubmit={async (values) => {
        if (!chosen.length) throw new Error("至少选择一个项目角色");
        const identityId = member?.data.identityId || values.identityId;
        if (
          !member &&
          w.select("member").some((item) => item.data.identityId === identityId)
        ) {
          throw new Error("该账户已有成员记录，请使用编辑角色或重新授权");
        }
        await w.run("members.grant", {
          projectId: w.project.id,
          identityId,
          roles: chosen,
          ...(member ? { expectedRevision: member.revision } : {}),
        });
        w.close();
        w.notify(regrant ? "项目访问已恢复，旧凭据仍然失效" : "项目角色已保存");
      }}
    >
      {member && (
        <p className="form-note">
          {member.title} ·{" "}
          <span className="mono">{member.data.identityId}</span>
        </p>
      )}
      <fieldset className="target-picker member-role-picker">
        <legend>项目角色（可多选）</legend>
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
      <p className="form-note">
        {regrant
          ? "重新授权不会恢复旧的 Agent 或 Worker 凭据；项目负责人需重新签发。"
          : "角色发生变化时，该成员在此项目签发的 Agent 和 Worker 凭据会失效；相同角色不影响凭据。"}
      </p>
      {member && (
        <p className="form-note">
          当前成员版本 {member.revision}。若版本已变化，请关闭弹窗并刷新后重试。
        </p>
      )}
    </Form>
  );
}

function RemovalForm({ member }: { member: Resource }) {
  const w = useWorkspace();
  return (
    <Form
      fields={[
        {
          name: "reason",
          label: "移除原因",
          type: "textarea",
          required: true,
          hint: "记录职责调整或离职交接依据，最多 2048 字节。",
        },
      ]}
      submit="确认移除"
      onSubmit={async (values) => {
        await w.run("members.revoke", {
          id: member.id,
          expectedRevision: member.revision,
          reason: values.reason,
        });
        w.close();
        w.notify("成员已移除，项目记录与审计历史保留");
      }}
    >
      <div className="form-note member-removal-note">
        <p>移除 {member.title || member.data.identityId} 的直接项目授权。</p>
        <p>通过组织团队继承的角色仍然有效；需要在组织团队中另行调整。</p>
        <p>该成员在此项目签发的 Agent 和 Worker 凭据会立即失效。</p>
        <p>
          平台管理员仍可通过全局权限访问项目；停用账户需由企业管理员另行处理。
        </p>
        <p>
          已经派发的远程操作仍需跟踪实际结果；移除成员不会证明这些操作已停止。
        </p>
      </div>
    </Form>
  );
}

export function Members() {
  const w = useWorkspace();
  const members = w.select("member");
  const inherited = w
    .select("team_grant")
    .filter((grant) => grant.data.effectiveForCurrentActor);
  const manage = w.actor.kind === "human" && w.can("owner");
  const [error, setError] = useState("");
  async function addMember() {
    setError("");
    let users: Actor[] = [];
    try {
      users = await api<Actor[]>("identities");
    } catch (error) {
      // Owners without enterprise admin rights can use a known identity ID.
      if (w.actor.admin) {
        setError((error as Error).message);
        return;
      }
    }
    users = users.filter(
      (user) => !members.some((member) => member.data.identityId === user.id),
    );
    w.open("授予项目角色", <MembershipForm users={users} />);
  }
  return (
    <>
      <SectionHeading
        title="项目成员与角色"
        description="直接授权与团队角色共同生效。移除直接授权后，团队角色仍可保留项目访问。"
        action={
          <div className="button-group">
            {w.actor.admin && w.actor.kind === "human" && (
              <Button
                secondary
                onClick={() =>
                  w.open(
                    "开通企业成员",
                    <Form
                      fields={[
                        { name: "name", label: "成员姓名", required: true },
                        { name: "email", label: "企业邮箱", required: true },
                        {
                          name: "password",
                          label: "初始密码（至少 14 位）",
                          type: "password",
                          required: true,
                        },
                      ]}
                      submit="创建成员身份"
                      onSubmit={async (values) => {
                        await post("identities", values);
                        w.close();
                        w.notify("成员身份已创建，请继续授予项目角色");
                      }}
                    />,
                  )
                }
              >
                开通成员
              </Button>
            )}
            {manage && (
              <Button onClick={addMember}>
                <Plus size={15} />
                添加项目成员
              </Button>
            )}
          </div>
        }
      />
      <div
        className="source-details member-access-sources"
        aria-label="我的项目权限来源"
      >
        <p>
          当前有效角色：
          {(w.project.data.effectiveRoles || [])
            .map((role: string) => roles[role] || role)
            .join(" · ") || (w.actor.admin ? "平台管理员" : "暂无")}
        </p>
        {members
          .filter(
            (member) =>
              member.status === "active" &&
              member.data.identityId === w.actor.id,
          )
          .map((member) => (
            <p key={member.id}>
              直接授权：
              {member.data.roles
                .map((role: string) => roles[role] || role)
                .join(" · ")}
            </p>
          ))}
        {inherited.map((grant) => (
          <p key={grant.id}>来自团队：{grant.title}</p>
        ))}
        {w.actor.admin && (
          <p>平台管理员拥有企业管理权限；项目仍需保留一位直接授权的负责人。</p>
        )}
      </div>
      {error && (
        <div className="form-error" role="alert">
          {error}
        </div>
      )}
      <div className="member-list">
        {members.map((member) => (
          <article
            className="resource-row member-row"
            aria-label={"成员 " + member.data.identityId}
            key={member.id}
          >
            <span className="member-avatar">
              {(member.title || member.data.identityId).slice(0, 1)}
            </span>
            <div className="member-identity">
              <strong>{member.title || member.data.identityId}</strong>
              <small className="mono">{member.data.identityId}</small>
              <small>
                成员版本 {member.revision}
                {member.data.revocationReason &&
                  ` · 最近移除：${date(member.data.revokedAt)} · ${member.data.revocationReason}`}
              </small>
            </div>
            <div className="member-roles">
              {member.data.roles
                .map((role: string) => roles[role] || role)
                .join(" · ")}
            </div>
            <Badge status={member.status} />
            {manage && (
              <div className="button-group member-actions">
                <Button
                  secondary
                  onClick={() =>
                    w.open(
                      member.status === "revoked"
                        ? "重新授权项目成员"
                        : "编辑项目角色",
                      <MembershipForm member={member} />,
                    )
                  }
                >
                  {member.status === "revoked" ? "重新授权" : "编辑角色"}
                </Button>
                {member.status === "active" && (
                  <Button
                    secondary
                    onClick={() =>
                      w.open("移除项目成员", <RemovalForm member={member} />)
                    }
                  >
                    移除成员
                  </Button>
                )}
              </div>
            )}
          </article>
        ))}
      </div>
    </>
  );
}
