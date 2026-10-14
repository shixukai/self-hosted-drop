export type SiteStatus = "DRAFT" | "ACTIVE" | "PAUSED" | "DELETED";
export type Site = {
  id: string;
  slug: string;
  name: string;
  description: string;
  status: SiteStatus;
  desiredStatus: SiteStatus;
  generation: number;
  appliedGeneration: number;
  appliedDeploymentId: string | null;
  createdAt: string;
  updatedAt: string;
  url: string;
  deploymentCount: number;
  bytes: number;
};
export type Warning = {
  code: string;
  file: string;
  reference: string;
  message: string;
};
export type Report = {
  status: string;
  files: { path: string; size: number; sha256: string }[];
  candidates: { rootDirectory: string; entryPath: string }[];
  warnings: Warning[];
  rootDirectory: string;
  entryPath: string;
  routingMode: string;
  inspectionReportHash: string;
  revision: number;
  artifactHash: string;
};
export type Deployment = {
  filename: string;
  id: string;
  siteId: string;
  status: string;
  artifactState: string;
  metadata: {
    message: string;
    routingMode: string;
    publishAfterReady: boolean;
  };
  report: Report;
  files: Report["files"];
  receivedBytes: number;
  expandedBytes: number;
  fileCount: number;
  errorCode: string;
  errorMessage: string;
  autoPublicationId: string | null;
  autoPublishErrorCode: string;
  autoPublishErrorMessage: string;
  pinned: boolean;
  createdAt: string;
};
export type Publication = {
  action: string;
  createdAt: string;
  appliedAt: string | null;
  errorCode: string;
  id: string;
  siteId: string;
  status: string;
  errorMessage: string;
  targetGeneration: number;
};
export type Token = {
  id: string;
  name: string;
  prefix: string;
  scopes: string[];
  siteIds: string[];
  expiresAt: string;
  revokedAt: string | null;
  lastUsedAt: string | null;
};
export type System = {
  version: string;
  usedBytes: number;
  reservedBytes: number;
  quotaBytes: number;
  siteCount: number;
  queuedJobs: number;
  diskFreeBytes: number;
  gatewayReady: boolean;
  latestVerifiedBackup: string | null;
  backupConfigured: boolean;
  contentDomain: string;
  adminOrigin: string;
};
export type Session = { administrator: boolean; csrfToken: string };
export function publicationBlockedReason(site: Site): string {
  if (site.status === "DELETED" || site.desiredStatus === "DELETED")
    return "站点已移入回收站，请先恢复站点。";
  if (site.status === "PAUSED" || site.desiredStatus === "PAUSED")
    return "站点已暂停，请先在站点详情恢复服务，再发布或回滚。暂停期间仍可上传和检查新版本。";
  return "";
}
let csrf = "";
export function setCSRF(value: string) {
  csrf = value;
}
export class APIError extends Error {
  constructor(
    public code: string,
    message: string,
    public status: number,
  ) {
    super(message);
  }
}
export async function api<T>(
  path: string,
  options: RequestInit = {},
): Promise<T> {
  const method = options.method || "GET";
  const headers = new Headers(options.headers);
  if (options.body) headers.set("Content-Type", "application/json");
  if (method !== "GET") headers.set("X-CSRF-Token", csrf);
  const response = await fetch("/api/v1" + path, {
    ...options,
    headers,
    credentials: "same-origin",
  });
  if (!response.ok) {
    const data = await response.json().catch(() => ({
      code: "NETWORK_ERROR",
      message: "请求失败，请稍后重试",
    }));
    throw new APIError(data.code, data.message, response.status);
  }
  if (response.status === 204) return undefined as T;
  return response.json();
}
export const mutate = <T>(path: string, body: unknown, method = "POST") =>
  api<T>(path, {
    method,
    body: JSON.stringify(body),
    headers: { "Idempotency-Key": crypto.randomUUID() },
  });
export type Page<T> = { items: T[]; nextCursor: string | null };
export function upload(
  siteId: string,
  file: File,
  metadata: unknown,
  onProgress: (n: number) => void,
  signal: AbortSignal,
): Promise<Deployment> {
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest();
    xhr.open("POST", "/api/v1/sites/" + siteId + "/deployments");
    xhr.setRequestHeader("X-CSRF-Token", csrf);
    xhr.setRequestHeader("Idempotency-Key", crypto.randomUUID());
    const abort = () => xhr.abort();
    signal.addEventListener("abort", abort, { once: true });
    xhr.upload.onprogress = (e) => {
      if (e.lengthComputable)
        onProgress(Math.round((e.loaded / e.total) * 100));
    };
    xhr.onload = () => {
      signal.removeEventListener("abort", abort);
      try {
        const data = JSON.parse(xhr.responseText);
        if (xhr.status >= 200 && xhr.status < 300) resolve(data);
        else reject(new APIError(data.code, data.message, xhr.status));
      } catch {
        reject(new Error("服务器响应无效，请查询部署状态"));
      }
    };
    xhr.onerror = () => reject(new Error("连接中断，请刷新部署历史核对结果"));
    xhr.onabort = () => reject(new Error("上传已取消"));
    const body = new FormData();
    body.append("metadata", JSON.stringify(metadata));
    body.append("file", file);
    xhr.send(body);
  });
}
export async function awaitPublication(id: string): Promise<Publication> {
  for (let i = 0; i < 90; i++) {
    const p = await api<Publication>("/publications/" + id);
    if (p.status === "APPLIED") return p;
    if (["FAILED", "CONFLICT", "CANCELED"].includes(p.status))
      throw new Error(p.errorMessage || "发布未完成");
    if (p.status === "RECOVERING" && i > 15)
      throw new Error("网关正在恢复，可在部署记录中查询结果");
    await new Promise((r) => setTimeout(r, Math.min(1000 + i * 30, 3000)));
  }
  throw new Error("操作仍在处理中，请刷新站点查询，勿重复发布");
}
export function size(n = 0) {
  if (n < 1024) return n + " B";
  if (n < 1048576) return (n / 1024).toFixed(1) + " KiB";
  if (n < 1073741824) return (n / 1048576).toFixed(1) + " MiB";
  return (n / 1073741824).toFixed(1) + " GiB";
}
export function date(s?: string | null) {
  return s
    ? new Intl.DateTimeFormat("zh-CN", {
        month: "short",
        day: "numeric",
        hour: "2-digit",
        minute: "2-digit",
      }).format(new Date(s))
    : "—";
}
export function siteKind(slug: string): "report" | "api" | "product" {
  const n = [...slug].reduce((n, c) => n + c.charCodeAt(0), 0) % 3;
  return (["report", "api", "product"] as const)[n];
}
export const busyStatuses = [
  "RECEIVING",
  "QUEUED",
  "VALIDATING",
  "EXTRACTING",
  "CHECKING",
];
