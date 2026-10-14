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
} from "./api";
import { FileStack, SiteArtwork } from "./artwork";
import "@fontsource-variable/manrope";

export const messages: Record<string, string> = {
  DRAFT: "待发布",
  ACTIVE: "已发布",
  PAUSED: "已暂停",
  DELETED: "回收站",
  READY: "检查完成",
  QUEUED: "排队中",
  CHECKING: "检查中",
  EXTRACTING: "解包中",
  VALIDATING: "验证中",
  FAILED: "失败",
  CANCELED: "已取消",
  AWAITING_INPUT: "选择入口",
  PENDING: "等待切换",
  APPLYING: "切换中",
  RECOVERING: "恢复中",
  APPLIED: "已生效",
};
export function Status({ value }: { value: string }) {
  return (
    <Chip
      className="state-chip"
      size="sm"
      variant="soft"
      color={
        ["ACTIVE", "APPLIED", "READY"].includes(value)
          ? "success"
          : ["FAILED", "RECOVERING"].includes(value)
            ? "danger"
            : ["AWAITING_INPUT", "PENDING", "APPLYING", "CHECKING"].includes(
                  value,
                )
              ? "warning"
              : "default"
      }
    >
      <span className="status-dot" />
      {messages[value] || value}
    </Chip>
  );
}

export function ErrorNote({ error }: { error: unknown }) {
  return error ? (
    <div className="error-note" role="alert">
      <AlertCircle size={18} />
      <span>{error instanceof Error ? error.message : String(error)}</span>
    </div>
  ) : null;
}

export function Loading() {
  return (
    <div className="loading-state" role="status">
      <span className="loading-dot" />
      正在读取…
    </div>
  );
}

export function useAction() {
  const [busy, setBusy] = useState(false),
    [error, setError] = useState<unknown>(null);
  async function run(fn: () => Promise<unknown>) {
    setError(null);
    setBusy(true);
    try {
      return await fn();
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  }
  return { busy, error, run, clear: () => setError(null) };
}

export function useSystem() {
  return useQuery({
    queryKey: ["system"],
    queryFn: () => api<System>("/system/status"),
    refetchInterval: 15000,
  });
}

export function useSites() {
  return useQuery({
    queryKey: ["sites", "select"],
    queryFn: () => api<Page<Site>>("/sites?limit=100"),
  });
}

export function Brand() {
  return (
    <span className="brand">
      <span className="brand-mark">
        <i />
        <i />
        <i />
      </span>
      <span>
        drop<span className="brand-period">.</span>
      </span>
    </span>
  );
}

export function Confirm({
  title,
  body,
  confirmText = "确认",
  danger = false,
  typed,
  onConfirm,
  onClose,
}: {
  title: string;
  body: string;
  confirmText?: string;
  danger?: boolean;
  typed?: string;
  onConfirm: () => Promise<unknown>;
  onClose: () => void;
}) {
  const [value, setValue] = useState("");
  const action = useAction();
  return (
    <Modal.Backdrop
      isOpen
      onOpenChange={(o) => {
        if (!o && !action.busy) onClose();
      }}
      variant="blur"
    >
      <Modal.Container>
        <Modal.Dialog>
          <Modal.CloseTrigger isDisabled={action.busy} />
          <Modal.Header>
            <Modal.Heading>{title}</Modal.Heading>
          </Modal.Header>
          <Modal.Body>
            <p>{body}</p>
            {typed && (
              <TextField value={value} onChange={setValue}>
                <Label>输入 {typed} 确认</Label>
                <Input autoComplete="off" />
              </TextField>
            )}
            <ErrorNote error={action.error} />
          </Modal.Body>
          <Modal.Footer>
            <Button
              variant="secondary"
              onPress={onClose}
              isDisabled={action.busy}
            >
              取消
            </Button>
            <Button
              variant={danger ? "danger" : "primary"}
              isPending={action.busy}
              isDisabled={!!typed && value !== typed}
              onPress={() =>
                action.run(async () => {
                  await onConfirm();
                  onClose();
                })
              }
            >
              {confirmText}
            </Button>
          </Modal.Footer>
        </Modal.Dialog>
      </Modal.Container>
    </Modal.Backdrop>
  );
}
