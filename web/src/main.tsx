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
} from "./api";
import { FileStack, SiteArtwork } from "./artwork";
import "@fontsource-variable/manrope";
import "./style.css";
import "./application.css";

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
} from "./components";
const Sites = React.lazy(() => import("./pages/Sites"));
const UploadPage = React.lazy(() => import("./pages/UploadPage"));
const SiteDetail = React.lazy(() => import("./pages/SiteDetail"));
const History = React.lazy(() => import("./pages/History"));
const Tokens = React.lazy(() => import("./pages/Tokens"));
const SystemPage = React.lazy(() => import("./pages/SystemPage"));
const client = new QueryClient({
  defaultOptions: {
    queries: {
      retry: (n, e) => n < 1 && !(e instanceof APIError && e.status < 500),
      staleTime: 10000,
      refetchOnWindowFocus: true,
    },
  },
});
function Auth() {
  const nav = useNavigate();
  const qc = useQueryClient();
  const status = useQuery({
    queryKey: ["setup"],
    queryFn: () => api<{ initialized: boolean }>("/setup/status"),
  });
  const [password, setPassword] = useState(""),
    [key, setKey] = useState("");
  const action = useAction();
  const setup = status.data && !status.data.initialized;
  async function submit(e: React.FormEvent) {
    e.preventDefault();
    await action.run(async () => {
      const s = await mutate<Session>(
        setup ? "/setup" : "/auth/login",
        setup ? { key, password } : { password },
      );
      setCSRF(s.csrfToken);
      qc.setQueryData(["session"], s);
      qc.invalidateQueries({ queryKey: ["setup"] });
      nav("/", { replace: true });
    });
  }
  return (
    <main className="auth-page">
      <Link to="/">
        <Brand />
      </Link>
      <Card className="auth-card">
        <Card.Content>
          <div className="eyebrow">YOUR CREATIVE SPACE</div>
          <h1>{setup ? "让空间，属于你。" : "欢迎回到你的空间。"}</h1>
          <p>
            {setup
              ? "使用初始化凭据，设置你的管理员密码。"
              : "登录后，继续发布你的创作。"}
          </p>
          {status.isPending ? (
            <Loading />
          ) : (
            <form onSubmit={submit}>
              {setup && (
                <TextField value={key} onChange={setKey} isRequired>
                  <Label>初始化凭据</Label>
                  <Input type="password" autoComplete="off" />
                  <p className="field-help">
                    从部署时创建的 setup.key 文件读取。
                  </p>
                </TextField>
              )}
              <TextField value={password} onChange={setPassword} isRequired>
                <Label>{setup ? "设置管理员密码" : "管理员密码"}</Label>
                <Input
                  type="password"
                  autoComplete={setup ? "new-password" : "current-password"}
                  minLength={setup ? 12 : undefined}
                />
                {setup && <p className="field-help">至少 12 个字符。</p>}
              </TextField>
              <ErrorNote error={action.error || status.error} />
              <Button type="submit" fullWidth size="lg" isPending={action.busy}>
                {setup ? "创建我的工作区" : "登录工作区"}
                <ArrowRight size={17} />
              </Button>
            </form>
          )}
        </Card.Content>
      </Card>
      <p className="auth-foot">你的内容，你的服务器。</p>
    </main>
  );
}

function Workspace() {
  const nav = useNavigate(),
    loc = useLocation(),
    qc = useQueryClient();
  const [theme, setTheme] = useState(
    () => localStorage.getItem("drop-theme-v2") || "light",
  );
  const [toast, setToast] = useState("");
  const action = useAction();
  useEffect(() => {
    document.documentElement.className = theme;
    localStorage.setItem("drop-theme-v2", theme);
  }, [theme]);
  useEffect(() => {
    if (!toast) return;
    const t = setTimeout(() => setToast(""), 3500);
    return () => clearTimeout(t);
  }, [toast]);
  useEffect(() => {
    const handler = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k") {
        e.preventDefault();
        nav("/");
        setTimeout(
          () =>
            document
              .querySelector<HTMLInputElement>(".search-field input")
              ?.focus(),
          100,
        );
      }
    };
    document.addEventListener("keydown", handler);
    return () => document.removeEventListener("keydown", handler);
  }, [nav]);
  async function copy(text: string) {
    try {
      await navigator.clipboard.writeText(text);
      setToast("链接已复制");
    } catch {
      setToast("无法访问剪贴板，请从站点详情复制地址");
    }
  }
  const navs = [
    ["/", "站点"],
    ["/deployments", "部署记录"],
    ["/tokens", "API 访问"],
    ["/system", "系统"],
  ];
  return (
    <>
      <a className="skip-link" href="#main">
        跳到主要内容
      </a>
      <header className="app-header">
        <div className="header-inner">
          <Link to="/" aria-label="Drop 站点首页">
            <Brand />
          </Link>
          <nav className="main-nav" aria-label="主导航">
            {navs.map(([path, label]) => (
              <Button
                key={path}
                variant="ghost"
                className={
                  loc.pathname === path ||
                  (path === "/" &&
                    (loc.pathname.startsWith("/sites") ||
                      loc.pathname === "/upload"))
                    ? "nav-active"
                    : ""
                }
                onPress={() => nav(path)}
              >
                {label}
              </Button>
            ))}
          </nav>
          <div className="header-actions">
            <Button
              isIconOnly
              variant="ghost"
              aria-label={theme === "light" ? "切换深色主题" : "切换浅色主题"}
              onPress={() => setTheme(theme === "light" ? "dark" : "light")}
            >
              {theme === "light" ? <Moon size={19} /> : <Sun size={19} />}
            </Button>
            <Button
              isIconOnly
              variant="ghost"
              aria-label="退出登录"
              isPending={action.busy}
              onPress={() =>
                action.run(async () => {
                  await mutate("/auth/logout", {});
                  qc.clear();
                  setCSRF("");
                  nav("/login");
                })
              }
            >
              <LogOut size={18} />
            </Button>
            <span className="user-avatar">豆</span>
          </div>
        </div>
      </header>
      <main id="main" className="main-content">
        <ErrorNote error={action.error} />
        <React.Suspense fallback={<Loading />}>
          <Routes>
            <Route index element={<Sites onCopy={copy} />} />
            <Route path="sites/:id" element={<SiteDetail onCopy={copy} />} />
            <Route path="deployments" element={<History />} />
            <Route path="upload" element={<UploadPage />} />
            <Route path="tokens" element={<Tokens />} />
            <Route path="system" element={<SystemPage />} />
            <Route path="trash" element={<Sites trash onCopy={copy} />} />
            <Route path="*" element={<Navigate to="/" replace />} />
          </Routes>
        </React.Suspense>
        <footer className="app-footer">
          <span>
            <span className="footer-mark">drop.</span>内容属于你，可能性也是。
          </span>
          <span>
            <Link to="/trash">回收站</Link>
            <span className="footer-separator">/</span>
            <span>自托管 · v0.1.0</span>
          </span>
        </footer>
      </main>
      <div className={"app-toast " + (toast ? "show" : "")} role="status">
        <Check size={17} />
        {toast}
      </div>
    </>
  );
}

function App() {
  const loc = useLocation();
  const session = useQuery({
    queryKey: ["session"],
    queryFn: () => api<Session>("/auth/session"),
    retry: false,
    refetchInterval: 60000,
  });
  useEffect(() => {
    setCSRF(session.data?.csrfToken || "");
  }, [session.data]);
  if (session.isPending)
    return (
      <main className="auth-page">
        <Brand />
        <Loading />
      </main>
    );
  if (
    session.error &&
    !(session.error instanceof APIError && session.error.status === 401)
  )
    return (
      <main className="auth-page">
        <Brand />
        <ErrorNote error={session.error} />
        <Button onPress={() => session.refetch()}>重新连接</Button>
      </main>
    );
  if (!session.data)
    return loc.pathname === "/login" ? (
      <Auth />
    ) : (
      <Navigate to="/login" replace />
    );
  if (loc.pathname === "/login") return <Navigate to="/" replace />;
  return <Workspace />;
}
createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <QueryClientProvider client={client}>
      <BrowserRouter>
        <App />
      </BrowserRouter>
    </QueryClientProvider>
  </React.StrictMode>,
);
