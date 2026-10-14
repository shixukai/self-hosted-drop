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
export default function Tokens() {
  const qc = useQueryClient();
  const tokens = useQuery({
    queryKey: ["tokens"],
    queryFn: () => api<Page<Token>>("/tokens"),
  });
  const sites = useSites();
  const [creating, setCreating] = useState(false),
    [name, setName] = useState(""),
    [days, setDays] = useState("90"),
    [selected, setSelected] = useState<string[]>([]),
    [permissions, setPermissions] = useState<string[]>([
      "sites:read",
      "deployments:write",
    ]),
    [secret, setSecret] = useState(""),
    [revoke, setRevoke] = useState<Token | null>(null),
    [copied, setCopied] = useState(false);
  const action = useAction();
  const labels: Record<string, string> = {
    "sites:read": "查看站点与部署",
    "deployments:write": "上传与取消部署",
    "publications:write": "发布与回滚",
    "previews:write": "创建预览授权",
    "sites:manage": "修改、暂停与恢复",
    "sites:delete": "删除与恢复站点",
  };
  function toggle(value: string, values: string[], set: (v: string[]) => void) {
    set(
      values.includes(value)
        ? values.filter((x) => x !== value)
        : [...values, value],
    );
  }
  return (
    <section className="view-enter">
      <div className="page-intro">
        <div>
          <div className="eyebrow">PROJECT / API ACCESS</div>
          <h1>
            让发布，<span>成为工作流。</span>
          </h1>
          <p>给自动化工具一把钥匙，由你定义它能做什么。</p>
        </div>
        <Button
          size="lg"
          onPress={() => {
            setCreating(true);
            setName("");
            setSelected([]);
            action.clear();
          }}
        >
          <Plus size={18} />
          创建 Token
        </Button>
      </div>
      <ErrorNote error={tokens.error} />
      <div className="token-intro">
        <span className="token-intro-icon">
          <KeyRound size={25} />
        </span>
        <div>
          <h2>更顺手的自动化，更明确的边界。</h2>
          <p>每个 Token 都绑定站点、权限和有效期。密钥只在创建时显示一次。</p>
        </div>
      </div>
      {tokens.isPending ? (
        <Loading />
      ) : tokens.data?.items.length ? (
        <div className="token-list">
          {tokens.data.items.map((t) => (
            <Card key={t.id} className="token-card">
              <Card.Content>
                <div className="token-card-head">
                  <div>
                    <h3>{t.name}</h3>
                    <code>{t.prefix}••••••••</code>
                  </div>
                  <Chip
                    size="sm"
                    color={
                      t.revokedAt || new Date(t.expiresAt) < new Date()
                        ? "default"
                        : "success"
                    }
                    variant="soft"
                  >
                    {t.revokedAt
                      ? "已撤销"
                      : new Date(t.expiresAt) < new Date()
                        ? "已过期"
                        : "有效"}
                  </Chip>
                </div>
                <div className="token-scopes">
                  {t.scopes.map((scope) => (
                    <span key={scope}>{labels[scope] || scope}</span>
                  ))}
                </div>
                <p>
                  {t.siteIds
                    .map(
                      (id) =>
                        sites.data?.items.find((s) => s.id === id)?.name ||
                        id.slice(0, 8),
                    )
                    .join("、")}
                </p>
                <div className="token-card-foot">
                  <span>
                    到期 {date(t.expiresAt)} · 最近使用 {date(t.lastUsedAt)}
                  </span>
                  {!t.revokedAt && (
                    <Button
                      size="sm"
                      variant="ghost"
                      onPress={() => setRevoke(t)}
                    >
                      撤销 Token
                    </Button>
                  )}
                </div>
              </Card.Content>
            </Card>
          ))}
        </div>
      ) : (
        <div className="empty-state">
          <KeyRound size={30} />
          <h2>让重复的操作，自动发生。</h2>
          <p>创建一个 Token，将上传发布接入你的脚本或 CI。</p>
        </div>
      )}
      <Card className="api-example">
        <Card.Content>
          <div className="section-heading">
            <h2>从第一个请求开始</h2>
            <Chip size="sm" variant="soft">
              REST API
            </Chip>
          </div>
          <p>用环境变量保存密钥，并通过 Bearer 认证调用 API。</p>
          <pre>
            <code>
              {'curl -H "Authorization: Bearer $DROP_TOKEN" \\\n  "' +
                location.origin +
                '/api/v1/sites"'}
            </code>
          </pre>
        </Card.Content>
      </Card>
      {creating && (
        <Modal.Backdrop
          isOpen
          onOpenChange={(o) => {
            if (!o) setCreating(false);
          }}
        >
          <Modal.Container size="lg">
            <Modal.Dialog>
              <Modal.CloseTrigger />
              <Modal.Header>
                <Modal.Heading>创建访问 Token</Modal.Heading>
              </Modal.Header>
              <Modal.Body>
                <TextField value={name} onChange={setName}>
                  <Label>名称</Label>
                  <Input placeholder="例如 GitHub Actions" maxLength={100} />
                </TextField>
                <label className="field-label">
                  有效期
                  <select
                    value={days}
                    onChange={(e) => setDays(e.target.value)}
                  >
                    <option value="30">30 天</option>
                    <option value="90">90 天</option>
                    <option value="365">365 天</option>
                  </select>
                </label>
                <fieldset className="check-options">
                  <legend>允许访问的站点</legend>
                  {sites.data?.items.map((s) => (
                    <label key={s.id}>
                      <input
                        type="checkbox"
                        checked={selected.includes(s.id)}
                        onChange={() => toggle(s.id, selected, setSelected)}
                      />
                      <span>
                        {s.name}
                        <small>{s.slug}</small>
                      </span>
                    </label>
                  ))}
                  {!sites.data?.items.length && (
                    <p>先创建一个站点，再为它配置 Token。</p>
                  )}
                </fieldset>
                <fieldset className="check-options">
                  <legend>允许的操作</legend>
                  {Object.entries(labels).map(([scope, label]) => (
                    <label key={scope}>
                      <input
                        type="checkbox"
                        checked={permissions.includes(scope)}
                        onChange={() =>
                          toggle(scope, permissions, setPermissions)
                        }
                      />
                      {label}
                    </label>
                  ))}
                </fieldset>
                <ErrorNote error={action.error} />
              </Modal.Body>
              <Modal.Footer>
                <Button variant="secondary" onPress={() => setCreating(false)}>
                  取消
                </Button>
                <Button
                  isPending={action.busy}
                  isDisabled={
                    !name.trim() || !selected.length || !permissions.length
                  }
                  onPress={() =>
                    action.run(async () => {
                      const t = await mutate<{ token: string }>("/tokens", {
                        name: name.trim(),
                        siteIds: selected,
                        scopes: permissions,
                        expiresAt: new Date(
                          Date.now() + Number(days) * 86400000,
                        ).toISOString(),
                      });
                      setSecret(t.token);
                      setCopied(false);
                      setCreating(false);
                      await qc.invalidateQueries({ queryKey: ["tokens"] });
                    })
                  }
                >
                  创建 Token
                </Button>
              </Modal.Footer>
            </Modal.Dialog>
          </Modal.Container>
        </Modal.Backdrop>
      )}
      {secret && (
        <Modal.Backdrop
          isOpen
          onOpenChange={(o) => {
            if (!o) setSecret("");
          }}
        >
          <Modal.Container>
            <Modal.Dialog>
              <Modal.CloseTrigger />
              <Modal.Header>
                <Modal.Heading>保存你的 Token</Modal.Heading>
              </Modal.Header>
              <Modal.Body>
                <p>完整密钥只显示这一次。关闭后无法再次查看。</p>
                <code className="secret-value">{secret}</code>
                <Button
                  variant="secondary"
                  onPress={() =>
                    action.run(async () => {
                      await navigator.clipboard.writeText(secret);
                      setCopied(true);
                    })
                  }
                >
                  {copied ? <Check size={16} /> : <Copy size={16} />}{" "}
                  {copied ? "已复制" : "复制 Token"}
                </Button>
                <ErrorNote error={action.error} />
              </Modal.Body>
              <Modal.Footer>
                <Button onPress={() => setSecret("")}>我已保存</Button>
              </Modal.Footer>
            </Modal.Dialog>
          </Modal.Container>
        </Modal.Backdrop>
      )}
      {revoke && (
        <Confirm
          title="撤销这个 Token？"
          body={
            "“" +
            revoke.name +
            "” 的后续请求会立即被拒绝，现有公开内容不受影响。"
          }
          danger
          confirmText="撤销 Token"
          onClose={() => setRevoke(null)}
          onConfirm={async () => {
            await api("/tokens/" + revoke.id, { method: "DELETE" });
            await qc.invalidateQueries({ queryKey: ["tokens"] });
          }}
        />
      )}
    </section>
  );
}
