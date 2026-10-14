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
export default function SystemPage() {
  const system = useSystem();
  const qc = useQueryClient();
  const nav = useNavigate();
  const audit = useInfiniteQuery({
    queryKey: ["audit"],
    initialPageParam: "",
    queryFn: ({ pageParam }) =>
      api<
        Page<{
          id: number;
          action: string;
          result: string;
          actor: string;
          createdAt: string;
        }>
      >("/system/audit?limit=20" + (pageParam ? "&cursor=" + pageParam : "")),
    getNextPageParam: (p) => p.nextCursor || undefined,
  });
  const backups = useQuery({
    queryKey: ["backups"],
    queryFn: () =>
      api<
        Page<{
          id: string;
          status: string;
          createdAt: string;
          completedAt: string | null;
        }>
      >("/system/backups"),
  });
  const [changing, setChanging] = useState(false),
    [current, setCurrent] = useState(""),
    [password, setPassword] = useState("");
  const action = useAction();
  const s = system.data;
  return (
    <section className="view-enter">
      <div className="page-intro">
        <div>
          <div className="eyebrow">WORKSPACE / SYSTEM</div>
          <h1>
            所有状态，<span>心中有数。</span>
          </h1>
          <p>存储、服务和操作记录，都在这里。</p>
        </div>
        <Button
          variant="secondary"
          onPress={() => {
            qc.invalidateQueries({ queryKey: ["system"] });
            qc.invalidateQueries({ queryKey: ["audit"] });
            qc.invalidateQueries({ queryKey: ["backups"] });
          }}
        >
          <RefreshCw size={16} />
          刷新状态
        </Button>
      </div>
      <ErrorNote error={system.error} />
      {s ? (
        <>
          <div className="system-metrics">
            <Card>
              <Card.Content>
                <HardDrive size={22} />
                <span>文件存储</span>
                <strong>{size(s.usedBytes)}</strong>
                <small>
                  额度 {size(s.quotaBytes)} · 预占 {size(s.reservedBytes)}
                </small>
                <div className="usage-bar">
                  <i
                    style={{
                      width:
                        Math.min(100, (s.usedBytes / s.quotaBytes) * 100) + "%",
                    }}
                  />
                </div>
              </Card.Content>
            </Card>
            <Card>
              <Card.Content>
                <Globe2 size={22} />
                <span>内容网关</span>
                <strong>{s.gatewayReady ? "运行正常" : "连接异常"}</strong>
                <small>{s.contentDomain}</small>
              </Card.Content>
            </Card>
            <Card>
              <Card.Content>
                <Clock3 size={22} />
                <span>后台任务</span>
                <strong>{s.queuedJobs}</strong>
                <small>
                  {s.siteCount} 个站点 · 磁盘可用 {size(s.diskFreeBytes)}
                </small>
              </Card.Content>
            </Card>
          </div>
          <div className="system-sections">
            <Card>
              <Card.Content>
                <div className="section-heading">
                  <h2>备份记录</h2>
                  <Chip size="sm" variant="soft">
                    {s.backupConfigured ? "已配置" : "尚未配置"}
                  </Chip>
                </div>
                <p>最近验证成功：{date(s.latestVerifiedBackup)}</p>
                <p className="field-help">
                  备份由服务器上的备份命令执行；完成并校验后会出现在这里。
                </p>
                <ErrorNote error={backups.error} />
                {backups.data?.items.map((b) => (
                  <div className="backup-row" key={b.id}>
                    <span>{date(b.createdAt)}</span>
                    <Status value={b.status} />
                  </div>
                ))}
              </Card.Content>
            </Card>
            <Card>
              <Card.Content>
                <div className="section-heading">
                  <h2>工作区安全</h2>
                  <ShieldCheck size={19} />
                </div>
                <p>管理员登录采用独立会话。修改密码会退出所有已登录设备。</p>
                <Button
                  variant="secondary"
                  onPress={() => {
                    setChanging(true);
                    setCurrent("");
                    setPassword("");
                    action.clear();
                  }}
                >
                  修改管理员密码
                </Button>
                <div className="system-domain">
                  <span>管理地址</span>
                  <code>{s.adminOrigin}</code>
                </div>
              </Card.Content>
            </Card>
          </div>
        </>
      ) : (
        <Loading />
      )}
      <section className="audit-section">
        <div className="section-heading">
          <h2>操作记录</h2>
          <span>最新操作在前</span>
        </div>
        <ErrorNote error={audit.error} />
        <div className="audit-list">
          {audit.data?.pages
            .flatMap((p) => p.items)
            .map((e) => (
              <div className="audit-row" key={e.id}>
                <span className="activity-icon">
                  <ShieldCheck size={16} />
                </span>
                <div>
                  <strong>{e.action}</strong>
                  <small>
                    {e.actor.startsWith("token:")
                      ? "API Token"
                      : e.actor === "controller"
                        ? "发布协调器"
                        : "管理员"}
                  </small>
                </div>
                <code>{e.result}</code>
                <time>{date(e.createdAt)}</time>
              </div>
            ))}
        </div>
        {audit.hasNextPage && (
          <Button variant="secondary" onPress={() => audit.fetchNextPage()}>
            加载更多
          </Button>
        )}
      </section>
      {changing && (
        <Modal.Backdrop
          isOpen
          onOpenChange={(o) => {
            if (!o) setChanging(false);
          }}
        >
          <Modal.Container>
            <Modal.Dialog>
              <Modal.CloseTrigger />
              <Modal.Header>
                <Modal.Heading>修改管理员密码</Modal.Heading>
              </Modal.Header>
              <Modal.Body>
                <TextField value={current} onChange={setCurrent}>
                  <Label>当前密码</Label>
                  <Input type="password" autoComplete="current-password" />
                </TextField>
                <TextField value={password} onChange={setPassword}>
                  <Label>新密码</Label>
                  <Input
                    type="password"
                    autoComplete="new-password"
                    minLength={12}
                  />
                </TextField>
                <p>至少 12 个字符。保存后需要重新登录。</p>
                <ErrorNote error={action.error} />
              </Modal.Body>
              <Modal.Footer>
                <Button
                  isDisabled={password.length < 12 || !current}
                  isPending={action.busy}
                  onPress={() =>
                    action.run(async () => {
                      await mutate("/auth/password", {
                        currentPassword: current,
                        password,
                      });
                      qc.clear();
                      setCSRF("");
                      nav("/login");
                    })
                  }
                >
                  保存新密码
                </Button>
              </Modal.Footer>
            </Modal.Dialog>
          </Modal.Container>
        </Modal.Backdrop>
      )}
    </section>
  );
}
