import type { Data } from "./api";

export function SourceWriteScope({ task }: { task: Data }) {
  if (task.kind !== "code") return null;
  return (
    <div className="source-write-scope" aria-label="批准的源码修改范围">
      <small>源码修改范围</small>
      {Array.isArray(task.writePaths) ? (
        <ul aria-label="允许修改的路径">
          {task.writePaths.map((path: string) => (
            <li key={path}>
              <code>{path}</code>
            </li>
          ))}
        </ul>
      ) : (
        <p>全仓库（未限定路径）</p>
      )}
    </div>
  );
}
