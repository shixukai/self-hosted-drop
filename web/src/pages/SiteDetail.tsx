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
export default function SiteDetail({
  onCopy,
}: {
  onCopy: (s: string) => void;
}) {
  const { id = "" } = useParams();
  const nav = useNavigate(),
    qc = useQueryClient();
  const site = useQuery({
    queryKey: ["site", id],
    queryFn: () => api<Site>("/sites/" + id),
  });
  const publications = useQuery({
    queryKey: ["publications", id],
    queryFn: () =>
      api<Page<Publication>>("/sites/" + id + "/publications?limit=10"),
    refetchInterval: (q) =>
      q.state.data?.items.some((p) =>
        ["PENDING", "APPLYING", "RECOVERING"].includes(p.status),
      )
        ? 1500
        : false,
  });
  const list = useInfiniteQuery({
    queryKey: ["deployments", id],
    initialPageParam: "",
    queryFn: ({ pageParam }) =>
      api<Page<Deployment>>(
        "/sites/" +
          id +
          "/deployments?limit=20" +
          (pageParam ? "&cursor=" + pageParam : ""),
      ),
    getNextPageParam: (last) => last.nextCursor || undefined,
    refetchInterval: (q) =>
      q.state.data?.pages.some((p) =>
        p.items.some((d) => busyStatuses.includes(d.status)),
      )
        ? 2000
        : false,
  });
  const [confirm, setConfirm] = useState<{
      op: string;
      deployment?: Deployment;
    } | null>(null),
    [edit, setEdit] = useState(false),
    [name, setName] = useState(""),
    [description, setDescription] = useState(""),
    [preview, setPreview] = useState<{ id: string; url: string } | null>(null);
  const action = useAction();
  const s = site.data;
  const deployments = list.data?.pages.flatMap((p) => p.items) || [];
  async function refresh() {
    await Promise.all([
      qc.invalidateQueries({ queryKey: ["sites"] }),
      site.refetch(),
      publications.refetch(),
      list.refetch(),
      qc.invalidateQueries({ queryKey: ["system"] }),
    ]);
  }
  if (site.isPending) return <Loading />;
  if (site.error || !s) return <ErrorNote error={site.error || "站点不存在"} />;
  const current = deployments.find((d) => d.id === s.appliedDeploymentId);
  async function runOperation() {
    if (!confirm || !s) return;
    const { op, deployment: d } = confirm;
    if (op === "pin") {
      await mutate(
        "/deployments/" + d!.id + "/retention",
        { pinned: !d!.pinned },
        "PATCH",
      );
      await refresh();
      return;
    }
    let p: Publication;
    if (op === "purge") {
      await mutate("/sites/" + id + "/purge", {
        confirmSlug: s.slug,
        expectedGeneration: s.generation,
      });
      await refresh();
      return;
    }
    if (d)
      p = await mutate("/sites/" + id + "/publications", {
        deploymentId: d.id,
        action: op === "rollback" ? "ROLLBACK" : "PUBLISH",
        expectedGeneration: s.generation,
        inspectionReportHash: d.report.inspectionReportHash,
        acknowledgeWarnings: true,
      });
    else
      p = await mutate(
        "/sites/" + id + (op === "delete" ? "" : "/" + op),
        {
          expectedGeneration: s.generation,
          confirmSlug: op === "delete" ? s.slug : undefined,
        },
        op === "delete" ? "DELETE" : "POST",
      );
    await awaitPublication(p.id);
    await refresh();
  }
  return (
    <section className="view-enter">
      <Button
        variant="ghost"
        className="back-button"
        onPress={() => nav(s.status === "DELETED" ? "/trash" : "/")}
      >
        <ArrowLeft size={16} />
        全部站点
      </Button>
      <div className="page-intro sub-intro">
        <div>
          <div className="eyebrow">PROJECT / DEPLOYMENTS</div>
          <h1>{s.name}</h1>
          <button className="detail-domain" onClick={() => onCopy(s.url)}>
            {s.url.replace(/^https?:\/\//, "")}
            <Copy size={15} />
          </button>
        </div>
        {s.status !== "DELETED" && (
          <Button size="lg" onPress={() => nav("/upload?site=" + id)}>
            <Upload size={18} />
            上传新版本
          </Button>
        )}
      </div>
      <div className="site-actions">
        <Status value={s.status} />
        <Button
          size="sm"
          variant="ghost"
          onPress={() => {
            setName(s.name);
            setDescription(s.description);
            setEdit(true);
          }}
        >
          <Settings2 size={15} />
          编辑信息
        </Button>
        {s.status === "ACTIVE" && (
          <Button
            variant="ghost"
            size="sm"
            onPress={() => setConfirm({ op: "pause" })}
          >
            <Pause size={15} />
            暂停
          </Button>
        )}
        {s.status === "PAUSED" && s.appliedDeploymentId && (
          <Button
            variant="ghost"
            size="sm"
            onPress={() => setConfirm({ op: "resume" })}
          >
            <Play size={15} />
            恢复服务
          </Button>
        )}
        {s.status === "DELETED" ? (
          <>
            <Button
              variant="ghost"
              size="sm"
              onPress={() => setConfirm({ op: "restore" })}
            >
              恢复到工作区
            </Button>
            <Button
              variant="ghost"
              size="sm"
              onPress={() => setConfirm({ op: "purge" })}
            >
              <Trash2 size={15} />
              永久回收文件
            </Button>
          </>
        ) : (
          <Button
            variant="ghost"
            size="sm"
            onPress={() => setConfirm({ op: "delete" })}
          >
            <Trash2 size={15} />
            移入回收站
          </Button>
        )}
      </div>
      <ErrorNote error={action.error || list.error} />
      {s.generation !== s.appliedGeneration && (
        <div className="info-note">
          最近一次操作尚未生效或已失败。当前公开内容以已生效版本为准。
        </div>
      )}
      <div className="history-layout">
        <Card className="current-release">
          <div className="release-art">
            <SiteArtwork kind={siteKind(s.slug)} />
          </div>
          <Card.Content>
            <div className="section-heading">
              <h2>{s.status === "ACTIVE" ? "当前正式版本" : "最近正式版本"}</h2>
              <Status value={s.status} />
            </div>
            <h3>
              {s.appliedDeploymentId
                ? s.appliedDeploymentId.slice(0, 8)
                : "尚未发布"}
            </h3>
            <p>
              {current?.metadata.message ||
                s.description ||
                "上传一个版本，开始发布。"}
            </p>
            {s.status === "ACTIVE" && (
              <a
                className="button button--secondary"
                href={s.url}
                target="_blank"
                rel="noreferrer"
              >
                <Globe2 size={17} />
                访问公开站点
                <ArrowUpRight size={16} />
              </a>
            )}
          </Card.Content>
        </Card>
        <section className="release-history">
          <div className="section-heading">
            <h2>每次发布，都有迹可循。</h2>
            <Button
              isIconOnly
              variant="ghost"
              size="sm"
              aria-label="刷新部署记录"
              onPress={() => refresh()}
            >
              <RefreshCw size={16} />
            </Button>
          </div>
          {list.isPending ? (
            <Loading />
          ) : deployments.length ? (
            deployments.map((d, i) => (
              <article className="release-row" key={d.id}>
                <span
                  className={
                    "release-dot " +
                    (s.appliedDeploymentId === d.id ? "current" : "")
                  }
                >
                  {s.appliedDeploymentId === d.id ? (
                    <Check size={16} />
                  ) : (
                    <Clock3 size={15} />
                  )}
                </span>
                <div className="release-body">
                  <div className="release-title">
                    <strong>{d.metadata.message || d.filename}</strong>
                    <Status value={d.status} />
                    {s.appliedDeploymentId === d.id && (
                      <Chip size="sm" variant="soft">
                        当前正式
                      </Chip>
                    )}
                    {d.pinned && <Pin size={14} />}
                  </div>
                  <p>
                    {date(d.createdAt)} · {d.fileCount} 个文件 ·{" "}
                    {size(d.expandedBytes)}
                  </p>
                  <div className="release-meta">
                    <code>{d.id.slice(0, 8)}</code>
                    {d.artifactState === "PURGED" && <span>产物已回收</span>}
                  </div>
                  {d.errorMessage && <ErrorNote error={d.errorMessage} />}
                  <div className="version-actions">
                    {busyStatuses.includes(d.status) ||
                    d.status === "AWAITING_INPUT" ? (
                      <Button
                        size="sm"
                        variant="secondary"
                        onPress={() =>
                          nav("/upload?site=" + id + "&deployment=" + d.id)
                        }
                      >
                        查看检查进度
                      </Button>
                    ) : null}
                    {d.status === "READY" &&
                      d.artifactState === "AVAILABLE" && (
                        <>
                          <Button
                            size="sm"
                            variant="secondary"
                            isDisabled={
                              s.status === "PAUSED" ||
                              s.status === "DELETED" ||
                              action.busy
                            }
                            onPress={() =>
                              action.run(async () => {
                                const grant = await mutate<{
                                  id: string;
                                  url: string;
                                }>(
                                  "/deployments/" + d.id + "/preview-grants",
                                  {},
                                );
                                setPreview(grant);
                              })
                            }
                          >
                            受控预览
                            <ArrowUpRight size={14} />
                          </Button>
                          {s.appliedDeploymentId !== d.id &&
                            s.status !== "DELETED" && (
                              <Button
                                size="sm"
                                variant="secondary"
                                onPress={() =>
                                  d.report.warnings?.length
                                    ? nav(
                                        "/upload?site=" +
                                          id +
                                          "&deployment=" +
                                          d.id,
                                      )
                                    : setConfirm({
                                        op: s.appliedDeploymentId
                                          ? "rollback"
                                          : "publish",
                                        deployment: d,
                                      })
                                }
                              >
                                {s.appliedDeploymentId
                                  ? "回滚到此版本"
                                  : "发布此版本"}
                              </Button>
                            )}
                          <Button
                            size="sm"
                            variant="ghost"
                            onPress={() =>
                              setConfirm({ op: "pin", deployment: d })
                            }
                          >
                            {d.pinned ? "解除固定" : "固定保留"}
                          </Button>
                          <a
                            className="text-action"
                            href={"/api/v1/deployments/" + d.id + "/export"}
                          >
                            <Download size={14} />
                            导出
                          </a>
                        </>
                      )}
                  </div>
                </div>
              </article>
            ))
          ) : (
            <div className="empty-state">
              <FileCode2 size={28} />
              <h2>这里是作品的起点。</h2>
              <p>上传后，每个版本都会保存在这里。</p>
            </div>
          )}
          {list.hasNextPage && (
            <Button variant="secondary" onPress={() => list.fetchNextPage()}>
              加载更多
            </Button>
          )}
        </section>
      </div>
      <section className="publication-log">
        <div className="section-heading">
          <h2>发布与访问记录</h2>
          <span>最近 10 次</span>
        </div>
        <ErrorNote error={publications.error} />
        {publications.data?.items.map((p) => (
          <article className="publication-row" key={p.id}>
            <div>
              <strong>
                {{
                  PUBLISH: "发布",
                  ROLLBACK: "回滚",
                  PAUSE: "暂停",
                  RESUME: "恢复服务",
                  DELETE: "移入回收站",
                  RESTORE: "恢复站点",
                }[p.action] || p.action}{" "}
                · 第 {p.targetGeneration} 次操作
              </strong>
              <small>{date(p.createdAt)}</small>
              {p.errorMessage && <p>{p.errorMessage}</p>}
            </div>
            <Status value={p.status} />
          </article>
        ))}
      </section>
      {confirm && (
        <Confirm
          title={
            {
              pause: "暂停公开访问？",
              resume: "恢复公开访问？",
              delete: "将站点移入回收站？",
              restore: "恢复这个站点？",
              purge: "永久回收站点文件？",
              rollback: "回滚到这个版本？",
              publish: "公开发布这个版本？",
              pin: confirm.deployment?.pinned
                ? "解除固定保留？"
                : "固定保留这个版本？",
            }[confirm.op] || "确认操作"
          }
          body={
            {
              pause: "所有公开资源将暂时不可访问，现有预览授权会失效。",
              resume: "验证最近正式版本后，恢复原有公开网址。",
              delete: "站点会停止公开访问，进入回收期；原地址继续保留。",
              restore: "恢复后保持暂停，你可以再决定是否恢复服务。",
              purge:
                "符合保护规则的版本文件将永久删除，不能恢复；当前正在使用或受保护的文件会保留。",
              rollback: "固定网址将切换到这个版本；当前版本保留在历史记录中。",
              publish: "检查和路由切换完成后，任何知道地址的人都可以访问。",
              pin: "固定版本不会被自动保留策略回收。",
            }[confirm.op] || ""
          }
          typed={["delete", "purge"].includes(confirm.op) ? s.slug : undefined}
          danger={["delete", "purge"].includes(confirm.op)}
          onClose={() => setConfirm(null)}
          onConfirm={runOperation}
        />
      )}
      {edit && (
        <Modal.Backdrop
          isOpen
          onOpenChange={(o) => {
            if (!o) setEdit(false);
          }}
        >
          <Modal.Container>
            <Modal.Dialog>
              <Modal.CloseTrigger />
              <Modal.Header>
                <Modal.Heading>作品信息</Modal.Heading>
              </Modal.Header>
              <Modal.Body>
                <TextField value={name} onChange={setName}>
                  <Label>显示名称</Label>
                  <Input maxLength={100} />
                </TextField>
                <TextField value={description} onChange={setDescription}>
                  <Label>描述</Label>
                  <Input maxLength={2000} />
                </TextField>
                <p>修改名称不影响固定网址。</p>
                <ErrorNote error={action.error} />
              </Modal.Body>
              <Modal.Footer>
                <Button
                  isPending={action.busy}
                  onPress={() =>
                    action.run(async () => {
                      await mutate(
                        "/sites/" + id,
                        { name, description },
                        "PATCH",
                      );
                      await refresh();
                      setEdit(false);
                    })
                  }
                >
                  保存
                </Button>
              </Modal.Footer>
            </Modal.Dialog>
          </Modal.Container>
        </Modal.Backdrop>
      )}
      {preview && (
        <Modal.Backdrop
          isOpen
          onOpenChange={(o) => {
            if (!o) setPreview(null);
          }}
        >
          <Modal.Container>
            <Modal.Dialog>
              <Modal.CloseTrigger />
              <Modal.Header>
                <Modal.Heading>预览已准备好</Modal.Heading>
              </Modal.Header>
              <Modal.Body>
                <p>
                  链接只能兑换一次，预览授权一小时后过期。暂停或删除站点会立即撤销授权。
                </p>
                <a
                  className="button button--primary"
                  href={preview.url}
                  target="_blank"
                  rel="noreferrer"
                >
                  打开受控预览
                  <ArrowUpRight size={17} />
                </a>
                <Button
                  variant="secondary"
                  onPress={() =>
                    action.run(async () => {
                      await api("/preview-grants/" + preview.id, {
                        method: "DELETE",
                      });
                      setPreview(null);
                    })
                  }
                >
                  撤销此授权
                </Button>
                <ErrorNote error={action.error} />
              </Modal.Body>
            </Modal.Dialog>
          </Modal.Container>
        </Modal.Backdrop>
      )}
    </section>
  );
}
