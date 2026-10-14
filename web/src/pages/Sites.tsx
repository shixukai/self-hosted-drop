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
export default function Sites({
  trash = false,
  onCopy,
}: {
  trash?: boolean;
  onCopy: (s: string) => void;
}) {
  const nav = useNavigate(),
    qc = useQueryClient();
  const [filter, setFilter] = useState("all"),
    [query, setQuery] = useState(""),
    [search, setSearch] = useState(""),
    [layout, setLayout] = useState("grid");
  const [confirm, setConfirm] = useState<{ site: Site; op: string } | null>(
    null,
  );
  const system = useSystem();
  useEffect(() => {
    const t = setTimeout(() => setSearch(query), 200);
    return () => clearTimeout(t);
  }, [query]);
  const status = trash
    ? "DELETED"
    : filter === "active"
      ? "ACTIVE"
      : filter === "paused"
        ? "PAUSED"
        : "";
  const listing = useInfiniteQuery({
    queryKey: ["sites", "list", status, search],
    initialPageParam: "",
    queryFn: ({ pageParam }) =>
      api<Page<Site>>(
        "/sites?limit=30&status=" +
          status +
          "&q=" +
          encodeURIComponent(search) +
          (pageParam ? "&cursor=" + pageParam : ""),
      ),
    getNextPageParam: (last) => last.nextCursor || undefined,
  });
  const sites = listing.data?.pages.flatMap((x) => x.items) || [];
  return (
    <section aria-labelledby="sites-title" className="view-enter">
      <div className="page-intro">
        <div>
          <div className="eyebrow">
            <span />
            YOUR CREATIVE SPACE
          </div>
          <h1 id="sites-title">
            {trash ? (
              "给作品，留一次机会。"
            ) : (
              <>
                你的创作，<span>即刻上线。</span>
              </>
            )}
          </h1>
          <p>
            {trash
              ? "恢复后保持暂停，由你决定何时重新公开。"
              : "从一份文件，到一个属于你的网址。"}
          </p>
        </div>
        <Button
          className="new-site-button"
          size="lg"
          onPress={() => nav(trash ? "/" : "/upload")}
        >
          {trash ? <ArrowLeft size={18} /> : <Plus size={19} />}{" "}
          {trash ? "全部站点" : "新建站点"}
        </Button>
      </div>
      <Tabs
        selectedKey={filter}
        onSelectionChange={(k) => setFilter(String(k))}
        className="site-browser filter-tabs"
      >
        <div className="project-toolbar">
          <Tabs.ListContainer>
            <Tabs.List aria-label="站点筛选">
              {(trash
                ? [["all", "回收站"]]
                : [
                    ["all", "全部站点"],
                    ["active", "已发布"],
                    ["paused", "已暂停"],
                  ]
              ).map(([id, label]) => (
                <Tabs.Tab key={id} id={id}>
                  {label}
                  <Tabs.Indicator />
                </Tabs.Tab>
              ))}
            </Tabs.List>
          </Tabs.ListContainer>
          <div className="toolbar-right">
            <TextField
              aria-label="搜索站点"
              value={query}
              onChange={setQuery}
              className="search-field"
            >
              <Search size={17} />
              <Input placeholder="搜索站点…" />
              <kbd>⌘ K</kbd>
            </TextField>
            <div className="view-switch">
              <Button
                isIconOnly
                size="sm"
                variant="ghost"
                aria-label="卡片视图"
                aria-pressed={layout === "grid"}
                onPress={() => setLayout("grid")}
              >
                <LayoutGrid size={17} />
              </Button>
              <Button
                isIconOnly
                size="sm"
                variant="ghost"
                aria-label="列表视图"
                aria-pressed={layout === "list"}
                onPress={() => setLayout("list")}
              >
                <List size={19} />
              </Button>
            </div>
          </div>
        </div>
        {["all", "active", "paused"].map((k) => (
          <Tabs.Panel id={k} key={k} className="site-panel">
            {listing.isPending ? (
              <Loading />
            ) : listing.error ? (
              <ErrorNote error={listing.error} />
            ) : sites.length ? (
              <div
                className={
                  "project-grid " + (layout === "list" ? "project-list" : "")
                }
              >
                {sites.map((s) => (
                  <Card key={s.id} className="project-card">
                    <button
                      className="project-art"
                      onClick={() => nav("/sites/" + s.id)}
                      aria-label={"查看" + s.name}
                    >
                      <SiteArtwork kind={siteKind(s.slug)} />
                      <span className="art-open">
                        <ArrowUpRight size={19} />
                      </span>
                    </button>
                    <Card.Content className="project-info">
                      <div className="project-heading">
                        <button
                          className="project-name"
                          onClick={() => nav("/sites/" + s.id)}
                        >
                          {s.name}
                        </button>
                        <Status value={s.status} />
                      </div>
                      <button
                        className="domain-link"
                        onClick={() => onCopy(s.url)}
                      >
                        <Globe2 size={13} />
                        <span>{s.url.replace(/^https?:\/\//, "")}</span>
                        <Copy size={12} />
                      </button>
                    </Card.Content>
                    <Card.Footer className="project-footer">
                      <span>
                        <span className="version-tag">
                          {s.appliedDeploymentId
                            ? s.appliedDeploymentId.slice(0, 8)
                            : "草稿"}
                        </span>
                        {date(s.updatedAt)}
                      </span>
                      <span>
                        {size(s.bytes)}
                        {trash ? (
                          <Button
                            size="sm"
                            variant="ghost"
                            onPress={() =>
                              setConfirm({ site: s, op: "restore" })
                            }
                          >
                            恢复
                          </Button>
                        ) : (
                          <Button
                            isIconOnly
                            size="sm"
                            variant="ghost"
                            aria-label={"管理" + s.name}
                            onPress={() => nav("/sites/" + s.id)}
                          >
                            <ArrowUpRight size={17} />
                          </Button>
                        )}
                      </span>
                    </Card.Footer>
                  </Card>
                ))}
              </div>
            ) : (
              <div className="empty-state">
                <Globe2 size={32} />
                <h2>
                  {search
                    ? "没有找到这个站点"
                    : trash
                      ? "回收站是空的"
                      : "把第一个好想法，放到这里。"}
                </h2>
                <p>
                  {search
                    ? "试试名称或网址中的关键词。"
                    : trash
                      ? "已删除的站点会暂时保留在这里。"
                      : "上传一份 HTML 或 ZIP，就可以开始。"}
                </p>
                <Button
                  variant="secondary"
                  onPress={() => {
                    if (search || filter !== "all") {
                      setQuery("");
                      setFilter("all");
                    } else nav(trash ? "/" : "/upload");
                  }}
                >
                  {search || filter !== "all"
                    ? "清除筛选"
                    : trash
                      ? "返回工作区"
                      : "新建站点"}
                </Button>
              </div>
            )}
          </Tabs.Panel>
        ))}
      </Tabs>
      {listing.hasNextPage && (
        <Button
          variant="secondary"
          onPress={() => listing.fetchNextPage()}
          isPending={listing.isFetchingNextPage}
        >
          加载更多
        </Button>
      )}
      {!trash && (
        <>
          <div className="drop-launch">
            <FileStack />
            <div className="drop-launch-copy">
              <h2>下一个好想法，拖进来就好。</h2>
              <p>支持 HTML、ZIP 和网站目录。你的内容，你的服务器。</p>
            </div>
            <Button
              variant="secondary"
              className="launch-button"
              onPress={() => nav("/upload")}
            >
              <Upload size={17} />
              上传文件
              <ArrowRight size={16} />
            </Button>
          </div>
          <div className="workspace-bottom">
            <section className="recent-section">
              <div className="section-heading">
                <h2>最近更新</h2>
                <Button
                  variant="ghost"
                  size="sm"
                  onPress={() => nav("/deployments")}
                >
                  部署记录
                  <ArrowUpRight size={15} />
                </Button>
              </div>
              <div className="activity-list">
                {sites.slice(0, 3).map((s) => (
                  <button
                    className="activity-row"
                    key={s.id}
                    onClick={() => nav("/sites/" + s.id)}
                  >
                    <span className="activity-icon">
                      <CheckCheck size={17} />
                    </span>
                    <span className="activity-copy">
                      <strong>{s.name}</strong>
                      <span>{s.description || messages[s.status]}</span>
                    </span>
                    <span className="activity-version">
                      {s.deploymentCount} 个版本
                    </span>
                    <time>{date(s.updatedAt)}</time>
                    <ArrowUpRight size={16} />
                  </button>
                ))}
              </div>
            </section>
            <aside className="storage-card">
              <div className="storage-title">
                <span>
                  <HardDrive size={17} />
                  存储空间
                </span>
              </div>
              <div className="storage-value">
                {size(system.data?.usedBytes)}
                <small> / {size(system.data?.quotaBytes)}</small>
              </div>
              <div className="storage-track">
                <i
                  style={{
                    width: system.data
                      ? Math.max(
                          0.5,
                          (system.data.usedBytes / system.data.quotaBytes) *
                            100,
                        ) + "%"
                      : "0%",
                  }}
                />
              </div>
              <p>包含保留版本；预留 {size(system.data?.reservedBytes)}</p>
            </aside>
          </div>
        </>
      )}
      {confirm && (
        <Confirm
          title="恢复这个站点？"
          body="恢复后站点保持暂停，原有链接继续保留。"
          onClose={() => setConfirm(null)}
          onConfirm={async () => {
            const p = await mutate<Publication>(
              "/sites/" + confirm.site.id + "/restore",
              { expectedGeneration: confirm.site.generation },
            );
            await awaitPublication(p.id);
            await qc.invalidateQueries({ queryKey: ["sites"] });
          }}
        />
      )}
    </section>
  );
}
