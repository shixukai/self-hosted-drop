import React, { useEffect, useMemo, useRef, useState } from "react";
import { createRoot } from "react-dom/client";
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
  CircleHelp,
  Clock3,
  Code2,
  Copy,
  FileCode2,
  FolderUp,
  Globe2,
  HardDrive,
  KeyRound,
  LayoutGrid,
  List,
  Moon,
  Plus,
  Search,
  ShieldCheck,
  Sun,
  Upload,
  X,
} from "lucide-react";
import "@fontsource-variable/manrope";
import "./style.css";

type View = "sites" | "history" | "tokens" | "system" | "upload";
type Site = {
  slug: string;
  name: string;
  kind: "report" | "api" | "product";
  version: number;
  size: string;
  time: string;
  paused?: boolean;
  note: string;
};
const sites: Site[] = [
  {
    slug: "weekly-report",
    name: "研发周报",
    kind: "report",
    version: 3,
    size: "2.4 MiB",
    time: "今天 10:42",
    note: "更新图表与本周结论",
  },
  {
    slug: "api-observability",
    name: "API 可观测性报告",
    kind: "api",
    version: 2,
    size: "7.8 MiB",
    time: "昨天 16:08",
    note: "补充接口性能分析",
  },
  {
    slug: "product-demo",
    name: "产品交互演示",
    kind: "product",
    version: 1,
    size: "3.0 MiB",
    time: "10 月 12 日",
    paused: true,
    note: "首次发布产品演示",
  },
];
const filters = [
  { id: "all", label: "全部站点", count: 3 },
  { id: "active", label: "已发布", count: 2 },
  { id: "paused", label: "已暂停", count: 1 },
];
const nav = [
  { id: "sites", label: "站点" },
  { id: "history", label: "部署记录" },
  { id: "tokens", label: "API 访问" },
  { id: "system", label: "系统" },
] as const;

function SiteArtwork({ kind }: { kind: Site["kind"] }) {
  if (kind === "report")
    return (
      <div className="art art-report" aria-hidden="true">
        <div className="art-top">
          <span>DOUYA / ENGINEERING</span>
          <span>VOL. 042</span>
        </div>
        <div className="report-title">
          BUILD.
          <br />
          <em>REFLECT.</em>
          <br />
          REPEAT.
        </div>
        <div className="report-stamp">
          WEEK
          <br />
          <strong>42</strong>
        </div>
        <div className="report-lines">
          <i />
          <i />
          <i />
          <i />
          <i />
          <i />
        </div>
        <div className="art-bottom">
          <span>每周一点，持续向前。</span>
          <ArrowUpRight size={16} />
        </div>
      </div>
    );
  if (kind === "api")
    return (
      <div className="art art-api" aria-hidden="true">
        <div className="art-top">
          <span>
            <i /> SYSTEM OBSERVABILITY
          </span>
          <span>LIVE</span>
        </div>
        <div className="api-title">
          Every request.
          <br />
          <span>Understood.</span>
        </div>
        <div className="api-stats">
          <span>
            99.98<small>% uptime</small>
          </span>
          <span>
            42<small>ms latency</small>
          </span>
        </div>
        <svg className="wave" viewBox="0 0 400 100" preserveAspectRatio="none">
          <defs>
            <linearGradient id="wave-fill" x1="0" y1="0" x2="0" y2="1">
              <stop stopColor="#94b7fd" stopOpacity=".35" />
              <stop offset="1" stopColor="#94b7fd" stopOpacity="0" />
            </linearGradient>
          </defs>
          <path
            d="M0 90C20 90 21 75 39 75S62 90 77 65 100 77 119 50 148 77 165 48 185 57 202 35 223 66 239 29 260 60 281 26 300 37 322 13 348 37 370 12 383 16 400 2V100H0Z"
            fill="url(#wave-fill)"
          />
          <path
            d="M0 90C20 90 21 75 39 75S62 90 77 65 100 77 119 50 148 77 165 48 185 57 202 35 223 66 239 29 260 60 281 26 300 37 322 13 348 37 370 12 383 16 400 2"
            fill="none"
            stroke="#a3c0ff"
            strokeWidth="2"
          />
        </svg>
      </div>
    );
  return (
    <div className="art art-product" aria-hidden="true">
      <div className="art-top">
        <span>studio / a little space</span>
        <span>↗</span>
      </div>
      <div className="product-title">
        Make room
        <br />
        for <em>ideas.</em>
      </div>
      <div className="sculpture">
        <div className="shape one" />
        <div className="shape two" />
        <div className="shape three" />
      </div>
      <div className="product-bottom">
        A NEW WAY TO CREATE
        <span>
          Explore the demo <ArrowRight size={12} />
        </span>
      </div>
    </div>
  );
}
function FileStack() {
  return (
    <div className="file-stack" aria-hidden="true">
      <div className="file-paper back">
        <span>ZIP</span>
        <i />
        <i />
      </div>
      <div className="file-paper middle">
        <span>CSS</span>
        <i />
        <i />
      </div>
      <div className="file-paper front">
        <Code2 size={29} />
        <span>index.html</span>
        <div className="paper-rule" />
      </div>
      <div className="stack-arrow">
        <ArrowUpRight size={21} />
      </div>
    </div>
  );
}
function StateChip({
  paused = false,
  children,
}: {
  paused?: boolean;
  children?: React.ReactNode;
}) {
  return (
    <Chip
      size="sm"
      variant="soft"
      color={paused ? "default" : "success"}
      className="state-chip"
    >
      <span className="status-dot" />
      {children || (paused ? "已暂停" : "已发布")}
    </Chip>
  );
}

function App() {
  const [view, setView] = useState<View>("sites");
  const [site, setSite] = useState<Site>(sites[0]);
  const [filter, setFilter] = useState("all");
  const [query, setQuery] = useState("");
  const [layout, setLayout] = useState<"grid" | "list">("grid");
  const [theme, setTheme] = useState(() => {
    try {
      return localStorage.getItem("drop-theme-v2") || "light";
    } catch {
      return "light";
    }
  });
  const [dialog, setDialog] = useState<{ title: string; body: string } | null>(
    null,
  );
  const [toast, setToast] = useState("");
  const [uploadExisting, setUploadExisting] = useState(false);
  const [siteName, setSiteName] = useState("");
  const [slug, setSlug] = useState("");
  const [selectedFiles, setSelectedFiles] = useState<File[]>([]);
  const [inspected, setInspected] = useState(false);
  const [dragging, setDragging] = useState(false);
  const [mode, setMode] = useState("static");
  const fileInput = useRef<HTMLInputElement>(null),
    folderInput = useRef<HTMLInputElement>(null),
    searchRef = useRef<HTMLInputElement>(null);
  const mainRef = useRef<HTMLElement>(null),
    lastTrigger = useRef<HTMLElement | null>(null);
  const visible = useMemo(
    () =>
      sites.filter(
        (s) =>
          (filter === "all" || (filter === "paused" ? s.paused : !s.paused)) &&
          (s.name + " " + s.slug).toLowerCase().includes(query.toLowerCase()),
      ),
    [filter, query],
  );
  useEffect(() => {
    document.documentElement.className = theme;
    document.documentElement.dataset.theme = theme;
    try {
      localStorage.setItem("drop-theme-v2", theme);
    } catch {}
  }, [theme]);
  useEffect(() => {
    if (!toast) return;
    const id = setTimeout(() => setToast(""), 3500);
    return () => clearTimeout(id);
  }, [toast]);
  useEffect(() => {
    function shortcut(e: KeyboardEvent) {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k" && !dialog) {
        e.preventDefault();
        setView("sites");
        requestAnimationFrame(() => searchRef.current?.focus());
      }
    }
    document.addEventListener("keydown", shortcut);
    return () => document.removeEventListener("keydown", shortcut);
  }, [dialog]);
  function navigate(next: View) {
    setView(next);
    setToast("");
    window.scrollTo({ top: 0, behavior: "instant" });
    requestAnimationFrame(() => mainRef.current?.focus());
  }
  function explain(title: string, body: string) {
    lastTrigger.current = document.activeElement as HTMLElement;
    setDialog({ title, body });
  }
  function closeDialog() {
    setDialog(null);
    requestAnimationFrame(() => lastTrigger.current?.focus());
  }
  function startUpload(existing = false) {
    setUploadExisting(existing);
    setSiteName(existing ? site.name : "");
    setSlug(existing ? site.slug : "");
    setSelectedFiles([]);
    setInspected(false);
    setMode("static");
    navigate("upload");
  }
  function openSite(s: Site) {
    setSite(s);
    navigate("history");
  }
  async function copyLink(s: Site) {
    const url = `https://${s.slug}.drop.douyacn.com`;
    try {
      await navigator.clipboard.writeText(url);
      setToast("已复制示例地址，站点尚未实际部署。");
    } catch {
      explain("站点示例地址", url);
    }
  }
  function chooseFiles(files: File[]) {
    if (files.reduce((n, f) => n + f.size, 0) > 200 * 1024 * 1024) {
      explain("文件超过限制", "单次上传最多 200 MiB。请缩小文件后重新选择。");
      return;
    }
    setSelectedFiles(files);
    setInspected(false);
  }
  function publishDemo() {
    if (
      !siteName.trim() ||
      !/^[a-z0-9][a-z0-9-]{1,38}[a-z0-9]$/.test(slug) ||
      slug.startsWith("d-") ||
      ["api", "www", "admin"].includes(slug)
    ) {
      explain(
        "还需要完善站点信息",
        "请填写显示名称和 3–40 位站点标识。标识使用小写字母、数字和连字符，首尾不能是连字符，不能使用系统保留名称。",
      );
      return;
    }
    explain(
      "发布流程演示",
      `正式实现会在文件检查与路由切换完成后提供 https://${slug}.drop.douyacn.com 。此处为设计原型，未上传文件或执行发布。`,
    );
  }
  function renderProject(s: Site) {
    return (
      <Card key={s.slug} className="project-card" data-site={s.slug}>
        <button
          className="project-art"
          onClick={() => openSite(s)}
          aria-label={`查看${s.name}部署历史`}
        >
          <SiteArtwork kind={s.kind} />
          <span className="art-open">
            <ArrowUpRight size={19} />
          </span>
        </button>
        <Card.Content className="project-info">
          <div className="project-heading">
            <button className="project-name" onClick={() => openSite(s)}>
              {s.name}
            </button>
            <StateChip paused={s.paused} />
          </div>
          <button
            className="domain-link"
            onClick={() => copyLink(s)}
            aria-label={`复制${s.name}示例地址`}
          >
            <Globe2 size={13} />
            <span>{s.slug}.drop.douyacn.com</span>
            <Copy size={12} />
          </button>
        </Card.Content>
        <Card.Footer className="project-footer">
          <span>
            <span className="version-tag">v{s.version}</span>
            {s.time}
          </span>
          <span>
            {s.size}
            <Button
              isIconOnly
              size="sm"
              variant="ghost"
              aria-label={`管理${s.name}`}
              onPress={() => openSite(s)}
            >
              <ArrowUpRight size={17} />
            </Button>
          </span>
        </Card.Footer>
      </Card>
    );
  }

  return (
    <>
      <a className="skip-link" href="#main">
        跳到主要内容
      </a>
      <header className="app-header">
        <div className="header-inner">
          <button
            className="brand"
            aria-label="Drop 站点首页"
            onClick={() => navigate("sites")}
          >
            <span className="brand-mark">
              <i />
              <i />
              <i />
            </span>
            <span>
              drop<span className="brand-period">.</span>
            </span>
          </button>
          <nav className="main-nav" aria-label="主导航">
            {nav.map((n) => (
              <Button
                key={n.id}
                variant="ghost"
                className={
                  view === n.id || (view === "upload" && n.id === "sites")
                    ? "nav-active"
                    : ""
                }
                aria-current={view === n.id ? "page" : undefined}
                onPress={() => navigate(n.id)}
              >
                {n.label}
              </Button>
            ))}
          </nav>
          <div className="header-actions">
            <span className="demo-label">
              <span />
              交互原型
            </span>
            <Button
              isIconOnly
              variant="ghost"
              aria-label={theme === "light" ? "切换深色主题" : "切换浅色主题"}
              onPress={() => setTheme(theme === "light" ? "dark" : "light")}
            >
              {theme === "light" ? <Moon size={19} /> : <Sun size={19} />}
            </Button>
            <span className="user-avatar" aria-label="管理员">
              豆
            </span>
          </div>
        </div>
      </header>
      <main id="main" className="main-content" tabIndex={-1} ref={mainRef}>
        {view === "sites" && (
          <section aria-labelledby="sites-title" className="view-enter">
            <div className="page-intro">
              <div>
                <div className="eyebrow">
                  <span />
                  YOUR CREATIVE SPACE
                </div>
                <h1 id="sites-title">
                  你的创作，<span>即刻上线。</span>
                </h1>
                <p>从一份文件，到一个属于你的网址。</p>
              </div>
              <Button
                className="new-site-button"
                size="lg"
                onPress={() => startUpload()}
              >
                <Plus size={19} />
                新建站点
              </Button>
            </div>
            <Tabs
              selectedKey={filter}
              onSelectionChange={(key) => setFilter(String(key))}
              className="site-browser filter-tabs"
            >
              <div className="project-toolbar">
                <Tabs.ListContainer>
                  <Tabs.List aria-label="站点筛选">
                    {filters.map((f) => (
                      <Tabs.Tab key={f.id} id={f.id}>
                        {f.label}
                        <span className="filter-count">{f.count}</span>
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
                    <Input ref={searchRef} placeholder="搜索站点…" />
                    <kbd>⌘ K</kbd>
                  </TextField>
                  <div className="view-switch" aria-label="布局">
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
              {filters.map((f) => (
                <Tabs.Panel key={f.id} id={f.id} className="site-panel">
                  {visible.length ? (
                    <div
                      className={
                        "project-grid " +
                        (layout === "list" ? "project-list" : "")
                      }
                      aria-live="polite"
                    >
                      {visible.map(renderProject)}
                    </div>
                  ) : (
                    <div className="empty-state">
                      <Search size={30} />
                      <h2>没有找到这个站点</h2>
                      <p>试试站点名称或网址中的关键词。</p>
                      <Button
                        variant="secondary"
                        onPress={() => {
                          setQuery("");
                          setFilter("all");
                        }}
                      >
                        清除筛选
                      </Button>
                    </div>
                  )}
                </Tabs.Panel>
              ))}
            </Tabs>
            <div
              className="drop-launch"
              onDragOver={(e) => e.preventDefault()}
              onDrop={(e) => {
                e.preventDefault();
                startUpload();
                chooseFiles(Array.from(e.dataTransfer.files));
              }}
            >
              <FileStack />
              <div className="drop-launch-copy">
                <h2>下一个好想法，拖进来就好。</h2>
                <p>支持 HTML、ZIP 和网站目录。你的内容，你的服务器。</p>
              </div>
              <Button
                variant="secondary"
                className="launch-button"
                onPress={() => startUpload()}
              >
                <Upload size={17} />
                上传文件
                <ArrowRight size={16} />
              </Button>
            </div>
            <div className="workspace-bottom">
              <section
                className="recent-section"
                aria-labelledby="recent-title"
              >
                <div className="section-heading">
                  <h2 id="recent-title">最近部署</h2>
                  <Button
                    variant="ghost"
                    size="sm"
                    onPress={() => navigate("history")}
                  >
                    查看全部
                    <ArrowUpRight size={15} />
                  </Button>
                </div>
                <div className="activity-list">
                  {sites.slice(0, 2).map((s) => (
                    <button
                      className="activity-row"
                      key={s.slug}
                      onClick={() => openSite(s)}
                    >
                      <span className="activity-icon">
                        <CheckCheck size={17} />
                      </span>
                      <span className="activity-copy">
                        <strong>{s.name}</strong>
                        <span>{s.note}</span>
                      </span>
                      <span className="activity-version">v{s.version}</span>
                      <time>{s.time}</time>
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
                  <span className="sample-label">示例</span>
                </div>
                <div className="storage-value">
                  13.2{" "}
                  <small>
                    MiB <span>/ 20 GiB</span>
                  </small>
                </div>
                <div className="storage-track">
                  <i />
                </div>
                <p>包含站点版本与部署历史</p>
              </aside>
            </div>
          </section>
        )}
        {view === "upload" && (
          <section className="view-enter" aria-labelledby="upload-title">
            <Button
              variant="ghost"
              className="back-button"
              onPress={() => navigate(uploadExisting ? "history" : "sites")}
            >
              <ArrowLeft size={16} />
              返回{uploadExisting ? "站点" : "工作区"}
            </Button>
            <div className="page-intro sub-intro">
              <div>
                <div className="eyebrow">A NEW DROP</div>
                <h1 id="upload-title">
                  {uploadExisting
                    ? "给作品，一个新版本。"
                    : "让好想法，被看见。"}
                </h1>
                <p>
                  {uploadExisting
                    ? `更新「${site.name}」，分享地址保持不变。`
                    : "选好文件，剩下的交给 Drop。"}
                </p>
              </div>
              <span className="step-count">
                {inspected ? "03" : selectedFiles.length ? "02" : "01"}{" "}
                <span>/ 03</span>
              </span>
            </div>
            <div className="upload-layout">
              <div>
                <Card className="upload-card">
                  <Card.Content>
                    <div
                      className={"dropzone " + (dragging ? "dragging" : "")}
                      onDragOver={(e) => {
                        e.preventDefault();
                        setDragging(true);
                      }}
                      onDragLeave={() => setDragging(false)}
                      onDrop={(e) => {
                        e.preventDefault();
                        setDragging(false);
                        chooseFiles(Array.from(e.dataTransfer.files));
                      }}
                    >
                      <FileStack />
                      <h2>
                        {dragging ? "松开，放到这里" : "把你的作品拖到这里"}
                      </h2>
                      <p>单个 HTML、ZIP 文件，或已构建的网站目录</p>
                      <div className="choose-actions">
                        <Button onPress={() => fileInput.current?.click()}>
                          <Plus size={17} />
                          选择文件
                        </Button>
                        <Button
                          variant="secondary"
                          onPress={() => folderInput.current?.click()}
                        >
                          <FolderUp size={17} />
                          选择目录
                        </Button>
                      </div>
                      <span className="upload-limit">
                        最大 200 MiB · 原型仅显示名称，文件不会上传
                      </span>
                      <input
                        type="file"
                        ref={fileInput}
                        accept=".html,.htm,.zip"
                        hidden
                        onChange={(e) =>
                          chooseFiles(Array.from(e.target.files || []))
                        }
                      />
                      <input
                        type="file"
                        ref={folderInput}
                        {...({
                          webkitdirectory: "",
                        } as React.InputHTMLAttributes<HTMLInputElement>)}
                        multiple
                        hidden
                        onChange={(e) =>
                          chooseFiles(Array.from(e.target.files || []))
                        }
                      />
                    </div>
                    {selectedFiles.length > 0 && (
                      <div className="file-selection">
                        <span className="file-selection-icon">
                          <FileCode2 size={23} />
                        </span>
                        <div>
                          <strong>
                            {selectedFiles.length === 1
                              ? selectedFiles[0].name
                              : `已选择 ${selectedFiles.length} 个文件`}
                          </strong>
                          <p>
                            {(
                              selectedFiles.reduce((n, f) => n + f.size, 0) /
                              1024 /
                              1024
                            ).toFixed(2)}{" "}
                            MiB · 尚未读取文件内容
                          </p>
                        </div>
                        <Button
                          isIconOnly
                          variant="ghost"
                          aria-label="移除所选文件"
                          onPress={() => {
                            setSelectedFiles([]);
                            setInspected(false);
                            if (fileInput.current) fileInput.current.value = "";
                            if (folderInput.current)
                              folderInput.current.value = "";
                          }}
                        >
                          <X size={17} />
                        </Button>
                      </div>
                    )}
                  </Card.Content>
                </Card>
                <div className="privacy-note">
                  <ShieldCheck size={17} />
                  <p>确认公开发布前，你的文件只会用于准备独立版本。</p>
                </div>
                {inspected && (
                  <Card className="inspection-card">
                    <Card.Header>
                      <Card.Title>一切就绪，再确认一下。</Card.Title>
                      <Chip size="sm" variant="soft">
                        示例检查
                      </Chip>
                    </Card.Header>
                    <Card.Content>
                      <div className="check-row">
                        <span>
                          <Check size={17} />
                          发布入口
                        </span>
                        <code>index.html</code>
                      </div>
                      <div className="check-row">
                        <span>
                          <Check size={17} />
                          文件路径与资源
                        </span>
                        <span>通过</span>
                      </div>
                      <div className="check-row">
                        <span>
                          <Check size={17} />
                          发布模式
                        </span>
                        <span>{mode === "spa" ? "单页应用" : "静态网站"}</span>
                      </div>
                      <p className="inspection-disclaimer">
                        这是检查结果的展示示例，尚未扫描所选文件。
                      </p>
                    </Card.Content>
                  </Card>
                )}
              </div>
              <aside className="upload-settings">
                <div className="section-heading">
                  <h2>作品信息</h2>
                  <Globe2 size={18} />
                </div>
                <TextField value={siteName} onChange={setSiteName} isRequired>
                  <Label>显示名称</Label>
                  <Input placeholder="例如：我的设计周报" maxLength={100} />
                </TextField>
                <TextField
                  value={slug}
                  onChange={setSlug}
                  isDisabled={uploadExisting}
                  isRequired
                >
                  <Label>站点标识</Label>
                  <Input placeholder="my-report" maxLength={40} />
                  <p className="field-help">
                    {slug || "my-report"}.drop.douyacn.com
                  </p>
                </TextField>
                <div className="mode-field">
                  <label htmlFor="routing-mode">路由模式</label>
                  <select
                    id="routing-mode"
                    value={mode}
                    onChange={(e) => setMode(e.target.value)}
                  >
                    <option value="static">静态网站</option>
                    <option value="spa">单页应用（SPA）</option>
                  </select>
                </div>
                <div className="publish-address">
                  <span>你的分享地址</span>
                  <strong>
                    {slug || "my-report"}
                    <small>.drop.douyacn.com</small>
                  </strong>
                  <p>后续更新，网址保持不变。</p>
                </div>
                {inspected ? (
                  <Button fullWidth size="lg" onPress={publishDemo}>
                    确认公开发布
                    <ArrowUpRight size={18} />
                  </Button>
                ) : (
                  <Button
                    fullWidth
                    size="lg"
                    onPress={() => setInspected(true)}
                  >
                    查看检查示例
                    <ArrowRight size={18} />
                  </Button>
                )}
                <p className="settings-note">设计演示 · 不会执行真实发布</p>
              </aside>
            </div>
          </section>
        )}
        {view === "history" && (
          <section className="view-enter" aria-labelledby="history-title">
            <Button
              variant="ghost"
              className="back-button"
              onPress={() => navigate("sites")}
            >
              <ArrowLeft size={16} />
              全部站点
            </Button>
            <div className="page-intro sub-intro">
              <div>
                <div className="eyebrow">PROJECT / DEPLOYMENTS</div>
                <h1 id="history-title">{site.name}</h1>
                <button
                  className="detail-domain"
                  onClick={() => copyLink(site)}
                >
                  {site.slug}.drop.douyacn.com
                  <Copy size={15} />
                </button>
              </div>
              <Button size="lg" onPress={() => startUpload(true)}>
                <Upload size={18} />
                上传新版本
              </Button>
            </div>
            <div className="history-layout">
              <Card className="current-release">
                <div className="release-art">
                  <SiteArtwork kind={site.kind} />
                </div>
                <Card.Content>
                  <div className="section-heading">
                    <h2>{site.paused ? "最近正式版本" : "当前正式版本"}</h2>
                    <StateChip paused={site.paused} />
                  </div>
                  <h3>
                    v{site.version}
                    <span>{site.note}</span>
                  </h3>
                  <p>
                    {site.time} · {site.size}
                  </p>
                  <Button
                    variant="secondary"
                    fullWidth
                    isDisabled={site.paused}
                    onPress={() =>
                      explain(
                        "受控预览",
                        "正式实现将生成与此部署绑定的一次性预览入口。当前为示例数据，未建立真实预览授权。",
                      )
                    }
                  >
                    <Globe2 size={17} />
                    受控预览
                    <ArrowUpRight size={16} />
                  </Button>
                </Card.Content>
              </Card>
              <section className="release-history" aria-label="版本时间线">
                <div className="section-heading">
                  <h2>每次发布，都有迹可循。</h2>
                  <span className="muted">{site.version} 个版本</span>
                </div>
                {Array.from(
                  { length: site.version },
                  (_, i) => site.version - i,
                ).map((v) => (
                  <article className="release-row" key={v}>
                    <span
                      className={
                        "release-dot " + (v === site.version ? "current" : "")
                      }
                    >
                      {v === site.version ? (
                        <Check size={16} />
                      ) : (
                        <Clock3 size={15} />
                      )}
                    </span>
                    <div className="release-body">
                      <div className="release-title">
                        <strong>Version {v}</strong>
                        {v === site.version && (
                          <Chip variant="soft" size="sm">
                            {site.paused ? "最近正式" : "当前正式"}
                          </Chip>
                        )}
                      </div>
                      <p>
                        {v === site.version
                          ? site.note
                          : v === 1
                            ? "首次发布"
                            : "调整内容与静态资源"}
                      </p>
                      <div className="release-meta">
                        <code>
                          {v === 3
                            ? "a8c2f9d"
                            : v === 2
                              ? "39b6e10"
                              : "f0724b1"}
                        </code>
                        <span>
                          {v === site.version ? site.time : "10 月 12 日"}
                        </span>
                        <span>管理员</span>
                      </div>
                    </div>
                    {v !== site.version && !site.paused && (
                      <Button
                        variant="secondary"
                        size="sm"
                        onPress={() =>
                          explain(
                            `回滚到 Version ${v}`,
                            `正式实现会将「${site.name}」的固定网址切换到 Version ${v}，完成内容校验后才报告成功。原型不会修改任何线上内容。`,
                          )
                        }
                      >
                        回滚
                        <ArrowLeft size={14} />
                      </Button>
                    )}
                  </article>
                ))}
              </section>
            </div>
          </section>
        )}
        {view === "tokens" && (
          <section className="view-enter" aria-labelledby="tokens-title">
            <div className="page-intro">
              <div>
                <div className="eyebrow">BUILT FOR YOUR WORKFLOW</div>
                <h1 id="tokens-title">
                  让发布，<span>自然发生。</span>
                </h1>
                <p>连接你的 Agent 和脚本，让好内容自动上线。</p>
              </div>
              <Button
                size="lg"
                onPress={() =>
                  explain(
                    "创建 API Token",
                    "正式实现可选择项目、操作范围与有效期，原始密钥只显示一次。此原型不会生成真实凭据。",
                  )
                }
              >
                <Plus size={18} />
                新建 Token
              </Button>
            </div>
            <Card className="token-card">
              <Card.Content>
                <span className="token-symbol">
                  <KeyRound size={26} />
                </span>
                <div>
                  <h2>本地报告发布脚本</h2>
                  <p>研发周报 · 读取、上传、发布</p>
                  <code>
                    drop_•••••••••••• <span>示例凭据</span>
                  </code>
                </div>
                <StateChip>有效</StateChip>
                <Button
                  variant="secondary"
                  onPress={() =>
                    explain(
                      "撤销 Token",
                      "正式实现会立即拒绝该 Token 的后续请求。原型未连接后台，不会撤销真实凭据。",
                    )
                  }
                >
                  撤销
                </Button>
              </Card.Content>
            </Card>
            <div className="token-hint">
              <ShieldCheck size={20} />
              <div>
                <h3>只授予需要的权限</h3>
                <p>发布 Token 默认不包含删除站点或修改系统配置的权限。</p>
              </div>
            </div>
          </section>
        )}
        {view === "system" && (
          <section className="view-enter" aria-labelledby="system-title">
            <div className="page-intro">
              <div>
                <div className="eyebrow">YOUR SERVER. YOUR SPACE.</div>
                <h1 id="system-title">
                  一切，<span>尽在掌握。</span>
                </h1>
                <p>存储、任务和恢复能力，在这里清楚可见。</p>
              </div>
              <Chip variant="soft">以下为示例状态</Chip>
            </div>
            <div className="system-grid">
              <Card>
                <Card.Content>
                  <HardDrive size={24} />
                  <h2>产物存储</h2>
                  <div className="system-number">
                    13.2 <small>MiB</small>
                  </div>
                  <p>配额 20 GiB，包含全部保留版本</p>
                </Card.Content>
              </Card>
              <Card>
                <Card.Content>
                  <Clock3 size={24} />
                  <h2>任务队列</h2>
                  <div className="system-number">
                    0 <small>个待处理</small>
                  </div>
                  <p>最多同时处理 2 个部署</p>
                </Card.Content>
              </Card>
              <Card>
                <Card.Content>
                  <ShieldCheck size={24} />
                  <h2>独立备份</h2>
                  <Chip variant="soft" color="warning">
                    待配置与验证
                  </Chip>
                  <p>恢复演练通过后，才标记可恢复</p>
                </Card.Content>
              </Card>
            </div>
            <Card className="system-checks">
              <Card.Header>
                <Card.Title>运行检查</Card.Title>
                <Card.Description>
                  状态呈现示例，尚未连接真实服务。
                </Card.Description>
              </Card.Header>
              <Card.Content>
                {[
                  ["静态文件服务", "通过"],
                  ["发布数据持久化", "通过"],
                  ["外部 HTTPS 入口", "待验证"],
                  ["备份与恢复演练", "待完成"],
                ].map(([label, value]) => (
                  <div className="system-check-row" key={label}>
                    <span>{label}</span>
                    <Chip
                      variant="soft"
                      size="sm"
                      color={value === "通过" ? "success" : "warning"}
                    >
                      {value}
                    </Chip>
                  </div>
                ))}
              </Card.Content>
            </Card>
          </section>
        )}
        <footer className="app-footer">
          <span>
            <span className="footer-mark">drop.</span>内容属于你，可能性也是。
          </span>
          <span>
            示例数据 · 无后台连接<span className="footer-separator">/</span>
            <a
              href="https://heroui.com/en/docs/react/components"
              target="_blank"
              rel="noreferrer"
            >
              Built with HeroUI <ArrowUpRight size={12} />
            </a>
          </span>
        </footer>
      </main>
      <Modal.Backdrop
        isOpen={!!dialog}
        onOpenChange={(open) => {
          if (!open) closeDialog();
        }}
        variant="blur"
      >
        <Modal.Container placement="center">
          <Modal.Dialog>
            <Modal.CloseTrigger />
            <Modal.Header>
              <Modal.Icon>
                <CircleHelp size={24} />
              </Modal.Icon>
              <Modal.Heading>{dialog?.title}</Modal.Heading>
            </Modal.Header>
            <Modal.Body>
              <p>{dialog?.body}</p>
            </Modal.Body>
            <Modal.Footer>
              <Button onPress={closeDialog}>知道了</Button>
            </Modal.Footer>
          </Modal.Dialog>
        </Modal.Container>
      </Modal.Backdrop>
      <div
        className={"app-toast " + (toast ? "show" : "")}
        role="status"
        aria-live="polite"
      >
        <Check size={17} />
        {toast}
      </div>
    </>
  );
}

createRoot(document.getElementById("root")!).render(<App />);
