import React, { useEffect, useRef, useState } from "react";
import { createRoot } from "react-dom/client";
import {
  QueryClient,
  QueryClientProvider,
  useInfiniteQuery,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import {
  BrowserRouter,
  Link,
  Navigate,
  Route,
  Routes,
  useLocation,
  useNavigate,
  useParams,
  useSearchParams,
} from "react-router-dom";
import {
  Button,
  Card,
  Chip,
  Input,
  Label,
  Modal,
  Tabs,
  TextField,
} from "@heroui/react";
import {
  ArrowLeft,
  ArrowRight,
  ArrowUpRight,
  Check,
  CheckCheck,
  ChevronDown,
  Clock3,
  Copy,
  Download,
  FileCode2,
  FolderUp,
  Globe2,
  HardDrive,
  KeyRound,
  LayoutGrid,
  List,
  LogOut,
  Moon,
  Plus,
  RefreshCw,
  Search,
  ShieldCheck,
  Sun,
  Trash2,
  Upload,
  X,
  Pin,
  Pause,
  Play,
  AlertCircle,
  Settings2,
} from "lucide-react";
import {
  api,
  mutate,
  setCSRF,
  upload,
  awaitPublication,
  size,
  date,
  siteKind,
  busyStatuses,
  APIError,
  type Site,
  type Deployment,
  type Session,
  type Publication,
  type Page,
  type Token,
  type System,
} from "../api";
import { FileStack, SiteArtwork } from "../artwork";
import "@fontsource-variable/manrope";
import {
  fromFileList,
  readDroppedFiles,
  selectionLabel,
  suggestedSite,
  validateFiles,
  validSlug,
  type UploadFile,
} from "../upload-files";

import {
  Status,
  ErrorNote,
  Loading,
  useAction,
  useSystem,
  useSites,
  Brand,
  Confirm,
  messages,
} from "../components";
export default function UploadPage() {
  const nav = useNavigate(),
    qc = useQueryClient();
  const [params, setParams] = useSearchParams();
  const siteId = params.get("site") || "";
  const deploymentId = params.get("deployment") || "";
  const site = useQuery({
    queryKey: ["site", siteId],
    queryFn: () => api<Site>("/sites/" + siteId),
    enabled: !!siteId,
  });
  const system = useSystem();
  const [name, setName] = useState(""),
    [slug, setSlug] = useState(""),
    [mode, setMode] = useState("static"),
    [message, setMessage] = useState(""),
    [files, setFiles] = useState<UploadFile[]>([]),
    [progress, setProgress] = useState(0),
    [reading, setReading] = useState(false),
    [readCount, setReadCount] = useState(0),
    [packing, setPacking] = useState(false),
    [dragging, setDragging] = useState(false),
    [ack, setAck] = useState(false),
    [selection, setSelection] = useState("0"),
    [published, setPublished] = useState<string | null>(null);
  const action = useAction();
  const abort = useRef<AbortController | null>(null),
    worker = useRef<Worker | null>(null),
    fileInput = useRef<HTMLInputElement>(null),
    folderInput = useRef<HTMLInputElement>(null);
  const deployment = useQuery({
    queryKey: ["deployment", deploymentId],
    queryFn: () => api<Deployment>("/deployments/" + deploymentId),
    enabled: !!deploymentId,
    refetchInterval: (q) =>
      q.state.data && busyStatuses.includes(q.state.data.status) ? 1000 : false,
  });
  const d = deployment.data;
  useEffect(() => {
    if (site.data) {
      setName(site.data.name);
      setSlug(site.data.slug);
    }
  }, [site.data]);
  useEffect(() => {
    const protect = (e: BeforeUnloadEvent) => {
      if (action.busy) {
        e.preventDefault();
        e.returnValue = "";
      }
    };
    window.addEventListener("beforeunload", protect);
    return () => window.removeEventListener("beforeunload", protect);
  }, [action.busy]);
  useEffect(
    () => () => {
      abort.current?.abort();
      worker.current?.terminate();
    },
    [],
  );
  function choose(next: UploadFile[]) {
    action.clear();
    setFiles(next);
    setAck(false);
    setPublished(null);
    if (next.length && !siteId) {
      const suggestion = suggestedSite(next);
      setName((current) => current || suggestion.name);
      setSlug((current) => current || suggestion.slug);
    }
  }
  function chooseInput(input: HTMLInputElement) {
    const next = fromFileList(Array.from(input.files || []));
    input.value = "";
    if (!next.length || action.busy) return;
    void action.run(async () => {
      setFiles([]);
      validateFiles(next);
      choose(next);
    });
  }
  async function chooseDrop(data: DataTransfer) {
    if (action.busy || d) return;
    const controller = new AbortController();
    abort.current = controller;
    setFiles([]);
    setReading(true);
    setReadCount(0);
    await action.run(async () => {
      choose(await readDroppedFiles(data, controller.signal, setReadCount));
    });
    setReading(false);
  }
  async function bundle(): Promise<File> {
    if (files.length === 1 && !files[0].path.includes("/"))
      return files[0].file;
    setPacking(true);
    return new Promise((resolve, reject) => {
      const w = new Worker(new URL("../zip.worker.ts", import.meta.url), {
        type: "module",
      });
      worker.current = w;
      const canceled = () => {
        w.terminate();
        setPacking(false);
        reject(new Error("目录打包已取消"));
      };
      abort.current?.signal.addEventListener("abort", canceled, { once: true });
      w.onmessage = (e) => {
        if (e.data.error) {
          w.terminate();
          setPacking(false);
          reject(new Error(e.data.error));
        } else if (e.data.blob) {
          w.terminate();
          abort.current?.signal.removeEventListener("abort", canceled);
          setPacking(false);
          resolve(
            new File([e.data.blob], "website.zip", { type: "application/zip" }),
          );
        } else setProgress(e.data.progress);
      };
      w.onerror = () => {
        w.terminate();
        setPacking(false);
        reject(new Error("目录打包失败，请使用 ZIP 上传"));
      };
      w.postMessage({ files });
    });
  }
  async function start() {
    await action.run(async () => {
      validateFiles(files);
      let sid = siteId;
      if (!sid) {
        const s = await mutate<Site>("/sites", { name, slug, description: "" });
        sid = s.id;
        setParams({ site: sid });
        qc.invalidateQueries({ queryKey: ["sites"] });
      }
      abort.current = new AbortController();
      setProgress(0);
      const file = await bundle();
      if (file.size > 200 * 1024 * 1024)
        throw new Error("单次上传最大 200 MiB");
      setProgress(0);
      const result = await upload(
        sid,
        file,
        {
          interactionMode: "interactive",
          routingMode: mode,
          message,
          publishAfterReady: false,
        },
        setProgress,
        abort.current.signal,
      );
      setParams({ site: sid, deployment: result.id });
      qc.setQueryData(["deployment", result.id], result);
      qc.invalidateQueries({ queryKey: ["deployments", sid] });
      setFiles([]);
    });
  }
  async function publish() {
    if (!d) return;
    await action.run(async () => {
      const s = await api<Site>("/sites/" + d.siteId);
      const p = await mutate<Publication>("/sites/" + s.id + "/publications", {
        deploymentId: d.id,
        expectedGeneration: s.generation,
        action: "PUBLISH",
        inspectionReportHash: d.report.inspectionReportHash,
        acknowledgeWarnings: ack,
      });
      await awaitPublication(p.id);
      setPublished(s.url);
      qc.invalidateQueries({ queryKey: ["sites"] });
      qc.invalidateQueries({ queryKey: ["site", s.id] });
      qc.invalidateQueries({ queryKey: ["system"] });
    });
  }
  const uploadRequirement = !files.length
    ? "先添加 HTML、ZIP 或网站目录。"
    : !name.trim()
      ? "请填写显示名称，再继续上传。"
      : !slug
        ? "请填写站点标识，它会成为分享地址的一部分。"
        : !validSlug(slug)
          ? "站点标识须为 3–40 位小写字母、数字或连字符，首尾为字母或数字；请避开保留名称及 d- 前缀。"
          : "";
  return (
    <section className="view-enter">
      <Button
        variant="ghost"
        className="back-button"
        onPress={() => {
          if (!action.busy || confirm("离开会中断当前传输，确定离开？"))
            nav(siteId ? "/sites/" + siteId : "/");
        }}
      >
        <ArrowLeft size={16} />
        返回{siteId ? "站点" : "工作区"}
      </Button>
      <div className="page-intro sub-intro">
        <div>
          <div className="eyebrow">A NEW DROP</div>
          <h1>{siteId ? "给作品，一个新版本。" : "让好想法，被看见。"}</h1>
          <p>
            {siteId
              ? "上传完整版本，分享地址保持不变。"
              : "选好文件，剩下的交给 Drop。"}
          </p>
        </div>
        <span className="step-count">
          {published ? "03" : d ? "02" : "01"}
          <span> / 03</span>
        </span>
      </div>
      <ErrorNote error={action.error || deployment.error || site.error} />
      {published ? (
        <Card className="success-panel">
          <Card.Content>
            <CheckCheck size={38} />
            <h2>已上线，分享这一刻。</h2>
            <p>文件和内容路由均已验证，作品已发布。</p>
            <a href={published} target="_blank" rel="noreferrer">
              {published}
              <ArrowUpRight size={17} />
            </a>
            <Button onPress={() => nav("/sites/" + siteId)}>查看站点</Button>
          </Card.Content>
        </Card>
      ) : (
        <div className="upload-layout">
          <div>
            <Card className="upload-card">
              <Card.Content>
                {d ? (
                  <div className="processing-panel">
                    <FileCode2 size={38} />
                    <h2>{messages[d.status] || d.status}</h2>
                    <p>{d.filename || "静态站点版本"}</p>
                    <Status value={d.status} />
                    {busyStatuses.includes(d.status) && (
                      <>
                        <p>
                          文件已接收，正在服务端检查。可以离开页面，任务会继续。
                        </p>
                        <Button
                          variant="secondary"
                          onPress={() =>
                            action.run(async () => {
                              await mutate(
                                "/deployments/" + d.id + "/cancel",
                                {},
                              );
                              await deployment.refetch();
                            })
                          }
                        >
                          取消处理
                        </Button>
                      </>
                    )}
                    {d.status === "FAILED" && (
                      <>
                        <ErrorNote error={d.errorMessage} />
                        <Button
                          variant="secondary"
                          onPress={() => setParams({ site: siteId })}
                        >
                          重新上传
                        </Button>
                      </>
                    )}
                    {d.status === "CANCELED" && (
                      <Button
                        variant="secondary"
                        onPress={() => setParams({ site: siteId })}
                      >
                        重新上传
                      </Button>
                    )}
                  </div>
                ) : (
                  <div
                    className={"dropzone " + (dragging ? "dragging" : "")}
                    onDragOver={(e) => {
                      e.preventDefault();
                      e.dataTransfer.dropEffect = action.busy ? "none" : "copy";
                      setDragging(!action.busy);
                    }}
                    onDragLeave={() => setDragging(false)}
                    onDrop={(e) => {
                      e.preventDefault();
                      setDragging(false);
                      void chooseDrop(e.dataTransfer);
                    }}
                  >
                    <FileStack />
                    <h2>
                      {dragging ? "松开，放到这里" : "把你的作品拖到这里"}
                    </h2>
                    <p>单个 HTML、ZIP 文件，或已构建的网站目录</p>
                    <div className="choose-actions">
                      <Button
                        onPress={() => fileInput.current?.click()}
                        isDisabled={action.busy}
                      >
                        <Plus size={17} />
                        选择文件
                      </Button>
                      <Button
                        variant="secondary"
                        onPress={() => folderInput.current?.click()}
                        isDisabled={action.busy}
                      >
                        <FolderUp size={17} />
                        选择目录
                      </Button>
                    </div>
                    <span className="upload-limit">
                      最大 200 MiB · 内容检查后由你确认公开
                    </span>
                    <input
                      type="file"
                      accept=".html,.htm,.zip"
                      hidden
                      ref={fileInput}
                      onChange={(e) => chooseInput(e.target)}
                    />
                    <input
                      type="file"
                      multiple
                      hidden
                      ref={folderInput}
                      {...({
                        webkitdirectory: "",
                      } as React.InputHTMLAttributes<HTMLInputElement>)}
                      onChange={(e) => chooseInput(e.target)}
                    />
                  </div>
                )}
                {files.length > 0 && !d && (
                  <div className="file-selection">
                    <span className="file-selection-icon">
                      <FileCode2 size={23} />
                    </span>
                    <div>
                      <strong>{selectionLabel(files)}</strong>
                      <p>
                        {files.length} 个文件 ·{" "}
                        {size(files.reduce((n, f) => n + f.file.size, 0))}
                      </p>
                    </div>
                    <Button
                      isIconOnly
                      variant="ghost"
                      aria-label="移除所选文件"
                      isDisabled={action.busy}
                      onPress={() => choose([])}
                    >
                      <X size={17} />
                    </Button>
                  </div>
                )}
                {action.busy && !d && (
                  <div className="upload-progress">
                    <progress
                      max={100}
                      value={reading ? undefined : progress}
                    />
                    <span role="status">
                      {reading
                        ? `正在读取目录，已找到 ${readCount} 个文件…`
                        : `${packing ? "正在打包目录" : "正在上传"} ${progress}%`}
                    </span>
                    <Button
                      variant="ghost"
                      size="sm"
                      onPress={() => {
                        abort.current?.abort();
                        worker.current?.terminate();
                      }}
                    >
                      取消
                    </Button>
                  </div>
                )}
              </Card.Content>
            </Card>
            <div className="privacy-note">
              <ShieldCheck size={17} />
              <p>检查通过后仍需你确认发布，正式网址才会切换。</p>
            </div>
            {d?.status === "AWAITING_INPUT" && (
              <Card className="inspection-card">
                <Card.Header>
                  <Card.Title>选择作品的入口</Card.Title>
                </Card.Header>
                <Card.Content>
                  <p>发现多个 HTML 入口。选择要公开的根目录与首页。</p>
                  <select
                    aria-label="HTML 入口"
                    className="app-select"
                    value={selection}
                    onChange={(e) => setSelection(e.target.value)}
                  >
                    {d.report.candidates.map((c, i) => (
                      <option value={i} key={i}>
                        {c.rootDirectory || "/"} / {c.entryPath}
                      </option>
                    ))}
                  </select>
                  <Button
                    isPending={action.busy}
                    onPress={() =>
                      action.run(async () => {
                        const c = d.report.candidates[Number(selection)];
                        await mutate("/deployments/" + d.id + "/selection", {
                          ...c,
                          routingMode: mode,
                          inspectionRevision: d.report.revision,
                        });
                        await deployment.refetch();
                      })
                    }
                  >
                    使用此入口，继续检查
                    <ArrowRight size={17} />
                  </Button>
                </Card.Content>
              </Card>
            )}
            {d?.status === "READY" && (
              <Card className="inspection-card">
                <Card.Header>
                  <Card.Title>检查完成，再确认一下。</Card.Title>
                  <Status value="READY" />
                </Card.Header>
                <Card.Content>
                  <div className="check-row">
                    <span>
                      <Check size={17} />
                      发布入口
                    </span>
                    <code>{d.report.entryPath}</code>
                  </div>
                  <div className="check-row">
                    <span>
                      <Check size={17} />
                      文件与占用
                    </span>
                    <span>
                      {d.fileCount} 个 · {size(d.expandedBytes)}
                    </span>
                  </div>
                  <div className="check-row">
                    <span>路由模式</span>
                    <span>
                      {d.report.routingMode === "spa" ? "单页应用" : "静态网站"}
                    </span>
                  </div>
                  {d.report.warnings?.length > 0 && (
                    <div className="warning-panel">
                      <h3>{d.report.warnings.length} 项需要留意</h3>
                      <ul>
                        {d.report.warnings.map((v, i) => (
                          <li key={i}>
                            <strong>{v.message}</strong>
                            <code>
                              {v.file} → {v.reference}
                            </code>
                          </li>
                        ))}
                      </ul>
                      <label className="checkbox-line">
                        <input
                          type="checkbox"
                          checked={ack}
                          onChange={(e) => setAck(e.target.checked)}
                        />
                        我已阅读以上警告，仍要公开发布。
                      </label>
                    </div>
                  )}
                  <details>
                    <summary>查看文件清单</summary>
                    <div className="file-list">
                      {d.files.slice(0, 100).map((f) => (
                        <div key={f.path}>
                          <span>{f.path}</span>
                          <span>{size(f.size)}</span>
                        </div>
                      ))}
                      {d.fileCount > 100 && (
                        <p>仅展示前 100 个文件，导出可查看完整版本。</p>
                      )}
                    </div>
                  </details>
                </Card.Content>
              </Card>
            )}
          </div>
          <aside className="upload-settings">
            <div className="section-heading">
              <h2>作品信息</h2>
              <Globe2 size={18} />
            </div>
            <TextField
              value={name}
              onChange={setName}
              isRequired
              isDisabled={!!siteId || action.busy}
            >
              <Label>显示名称</Label>
              <Input placeholder="例如：我的设计周报" maxLength={100} />
            </TextField>
            <TextField
              value={slug}
              onChange={setSlug}
              isRequired
              isDisabled={!!siteId || action.busy}
            >
              <Label>站点标识</Label>
              <Input placeholder="my-report" maxLength={40} />
              <p className="field-help">
                用于分享网址，3–40 位小写字母、数字或连字符。
              </p>
            </TextField>
            <div className="mode-field">
              <label htmlFor="routing-mode">路由模式</label>
              <select
                id="routing-mode"
                value={mode}
                onChange={(e) => setMode(e.target.value)}
                disabled={!!d || action.busy}
              >
                <option value="static">静态网站</option>
                <option value="spa">单页应用（SPA）</option>
              </select>
            </div>
            <TextField
              value={message}
              onChange={setMessage}
              isDisabled={!!d || action.busy}
            >
              <Label>版本说明</Label>
              <Input placeholder="这次更新了什么？" maxLength={1000} />
            </TextField>
            <div className="publish-address">
              <span>你的分享地址</span>
              <strong>
                {slug || "my-report"}
                <small>
                  .{system.data?.contentDomain || "drop.douyacn.com"}
                </small>
              </strong>
              <p>后续更新，网址保持不变。</p>
            </div>
            {d?.status === "READY" ? (
              <Button
                fullWidth
                size="lg"
                isPending={action.busy}
                isDisabled={!!d.report.warnings?.length && !ack}
                onPress={publish}
              >
                确认公开发布
                <ArrowUpRight size={18} />
              </Button>
            ) : !d ? (
              <>
                <Button
                  fullWidth
                  size="lg"
                  isPending={action.busy}
                  isDisabled={!!uploadRequirement || action.busy}
                  aria-describedby="upload-requirement"
                  onPress={start}
                >
                  上传并检查
                  <ArrowRight size={18} />
                </Button>
                <p id="upload-requirement" className="field-help" role="status">
                  {!action.busy &&
                    (uploadRequirement ||
                      (!siteId &&
                        "已根据文件名填写作品信息，你可以修改后再上传。"))}
                </p>
              </>
            ) : (
              <p className="settings-note">
                {busyStatuses.includes(d.status)
                  ? "检查完成后可确认发布。"
                  : "文件准备完成后，才会生成公开链接。"}
              </p>
            )}
          </aside>
        </div>
      )}
    </section>
  );
}
