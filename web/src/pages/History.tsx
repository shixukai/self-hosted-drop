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
export default function History() {
  const sites = useSites();
  const nav = useNavigate();
  return (
    <section className="view-enter">
      <div className="page-intro">
        <div>
          <div className="eyebrow">PROJECT / DEPLOYMENTS</div>
          <h1>
            每次发布，<span>都有迹可循。</span>
          </h1>
          <p>选择站点，查看检查结果、历史版本与发布状态。</p>
        </div>
      </div>
      {sites.isPending ? <Loading /> : <ErrorNote error={sites.error} />}
      <div className="activity-list history-index">
        {sites.data?.items.map((s) => (
          <button
            className="activity-row"
            key={s.id}
            onClick={() => nav("/sites/" + s.id)}
          >
            <span className="activity-icon">
              <Globe2 size={20} />
            </span>
            <span className="activity-copy">
              <strong>{s.name}</strong>
              <span>
                {s.slug} · {s.deploymentCount} 个部署
              </span>
            </span>
            <span className="activity-version">
              <Status value={s.status} />
            </span>
            <time>{date(s.updatedAt)}</time>
            <ArrowUpRight size={17} />
          </button>
        ))}
      </div>
      {sites.data?.items.length === 0 && (
        <div className="empty-state">
          <h2>还没有部署记录</h2>
          <Button onPress={() => nav("/upload")}>发布第一个作品</Button>
        </div>
      )}
    </section>
  );
}
