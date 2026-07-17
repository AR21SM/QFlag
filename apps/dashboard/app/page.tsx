"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import {
  Check,
  Copy,
  FileClock,
  Flag,
  Moon,
  RotateCcw,
  Search,
  Sun,
  Users,
} from "lucide-react";
import { ApiClient } from "@/lib/api";
import type { AuditLog, ClusterStatus, FeatureFlag } from "@/lib/types";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Progress } from "@/components/ui/progress";
import { Select } from "@/components/ui/select";
import { Separator } from "@/components/ui/separator";
import { Switch } from "@/components/ui/switch";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { cn } from "@/lib/utils";

const api = new ApiClient();

type FormState = {
  enabled: boolean;
  rolloutPercentage: number;
  targetUsers: string;
};

export default function DashboardPage() {
  const [flags, setFlags] = useState<FeatureFlag[]>([]);
  const [cluster, setCluster] = useState<ClusterStatus | null>(null);
  const [audit, setAudit] = useState<AuditLog[]>([]);
  const [selectedName, setSelectedName] = useState("");
  const [form, setForm] = useState<FormState>({ enabled: false, rolloutPercentage: 0, targetUsers: "" });
  const [search, setSearch] = useState("");
  const [status, setStatus] = useState<"all" | "enabled" | "disabled">("all");
  const [sort, setSort] = useState<"updated" | "name">("updated");
  const [theme, setTheme] = useState<"dark" | "light">("dark");
  const [busy, setBusy] = useState(false);
  const [loading, setLoading] = useState(true);
  const [backendError, setBackendError] = useState("");
  const [notice, setNotice] = useState("");
  const [copied, setCopied] = useState(false);

  const selected = flags.find((flag) => flag.flagName === selectedName) ?? flags[0];
  const clusterHealthy = Boolean(cluster?.leader) && cluster?.healthyNodes === cluster?.totalNodes;
  const leaderNode = cluster?.nodes.find((node) => node.id === cluster.leader);

  const refresh = useCallback(async () => {
    const [flagsResult, clusterResult, auditResult] = await Promise.allSettled([
      api.listFlags(),
      api.clusterStatus(),
      api.audit(),
    ]);

    if (flagsResult.status === "fulfilled") {
      setFlags(flagsResult.value);
      if (flagsResult.value.length > 0 && !flagsResult.value.some((flag) => flag.flagName === selectedName)) {
        setSelectedName(flagsResult.value[0].flagName);
      }
      if (flagsResult.value.length === 0) setSelectedName("");
    } else {
      setFlags([]);
      setSelectedName("");
    }
    setCluster(clusterResult.status === "fulfilled" ? clusterResult.value : null);
    setAudit(auditResult.status === "fulfilled" ? auditResult.value : []);

    const failed = [flagsResult, clusterResult, auditResult].filter((result) => result.status === "rejected").length;
    setBackendError(failed === 0 ? "" : failed === 3 ? "Backend unavailable. No data is being displayed." : "Some backend data is unavailable.");
    setLoading(false);
  }, [selectedName]);

  useEffect(() => {
    const storedTheme = window.localStorage.getItem("qflag-theme");
    if (storedTheme === "light" || storedTheme === "dark") setTheme(storedTheme);
  }, []);

  useEffect(() => {
    const root = document.documentElement;
    root.classList.toggle("light-theme", theme === "light");
    root.classList.toggle("dark", theme === "dark");
    window.localStorage.setItem("qflag-theme", theme);
  }, [theme]);

  useEffect(() => {
    void refresh();
    const timer = window.setInterval(() => void refresh(), 5000);
    return () => window.clearInterval(timer);
  }, [refresh]);

  useEffect(() => {
    if (!selected) return;
    setForm({
      enabled: selected.enabled,
      rolloutPercentage: selected.rolloutPercentage,
      targetUsers: selected.targetUsers.join(", "),
    });
    setNotice("");
  }, [selected]);

  const dirty = Boolean(selected) && (
    form.enabled !== selected.enabled ||
    form.rolloutPercentage !== selected.rolloutPercentage ||
    form.targetUsers !== selected.targetUsers.join(", ")
  );

  const filteredFlags = useMemo(() => {
    return [...flags]
      .filter((flag) => flag.flagName.toLowerCase().includes(search.toLowerCase()))
      .filter((flag) => status === "all" || (status === "enabled" ? flag.enabled : !flag.enabled))
      .sort((a, b) => sort === "name" ? a.flagName.localeCompare(b.flagName) : Date.parse(b.updatedAt) - Date.parse(a.updatedAt));
  }, [flags, search, sort, status]);

  async function updateFlag() {
    if (!selected) return;
    setBusy(true);
    setNotice("Applying changes through Raft consensus…");
    try {
      await api.updateFlag(selected.flagName, {
        enabled: form.enabled,
        rolloutPercentage: form.rolloutPercentage,
        targetUsers: splitUsers(form.targetUsers),
      });
      await refresh();
      setNotice("Changes committed successfully.");
    } catch (error) {
      setNotice(error instanceof Error ? error.message : "Update failed");
    } finally {
      setBusy(false);
    }
  }

  async function rollbackFlag() {
    if (!selected || selected.version <= 1) return;
    setBusy(true);
    try {
      await api.rollback(selected.flagName, selected.version - 1);
      await refresh();
      setNotice("Previous configuration restored as a new version.");
    } catch (error) {
      setNotice(error instanceof Error ? error.message : "Rollback failed");
    } finally {
      setBusy(false);
    }
  }

  async function copyFlagName() {
    if (!selected) return;
    await navigator.clipboard.writeText(selected.flagName);
    setCopied(true);
    window.setTimeout(() => setCopied(false), 1500);
  }

  return (
    <div className="min-h-screen bg-background text-foreground">
      <header className="flex h-16 items-center justify-between border-b bg-background px-5 lg:px-6">
        <div className="flex items-center gap-5">
          <div className="flex items-center gap-2.5">
            <Flag className="size-5" />
            <span className="text-base font-semibold tracking-tight">QFlag</span>
          </div>
          <Separator orientation="vertical" className="h-7" />
          <span className="text-sm font-medium">Control Center</span>
        </div>
        <div className="flex items-center gap-4">
          <div className="hidden items-center gap-2 text-sm md:flex">
            <StatusDot healthy={clusterHealthy} warning={Boolean(cluster) && !clusterHealthy} />
            <span>{clusterHealthy ? "All systems operational" : cluster ? "Cluster degraded" : "Cluster unavailable"}</span>
          </div>
          <Separator orientation="vertical" className="hidden h-7 md:block" />
          <div className="flex rounded-md border bg-card p-0.5">
            <button aria-label="Use dark theme" onClick={() => setTheme("dark")} className={cn("rounded px-2.5 py-1.5", theme === "dark" ? "bg-foreground text-background" : "text-muted-foreground hover:text-foreground")}><Moon className="size-4" /></button>
            <button aria-label="Use light theme" onClick={() => setTheme("light")} className={cn("rounded px-2.5 py-1.5", theme === "light" ? "bg-foreground text-background" : "text-muted-foreground hover:text-foreground")}><Sun className="size-4" /></button>
          </div>
        </div>
      </header>

      {backendError ? <div role="status" className="border-b border-amber-500/30 bg-amber-500/10 px-5 py-2 text-sm text-amber-700 dark:text-amber-200 lg:px-6">{backendError}</div> : null}

      <main className="grid min-h-[calc(100vh-4rem)] gap-2 p-3 lg:grid-cols-[minmax(0,3fr)_minmax(320px,1.1fr)] lg:p-4">
        <section className="flex min-w-0 flex-col gap-2">
          <div className="rounded-lg border bg-card">
            <div className="px-5 py-4">
              <div className="flex items-start justify-between gap-4">
                <div>
                  <h1 className="text-xl font-semibold tracking-tight">Flag controls</h1>
                  <p className="mt-1 text-sm text-muted-foreground">Manage rollout, targeting, and behavior for your feature flags.</p>
                </div>
                <span className="rounded-full border px-2.5 py-1 text-xs text-muted-foreground">{dirty ? "1 unsaved change" : "0 unsaved changes"}</span>
              </div>
            </div>

            {selected ? <>
            <Separator />

            <div className="flex flex-wrap items-end justify-between gap-4 px-5 py-3">
              <div>
                <span className="text-xs text-muted-foreground">Selected flag</span>
                <div className="mt-1 flex items-center gap-2">
                  <h2 className="text-xl font-semibold tracking-tight">{selected?.flagName ?? "No flag selected"}</h2>
                  <button aria-label="Copy flag name" onClick={() => void copyFlagName()} className="text-muted-foreground hover:text-foreground">{copied ? <Check className="size-4 text-emerald-500" /> : <Copy className="size-4" />}</button>
                </div>
              </div>
              {selected ? <div className="flex items-center gap-3 text-xs text-muted-foreground"><span>Created {formatRelativeTime(selected.createdAt)}</span><span>•</span><span>Updated {formatRelativeTime(selected.updatedAt)}</span><span>•</span><span>Version {selected.version}</span></div> : null}
            </div>

            <Separator />

            <div className="grid gap-5 px-5 py-4 md:grid-cols-[240px_minmax(0,1fr)]">
              <div className="border-b pb-4 md:border-b-0 md:border-r md:pb-0 md:pr-6">
                <Label>Status</Label>
                <p className="mt-1 text-xs text-muted-foreground">Turn the flag on or off.</p>
                <div className="mt-4 flex items-center gap-2.5"><Switch checked={form.enabled} onCheckedChange={(enabled) => setForm((current) => ({ ...current, enabled }))} /><span className="text-sm">{form.enabled ? "Enabled" : "Disabled"}</span></div>
              </div>
              <div>
                <Label htmlFor="rollout">Rollout</Label>
                <p className="mt-1 text-xs text-muted-foreground">Gradually roll out to your users.</p>
                <div className="mt-3 grid gap-5 sm:grid-cols-[120px_minmax(0,1fr)] sm:items-center">
                  <div className="flex h-11 items-center rounded-md border bg-background">
                    <button aria-label="Decrease rollout" onClick={() => setForm((current) => ({ ...current, rolloutPercentage: Math.max(0, current.rolloutPercentage - 5) }))} className="h-full px-3 text-muted-foreground hover:text-foreground">−</button>
                    <span className="flex-1 text-center text-sm tabular-nums">{form.rolloutPercentage}</span>
                    <span className="pr-2 text-sm">%</span>
                    <button aria-label="Increase rollout" onClick={() => setForm((current) => ({ ...current, rolloutPercentage: Math.min(100, current.rolloutPercentage + 5) }))} className="h-full border-l px-2 text-muted-foreground hover:text-foreground">+</button>
                  </div>
                  <div className="py-4">
                    <div className="relative h-5">
                      <Progress value={form.rolloutPercentage} className="pointer-events-none absolute top-1/2 -translate-y-1/2" />
                      <input id="rollout" type="range" min={0} max={100} step={5} value={form.rolloutPercentage} onChange={(event) => setForm((current) => ({ ...current, rolloutPercentage: Number(event.target.value) }))} className="absolute inset-0 z-10 m-0 h-5" />
                    </div>
                    <div className="mt-2 flex justify-between text-xs text-muted-foreground"><span>0%</span><span>25%</span><span>50%</span><span>75%</span><span>100%</span></div>
                  </div>
                </div>
              </div>
            </div>

            <Separator />

            <div className="grid gap-4 px-5 py-3 md:grid-cols-[240px_minmax(0,1fr)] md:items-center">
              <div><Label htmlFor="targeting">Targeting</Label><p className="mt-1 text-xs text-muted-foreground">Define who will see this flag.</p></div>
              <div className="relative"><Users className="absolute left-3 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" /><Input id="targeting" value={form.targetUsers} onChange={(event) => setForm((current) => ({ ...current, targetUsers: event.target.value }))} className="pl-9" placeholder="user@example.com, segment-name" /></div>
            </div>

            <Separator />

            <div className="flex flex-wrap items-center justify-between gap-3 px-5 py-3">
              <Button variant="outline" onClick={() => void rollbackFlag()} disabled={busy || selected.version <= 1}><RotateCcw className="size-4" />Rollback one version</Button>
              <div className="flex items-center gap-3"><span className={cn("text-xs", notice.includes("failed") || notice.includes("unavailable") ? "text-destructive" : "text-muted-foreground")}>{notice}</span><Button onClick={() => void updateFlag()} disabled={busy || !selected || !dirty}>{busy ? "Updating…" : "Update changes"}</Button></div>
            </div>
            </> : <div className="border-t px-5 py-16 text-center"><p className="font-medium">{loading ? "Loading flags…" : backendError ? "Flags unavailable" : "No flags yet"}</p><p className="mt-2 text-sm text-muted-foreground">{loading ? "Reading the Raft-backed flag store." : backendError ? "Start or configure the backend to manage flags." : "Create a flag through the API to begin managing it here."}</p></div>}
          </div>

          <div className="min-h-[330px] rounded-lg border bg-card">
            <div className="flex flex-wrap items-center justify-between gap-3 px-5 py-4">
              <div className="flex items-center gap-4"><h2 className="text-lg font-semibold">All flags</h2><div className="relative"><Search className="absolute left-3 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" /><Input value={search} onChange={(event) => setSearch(event.target.value)} className="w-44 pl-9" placeholder="Search flags…" /></div></div>
              <div className="flex gap-2"><Select value={status} onChange={(event) => setStatus(event.target.value as typeof status)}><option value="all">All statuses</option><option value="enabled">Enabled</option><option value="disabled">Disabled</option></Select><Select value={sort} onChange={(event) => setSort(event.target.value as typeof sort)}><option value="updated">Sort: Updated</option><option value="name">Sort: Name</option></Select></div>
            </div>
            <div className="overflow-x-auto">
              <Table>
                <TableHeader><TableRow><TableHead>Flag name</TableHead><TableHead>Status</TableHead><TableHead>Rollout</TableHead><TableHead>Targeting</TableHead><TableHead>Updated</TableHead><TableHead>Version</TableHead></TableRow></TableHeader>
                <TableBody>
                  {filteredFlags.map((flag) => (
                    <TableRow key={flag.flagName} onClick={() => setSelectedName(flag.flagName)} className={cn("cursor-pointer", selected?.flagName === flag.flagName && "bg-muted/60")}>
                      <TableCell className="font-medium">{flag.flagName}</TableCell>
                      <TableCell><span className="inline-flex items-center gap-2"><StatusDot healthy={flag.enabled} warning={!flag.enabled} />{flag.enabled ? "Enabled" : "Disabled"}</span></TableCell>
                      <TableCell><div className="flex min-w-32 items-center gap-3"><span className="w-9 tabular-nums">{flag.rolloutPercentage}%</span><Progress value={flag.rolloutPercentage} /></div></TableCell>
                      <TableCell className="text-muted-foreground">{targetingLabel(flag)}</TableCell>
                      <TableCell className="text-muted-foreground">{formatRelativeTime(flag.updatedAt)}</TableCell>
                      <TableCell>{flag.version}</TableCell>
                    </TableRow>
                  ))}
                  {!loading && filteredFlags.length === 0 ? <TableRow><TableCell colSpan={6} className="h-28 text-center text-muted-foreground">{flags.length === 0 ? "No flags returned by the backend." : "No flags match the current filters."}</TableCell></TableRow> : null}
                </TableBody>
              </Table>
            </div>
            <div className="border-t px-5 py-3 text-xs text-muted-foreground">{filteredFlags.length} of {flags.length} flags</div>
          </div>
        </section>

        <aside className="grid min-w-0 gap-2 lg:grid-rows-[472px_minmax(0,1fr)]">
          <div className="rounded-lg border bg-card p-5">
            <div className="flex items-center justify-between"><h2 className="text-lg font-semibold">Cluster status</h2>{cluster ? <span className="flex items-center gap-2 text-sm"><StatusDot healthy={clusterHealthy} warning={!clusterHealthy} />{clusterHealthy ? "Healthy" : "Degraded"}</span> : null}</div>
            {cluster ? <>
              <div className="mt-5 flex items-end justify-between"><div><span className="text-sm text-muted-foreground">Leader</span><p className="mt-1 font-medium">{cluster.leader || "unknown"}</p></div><span className={cn("text-sm font-medium", cluster.leader ? "text-emerald-500" : "text-destructive")}>{cluster.leader ? "Up" : "Down"}</span></div>
              <Separator className="my-4" />
              <div className="flex items-center justify-between text-sm"><span className="text-muted-foreground">Nodes</span><span className={clusterHealthy ? "text-emerald-500" : "text-amber-400"}>{cluster.healthyNodes}/{cluster.totalNodes} healthy</span></div>
              <div className="mt-4 space-y-4">
                {cluster.nodes.map((node) => <div key={node.id} className="grid grid-cols-[1fr_auto] items-center gap-4 text-sm"><span className="flex items-center gap-2"><StatusDot healthy={node.healthy} warning={!node.healthy} />{node.id}{node.role === "leader" ? " (leader)" : ""}</span><span className="text-muted-foreground capitalize">{node.role}</span></div>)}
              </div>
              <Separator className="my-4" />
              <dl className="space-y-3 text-sm"><div className="flex justify-between"><dt className="text-muted-foreground">Term</dt><dd>{cluster.term}</dd></div><div className="flex justify-between"><dt className="text-muted-foreground">Commit index</dt><dd>{cluster.commitIndex.toLocaleString()}</dd></div><div className="flex justify-between"><dt className="text-muted-foreground">Last applied</dt><dd>{leaderNode ? `${leaderNode.lastApplied.toLocaleString()} (${formatRelativeTime(leaderNode.lastHeartbeatAt)})` : "—"}</dd></div><div className="flex justify-between"><dt className="text-muted-foreground">Avg commit latency</dt><dd>{cluster.avgCommitLatency}</dd></div></dl>
            </> : <div className="flex h-[360px] items-center justify-center text-center"><div><p className="font-medium">Cluster data unavailable</p><p className="mt-2 text-sm text-muted-foreground">Cluster status will appear when the backend is available.</p></div></div>}
          </div>

          <div className="min-h-[400px] rounded-lg border bg-card p-5">
            <h2 className="text-lg font-semibold">Audit log</h2>
            <div className="mt-4">
              {audit.slice(0, 5).map((entry, index) => <AuditItem key={`${entry.timestamp}-${index}`} entry={entry} />)}
              {!loading && audit.length === 0 ? <div className="py-14 text-center"><p className="font-medium">No audit entries</p><p className="mt-2 text-sm text-muted-foreground">Only backend-recorded activity appears here.</p></div> : null}
            </div>
          </div>
        </aside>
      </main>
    </div>
  );
}

function AuditItem({ entry }: { entry: AuditLog }) {
  const isRollback = entry.action.includes("ROLLBACK");
  const title = auditTitle(entry.action);
  return (
    <div className="grid grid-cols-[20px_minmax(0,1fr)_auto] gap-3 border-b py-3.5 last:border-b-0">
      {isRollback ? <FileClock className="mt-0.5 size-4" /> : entry.action.includes("TARGET") ? <Users className="mt-0.5 size-4" /> : <Flag className="mt-0.5 size-4" />}
      <div className="min-w-0"><div className="flex items-center gap-2 text-sm font-medium"><StatusDot healthy={!isRollback} warning={isRollback} />{title}</div><p className="mt-1 truncate text-xs text-muted-foreground">{auditDetail(entry)}</p><p className="mt-1 text-xs text-muted-foreground">{entry.updatedBy}</p></div>
      <span className="text-xs text-muted-foreground">{formatRelativeTime(entry.timestamp)}</span>
    </div>
  );
}

function StatusDot({ healthy, warning = false }: { healthy: boolean; warning?: boolean }) {
  return <span className={cn("inline-block size-2 shrink-0 rounded-full", warning ? "bg-amber-400" : healthy ? "bg-emerald-400" : "bg-zinc-500")} />;
}

function splitUsers(value: string): string[] {
  return value.split(",").map((item) => item.trim()).filter(Boolean);
}

function targetingLabel(flag: FeatureFlag) {
  if (flag.targetUsers.length === 0) return "Everyone";
  if (flag.targetUsers.length === 1 && flag.targetUsers[0] === "internal-only") return "Internal only";
  return flag.targetUsers.length === 1 ? "1 user" : `${flag.targetUsers.length} users`;
}

function auditTitle(action: string) {
  if (action.includes("ROLLBACK")) return "Rolled back";
  if (action.includes("CREATE")) return "Flag created";
  if (action.includes("DELETE")) return "Flag deleted";
  return "Flag updated";
}

function auditDetail(entry: AuditLog) {
  if (!entry.action.includes("UPDATE")) return entry.flagName;
  const oldValue = recordValue(entry.oldValue);
  const newValue = recordValue(entry.newValue);
  const changes: string[] = [];
  if (oldValue.rolloutPercentage !== newValue.rolloutPercentage && typeof newValue.rolloutPercentage === "number") changes.push(`rollout ${newValue.rolloutPercentage}%`);
  if (oldValue.enabled !== newValue.enabled && typeof newValue.enabled === "boolean") changes.push(newValue.enabled ? "enabled" : "disabled");
  if (JSON.stringify(oldValue.targetUsers) !== JSON.stringify(newValue.targetUsers) && Array.isArray(newValue.targetUsers)) changes.push(`${newValue.targetUsers.length} target${newValue.targetUsers.length === 1 ? "" : "s"}`);
  return changes.length > 0 ? `${entry.flagName} · ${changes.join(", ")}` : `${entry.flagName} · version ${entry.version}`;
}

function recordValue(value: unknown): Record<string, unknown> {
  return value && typeof value === "object" && !Array.isArray(value) ? value as Record<string, unknown> : {};
}

function formatRelativeTime(value: string) {
  const time = Date.parse(value);
  if (!Number.isFinite(time)) return "—";
  const seconds = Math.max(0, Math.floor((Date.now() - time) / 1000));
  if (seconds < 60) return `${seconds}s ago`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours}h ago`;
  return `${Math.floor(hours / 24)}d ago`;
}
