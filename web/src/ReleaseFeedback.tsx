import { api, targets, type Resource } from "./api";
import { useWorkspace } from "./context";
import { Button, Form } from "./ui";

export function ReleaseFeedback({
  revision,
  canWrite,
}: {
  revision: number;
  canWrite: boolean;
}) {
  const w = useWorkspace();
  const choices = w
    .select("release")
    .filter(
      (r) => r.goalId === w.goal?.id && r.data.requirementRevision === revision,
    )
    .flatMap((release) =>
      (release.data.artifacts || [])
        .filter((artifact: { id: string; target: string }) =>
          (release.data.targets || []).some(
            (target: {
              target: string;
              artifactId: string;
              status: string;
              observedHistory?: { to: string }[];
            }) =>
              target.target === artifact.target &&
              target.artifactId === artifact.id &&
              (release.status === "delivered" ||
                ["published", "verified"].includes(target.status) ||
                target.observedHistory?.some((event) =>
                  ["published", "verified"].includes(event.to),
                )),
          ),
        )
        .map((artifact: { id: string; target: string }) => ({
          release,
          artifact,
          value: release.id + "/" + artifact.id,
        })),
    );
  function open() {
    const key = crypto.randomUUID();
    w.open(
      "上传上线问题报告",
      <Form
        fields={[
          {
            name: "source",
            label: "已发布版本与制品",
            type: "select",
            required: true,
            options: Object.fromEntries(
              choices.map((choice) => [
                choice.value,
                choice.release.title +
                  " · 需求 v" +
                  revision +
                  " · " +
                  (targets[choice.artifact.target] || choice.artifact.target) +
                  " · " +
                  choice.artifact.id.slice(-8),
              ]),
            ),
          },
          {
            name: "file",
            label: "问题报告文件",
            type: "file",
            required: true,
            hint: "选择与该发布版本有关的问题材料，大小不超过 16 MiB。重试时保留原文件；更换材料请关闭并重新上传。",
          },
        ]}
        submit="上传报告"
        onSubmit={async (values) => {
          const choice = choices.find(
            (choice) => choice.value === values.source,
          );
          const file = values.file as File;
          if (!choice || !file || file.size < 1 || file.size > 16 * 1024 * 1024)
            throw new Error("请选择准确的已发布制品与 1 B–16 MiB 的报告文件");
          await api<Resource>(
            "releases/" +
              encodeURIComponent(choice.release.id) +
              "/reports?" +
              new URLSearchParams({
                artifactId: choice.artifact.id,
                name: file.name,
                key,
              }),
            { method: "POST", body: file },
          );
          await w.refresh();
          w.close();
        }}
      >
        <p className="form-note">
          报告归属需求 v{revision}{" "}
          的准确发布版本。上传仅保存问题材料；需要随后登记上线缺陷，不改变验收、发布或修复结论。
        </p>
      </Form>,
    );
  }
  return (
    <Button secondary disabled={!canWrite || !choices.length} onClick={open}>
      上传上线问题报告
    </Button>
  );
}
