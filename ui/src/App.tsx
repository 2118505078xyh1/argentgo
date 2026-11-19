import {
  For,
  Show,
  createEffect,
  createMemo,
  createSignal,
  onCleanup,
  onMount,
} from "solid-js";
import { invoke } from "./ipc-bridge";
import Markdown from "./Markdown";
import Automations from "./Automations";
import Documents from "./Documents";
import Memory from "./Memory";
import Extensions from "./Extensions";
import Usage from "./Usage";
import Git from "./Git";
import Jobs from "./Jobs";
import Workflows from "./Workflows";
import Terminal from "./Terminal";
import Workspace from "./Workspace";
import Plan from "./Plan";
import ToolCard from "./ToolCard";
import { listen, type UnlistenFn } from "./ipc-bridge";
import {
  asNumber,
  asRecord,
  asString,
  parseIncoming,
  parseReady,
  THINKING_LEVELS,
  type AgentEvent,
  type Approval,
  type Incoming,
  type Item,
  type LoadedEntry,
  type ModelInfo,
  type Ready,
  type SessionRow,
  type TextItem,
  type UnknownRecord,
} from "./ipc";
import { formatCost, formatTokens, relativeStamp, sessionGroup } from "./format";
import { detectLocale, t, type Locale } from "./i18n";

type SearchHit = { path: string; title: string; line: number; snippet: string };

function storageGet(key: string): string | null {
  try {
    return localStorage.getItem(key);
  } catch {
    return null;
  }
}

function storageSet(key: string, value: string): void {
  try {
    localStorage.setItem(key, value);
  } catch {
    // Private modes can deny storage; selection still works for this run.
  }
}

function loadedItems(entries: LoadedEntry[]): Item[] {
  return entries.map((entry) => {
    if (entry.kind === "tool") {
      return {
        id: nextId++,
        kind: "tool",
        tool: {
          id: entry.callId,
          name: entry.name,
          args: entry.arguments,
          state: entry.state,
          output: entry.output,
          duration: entry.durationMs,
        },
      };
    }
    return { id: nextId++, kind: entry.kind, text: entry.text };
  });
}

const STATUS_SEGMENTS = [
  "status",
  "session",
  "provider",
  "model",
  "tokens",
  "cache",
  "cost",
  "requests",
  "warn",
  "channel",
] as const;

type StatusSegment = (typeof STATUS_SEGMENTS)[number];

function loadStatusSegments(): StatusSegment[] {
  try {
    const raw = JSON.parse(storageGet("argent.statusline") ?? "null");
    if (Array.isArray(raw)) {
      const valid = raw.filter((key): key is StatusSegment => STATUS_SEGMENTS.includes(key));
      if (valid.length) return valid;
    }
  } catch {
    // Fall through to defaults.
  }
  return [
    "status",
    "session",
    "provider",
    "model",
    "tokens",
    "cache",
    "cost",
    "requests",
    "warn",
    "channel",
  ];
}

const SLASH_COMMANDS = [
  "/new",
  "/rename",
  "/rewind",
  "/compact",
  "/model",
  "/thinking",
  "/cost",
  "/copy",
  "/export",
  "/help",
];

let nextId = 1;

function App() {
  type Tab = { id: string; title: string; path: string };
  const [tabs, setTabs] = createSignal<Tab[]>([{ id: "1", title: "新任务", path: "" }]);
  const [active, setActive] = createSignal("1");
  const [tabItems, setTabItems] = createSignal<Record<string, Item[]>>({});
  const [tabReady, setTabReady] = createSignal<Record<string, Ready>>({});
  const [tabBusy, setTabBusy] = createSignal<Record<string, boolean>>({});
  const [tabStatus, setTabStatus] = createSignal<Record<string, string>>({ "1": "starting" });
  const [tabPath, setTabPath] = createSignal<Record<string, string>>({});
  const [pausedByTab, setPausedByTab] = createSignal<Record<string, boolean>>({});
  const [input, setInput] = createSignal("");
  const [lastPrompt, setLastPrompt] = createSignal("");
  const [dragging, setDragging] = createSignal(false);
  const [searchOpen, setSearchOpen] = createSignal(false);
  const [searchQuery, setSearchQuery] = createSignal("");
  const [searchResults, setSearchResults] = createSignal<SearchHit[]>([]);
  const [paletteOpen, setPaletteOpen] = createSignal(false);
  const [paletteQuery, setPaletteQuery] = createSignal("");
  const [approvalQueue, setApprovalQueue] = createSignal<Approval[]>([]);
  const approval = () => approvalQueue()[0] ?? null;
  const [error, setError] = createSignal<string | null>(null);
  const [errors, setErrors] = createSignal<string[]>([]);

  const showSegment = (key: StatusSegment) => statusSegments().includes(key);

  const items = () => tabItems()[active()] ?? [];
  const setItemsFor = (sid: string, value: Item[] | ((prev: Item[]) => Item[])) =>
    setTabItems((prev) => ({
      ...prev,
      [sid]: typeof value === "function" ? value(prev[sid] ?? []) : value,
    }));
  const setItems = (value: Item[] | ((prev: Item[]) => Item[])) => setItemsFor(active(), value);

  const ready = () => tabReady()[active()] ?? null;
  const setReadyFor = (sid: string, value: Ready) =>
    setTabReady((prev) => ({ ...prev, [sid]: value }));
  const setReady = (value: Ready | ((prev: Ready | undefined) => Ready | undefined)) =>
    setTabReady((prev) => {
      const next = typeof value === "function" ? value(prev[active()]) : value;
      return next ? { ...prev, [active()]: next } : prev;
    });

  const busy = () => tabBusy()[active()] ?? false;
  const setBusyFor = (sid: string, value: boolean) =>
    setTabBusy((prev) => ({ ...prev, [sid]: value }));
  const setBusy = (value: boolean) => setBusyFor(active(), value);

  const status = () => tabStatus()[active()] ?? "ready";
  const setStatusFor = (sid: string, value: string) =>
    setTabStatus((prev) => ({ ...prev, [sid]: value }));
  const setStatus = (value: string) => setStatusFor(active(), value);

  const sessionPath = () => tabPath()[active()] ?? "";

  const paused = () => pausedByTab()[active()] ?? false;
  const setPausedFor = (sid: string, value: boolean) =>
    setPausedByTab((prev) => ({ ...prev, [sid]: value }));
  const setSessionPathFor = (sid: string, value: string) =>
    setTabPath((prev) => ({ ...prev, [sid]: value }));
  const setSessionPath = (value: string) => setSessionPathFor(active(), value);
  const [sessions, setSessions] = createSignal<SessionRow[]>([]);
  const [sessionQuery, setSessionQuery] = createSignal("");
  const [theme, setTheme] = createSignal<"dark" | "light">(
    storageGet("argent.theme") === "light" ? "light" : "dark",
  );
  const [screen, setScreen] = createSignal<
    | "chat"
    | "automations"
    | "documents"
    | "memory"
    | "extensions"
    | "usage"
    | "git"
    | "jobs"
    | "workflows"
    | "terminal"
    | "workspace"
    | "plan"
  >("chat");
  const [help, setHelp] = createSignal(false);
  const [locale, setLocale] = createSignal<Locale>(detectLocale());
  const [statusSegments, setStatusSegments] = createSignal<StatusSegment[]>(
    loadStatusSegments(),
  );
  const [channel, setChannel] = createSignal("stable");
  const [telemetry, setTelemetry] = createSignal({ enabled: false, consent: false });
  const [promptTokens, setPromptTokens] = createSignal(0);
  const [cachedTokens, setCachedTokens] = createSignal(0);
  const [requests, setRequests] = createSignal(0);
  const [cost, setCost] = createSignal(0);
  const [priced, setPriced] = createSignal(false);
  const [completionTokens, setCompletionTokens] = createSignal(0);
  const [totalTokens, setTotalTokens] = createSignal(0);
  const [follow, setFollow] = createSignal(true);
  const [scrollByTab, setScrollByTab] = createSignal<Record<string, number>>({});
  const [restoring, setRestoring] = createSignal(false);
  const [expandedTools, setExpandedTools] = createSignal<Record<string, boolean>>({});
  const [expandAllTools, setExpandAllTools] = createSignal(false);

  const filteredSessions = createMemo(() => {
    const query = sessionQuery().trim().toLowerCase();
    if (!query) return sessions();
    return sessions().filter((row) =>
      [row.title, row.cwd, row.path].some((field) => field.toLowerCase().includes(query)),
    );
  });

  const modelOptions = createMemo(() => {
    const models = ready()?.models ?? [];
    const current = ready()?.model;
    if (current && !models.some((model) => model.id === current)) {
      return [...models, { id: current, contextLength: ready()?.contextMax ?? 0 }];
    }
    return models;
  });

  const thinkingOptions = createMemo(() => {
    const levels = ready()?.thinkingLevels ?? [];
    return levels.length > 0 ? levels : THINKING_LEVELS;
  });

  const slashMatches = createMemo(() => {
    const value = input().trimStart();
    if (!value.startsWith("/")) return [];
    const query = value.slice(1).split(/\s/)[0].toLowerCase();
    return SLASH_COMMANDS.filter((command) => command.slice(1).startsWith(query)).slice(0, 8);
  });

  const mentionQuery = createMemo(() => /@([^\s@]*)$/.exec(input())?.[1] ?? null);
  const [fileOptions, setFileOptions] = createSignal<string[]>([]);
  createEffect(() => {
    const query = mentionQuery();
    if (query === null) {
      setFileOptions([]);
      return;
    }
    let cancelled = false;
    onCleanup(() => {
      cancelled = true;
    });
    void invoke<string[]>("list_files", { query })
      .then((files) => {
        if (!cancelled) setFileOptions(files.slice(0, 8));
      })
      .catch(() => setFileOptions([]));
  });

  createEffect(() => {
    const query = searchQuery();
    if (!searchOpen() || query.trim().length < 2) {
      setSearchResults([]);
      return;
    }
    let cancelled = false;
    onCleanup(() => {
      cancelled = true;
    });
    void invoke<SearchHit[]>("search_sessions", { query, limit: 30 })
      .then((hits) => {
        if (!cancelled) setSearchResults(hits);
      })
      .catch(() => setSearchResults([]));
  });

  let viewport!: HTMLDivElement;
  let composer!: HTMLTextAreaElement;
  let importInput!: HTMLInputElement;

  const append = (kind: TextItem["kind"], text: string, stream = false, sid = active()) => {
    setItemsFor(sid, (prev) => {
      const last = prev[prev.length - 1];
      if (stream && last && last.kind === kind) {
        return [...prev.slice(0, -1), { ...last, text: last.text + text }];
      }
      return [...prev, { id: nextId++, kind, text }];
    });
  };

  // Token deltas can arrive far faster than a display frame. Coalesce them so
  // a long answer updates the transcript once per animation frame.
  const streamBuffer = new Map<string, { kind: TextItem["kind"]; text: string; sid: string }>();
  let streamFrame = 0;
  const flushStream = () => {
    for (const [key, value] of streamBuffer) {
      append(value.kind, value.text, true, value.sid);
      streamBuffer.delete(key);
    }
  };
  const appendStream = (kind: TextItem["kind"], text: string, sid: string) => {
    const key = `${sid}:${kind}`;
    const pending = streamBuffer.get(key);
    if (pending) {
      pending.text += text;
    } else {
      streamBuffer.set(key, { kind, text, sid });
    }
    if (!streamFrame) {
      streamFrame = requestAnimationFrame(() => {
        streamFrame = 0;
        flushStream();
      });
    }
  };

  const applyReady = (value: Ready, sid = active()) => {
    setReadyFor(sid, value);
    setStatusFor(sid, "ready");
  };

  const resetMetrics = () => {
    setPromptTokens(0);
    setCompletionTokens(0);
    setTotalTokens(0);
    setCachedTokens(0);
    setRequests(0);
    setCost(0);
    setPriced(false);
  };

  const handleAgent = (event: AgentEvent, sid = active()) => {
    switch (event.type) {
      case "turn_start":
        setStatusFor(sid, "thinking");
        break;
      case "assistant_text":
        appendStream("assistant", event.text, sid);
        break;
      case "thinking":
        appendStream("thinking", event.text, sid);
        break;
      case "tool_requested":
        setItemsFor(sid, (prev) => [
          ...prev,
          {
            id: nextId++,
            kind: "tool",
            tool: {
              id: event.id,
              name: event.name,
              args: event.arguments,
              state: "running",
              output: "",
            },
          },
        ]);
        break;
      case "tool_finished":
        setItemsFor(sid, (prev) =>
          prev.map((item) => {
            if (item.kind !== "tool" || item.tool.id !== event.id) return item;
            return {
              ...item,
              tool: {
                ...item.tool,
                state: event.isError ? "error" : "ok",
                output: event.output,
                duration: event.durationMs,
              },
            };
          }),
        );
        break;
      case "finished":
        setBusyFor(sid, false);
        setStatusFor(sid, event.reason);
        break;
      case "aborted":
        setBusyFor(sid, false);
        setStatusFor(sid, "aborted");
        break;
      case "notice":
        append("notice", event.text, false, sid);
        break;
      default:
        break;
    }
  };

  const handle = (payload: Incoming) => {
    const sid = payload.sid || active();
    const isActive = sid === active();
    switch (payload.kind) {
      case "ready":
        applyReady(payload, sid);
        break;
      case "agent":
        handleAgent(payload.event, sid);
        break;
      case "turn_done":
        flushStream();
        setPausedFor(sid, false);
        setBusyFor(sid, false);
        setStatusFor(sid, payload.reason);
        if (isActive) {
          if (payload.promptTokens !== undefined) setPromptTokens(payload.promptTokens);
          if (payload.completionTokens !== undefined)
            setCompletionTokens(payload.completionTokens);
          if (payload.totalTokens !== undefined) setTotalTokens(payload.totalTokens);
          if (payload.cachedTokens !== undefined) setCachedTokens(payload.cachedTokens);
          if (payload.requests !== undefined) setRequests(payload.requests);
          if (payload.cost !== undefined) setCost(payload.cost);
          if (payload.priced !== undefined) setPriced(payload.priced);
          if (
            document.hidden &&
            "Notification" in window &&
            Notification.permission === "granted"
          ) {
            try {
              new Notification("Argent 回合完成", { body: payload.reason });
            } catch {
              // Some WebViews expose the API but refuse to display a toast.
            }
          }
        }
        break;
      case "approval":
        if (isActive) setApprovalQueue((prev) => [...prev, { ...payload, sid }]);
        break;
      case "session":
        setSessionPathFor(sid, payload.path);
        setTabs((prev) =>
          prev.map((tab) =>
            tab.id === sid
              ? {
                  ...tab,
                  path: payload.path,
                  title: sessions().find((row) => row.path === payload.path)?.title || tab.title,
                }
              : tab,
          ),
        );
        break;
      case "session_cleared":
        flushStream();
        setItemsFor(sid, []);
        setSessionPathFor(sid, "");
        setTabs((prev) =>
          prev.map((tab) =>
            tab.id === sid ? { ...tab, path: "", title: "新任务" } : tab,
          ),
        );
        if (isActive) resetMetrics();
        break;
      case "sessions":
        setSessions(payload.sessions);
        break;
      case "loaded":
        flushStream();
        setItemsFor(sid, loadedItems(payload.entries));
        if (isActive) {
          setError(null);
          setStatus("ready");
          setFollow(true);
        }
        break;
      case "error":
        if (isActive) {
          setError(payload.message);
          setErrors((prev) => [...prev.slice(-19), payload.message]);
          setBusy(false);
          setStatus("error");
        }
        break;
    }
  };

  onMount(async () => {
    let unlisten: UnlistenFn | undefined;
    try {
      unlisten = await listen<unknown>("argent://event", (event) => {
        const payload = parseIncoming(event.payload);
        if (payload) {
          handle({ ...payload, sid: asString(asRecord(event.payload)?.sid) });
        }
      });
      let unlistenDeep: UnlistenFn | undefined;
      try {
        unlistenDeep = await listen<string[]>("deep-link://new-url", (event) => {
          const url = event.payload?.[0];
          if (!url) return;
          try {
            const path = new URL(url).searchParams.get("path");
            if (path) {
              void openSearchHit({ path, title: "Deep link", line: 0, snippet: "" });
            }
          } catch {
            setError(`无效的 deep link：${url}`);
          }
        });
      } catch {
        // Deep-link permission may be disabled in a stripped-down build.
      }
      onCleanup(() => unlistenDeep?.());
      const state = await invoke<unknown>("get_state");
      const stateRecord = asRecord(state);
      const initialSid = asString(stateRecord?.sid, "1");
      if (initialSid) setActive(initialSid);
      setChannel(asString(stateRecord?.channel, "stable"));
      try {
        setTelemetry(
          await invoke<{ enabled: boolean; consent: boolean }>("get_telemetry_preferences"),
        );
      } catch {
        // Settings file may be absent; defaults are both off.
      }
      const readyRecord = stateRecord ? asRecord(stateRecord.ready) : null;
      if (readyRecord) {
        const parsed = parseReady(readyRecord);
        applyReady(parsed);
        const savedModel = storageGet("argent.model");
        if (savedModel && savedModel !== parsed.model) {
          try {
            await invoke("set_model", { sid: initialSid, model: savedModel });
            const info = parsed.models.find((entry) => entry.id === savedModel);
            setReady((prev) =>
              prev
                ? {
                    ...prev,
                    model: savedModel,
                    contextMax:
                      info && info.contextLength > 0 ? info.contextLength : prev.contextMax,
                  }
                : prev,
            );
          } catch (err) {
            setError(String(err));
          }
        }
        const savedThinking = storageGet("argent.thinking");
        if (
          savedThinking &&
          THINKING_LEVELS.includes(savedThinking) &&
          savedThinking !== parsed.level
        ) {
          try {
            await invoke("set_thinking", { sid: initialSid, level: savedThinking });
            setReady((prev) => (prev ? { ...prev, level: savedThinking } : prev));
          } catch (err) {
            setError(String(err));
          }
        }
      }
    } catch (err) {
      setError(String(err));
    }
    onCleanup(() => unlisten?.());
  });

  onMount(() => {
    const onKey = (event: KeyboardEvent) => {
      const target = event.target as HTMLElement | null;
      const typing = !!target && ["INPUT", "TEXTAREA", "SELECT"].includes(target.tagName);
      if ((event.ctrlKey || event.metaKey) && event.shiftKey && event.key.toLowerCase() === "p") {
        event.preventDefault();
        setPaletteOpen(true);
        setHelp(false);
        return;
      }
      if ((event.ctrlKey || event.metaKey) && event.shiftKey && event.key.toLowerCase() === "f") {
        event.preventDefault();
        setSearchOpen(true);
        setHelp(false);
        return;
      }
      if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === "k") {
        event.preventDefault();
        setScreen("chat");
        setHelp(false);
        requestAnimationFrame(() => {
          try {
            composer.focus();
          } catch {
            // The composer may still be mounting after a screen switch.
          }
        });
        return;
      }
      if (approval()) {
        if (typing) return;
        const key = event.key.toLowerCase();
        if (key === "y" || key === "enter") {
          event.preventDefault();
          void answer(true);
        } else if (key === "n" || key === "escape") {
          event.preventDefault();
          void answer(false);
        }
        return;
      }
      if (event.key === "Escape") {
        if (help()) {
          setHelp(false);
        } else if (busy()) {
          event.preventDefault();
          void abort();
        }
      }
    };
    window.addEventListener("keydown", onKey);
    onCleanup(() => window.removeEventListener("keydown", onKey));
  });

  createEffect(() => {
    document.documentElement.dataset.theme = theme();
    document.documentElement.lang = locale();
    storageSet("argent.theme", theme());
    storageSet("argent.locale", locale());
    storageSet("argent.statusline", JSON.stringify(statusSegments()));
  });

  createEffect(() => {
    items();
    if (follow() && viewport && !restoring()) {
      requestAnimationFrame(() => {
        viewport.scrollTop = viewport.scrollHeight;
      });
    }
  });

  const send = async () => {
    const text = input().trim();
    if (!text) return;
    setLastPrompt(text);
    setInput("");
    setError(null);
    composer.style.height = "auto";

    if (text.startsWith("/")) {
      await runCommand(text);
      return;
    }

    // While a turn is running, Enter queues a correction instead of starting a
    // second prompt. The agent drains it at the next tool boundary.
    if (busy()) {
      try {
        await invoke("steer", { sid: active(), text });
        append("notice", "插话已排队，将在下一个工具边界注入");
      } catch (err) {
        setError(String(err));
      }
      return;
    }

    setItems((prev) => [...prev, { id: nextId++, kind: "user", text }]);
    setBusy(true);
    setStatus("working");
    try {
      await invoke("send_prompt", { sid: active(), text });
    } catch (err) {
      setError(String(err));
      setBusy(false);
      setStatus("error");
    }
  };

  const addDocumentToChat = (text: string) => {
    setScreen("chat");
    setInput(text);
    setError(null);
    requestAnimationFrame(() => {
      try {
        composer.focus();
      } catch {
        // The composer is remounted with the chat screen; focusing is cosmetic.
      }
    });
  };

  const abort = async () => {
    try {
      await invoke("abort", { sid: active() });
      setPausedFor(active(), false);
    } catch (err) {
      setError(String(err));
    }
  };

  const pause = async () => {
    try {
      await invoke("pause_session", { sid: active() });
      setPausedFor(active(), true);
      setStatus("已暂停");
    } catch (err) {
      setError(String(err));
    }
  };

  const resume = async () => {
    try {
      await invoke("resume_session", { sid: active() });
      setPausedFor(active(), false);
      setStatus("运行中");
    } catch (err) {
      setError(String(err));
    }
  };

  const addTab = async (): Promise<string | undefined> => {
    try {
      const sid = await invoke<string>("new_tab");
      setTabs((prev) => [...prev, { id: sid, title: "新任务", path: "" }]);
      setActive(sid);
      setBusyFor(sid, false);
      setStatusFor(sid, "starting");
      setItemsFor(sid, []);
      setApprovalQueue([]);
      setError(null);
      resetMetrics();
      return sid;
    } catch (err) {
      setError(String(err));
      return undefined;
    }
  };

  const importSession = async (event: Event) => {
    const element = event.currentTarget as HTMLInputElement;
    const file = element.files?.[0];
    if (!file) return;
    try {
      const text = await file.text();
      const title = file.name.replace(/\.jsonl?$/i, "");
      const path = await invoke<string>("import_session_text", { text, title });
      const sid = await addTab();
      if (sid) {
        await invoke("open_session", { sid, path });
        setTabs((prev) =>
          prev.map((tab) => (tab.id === sid ? { ...tab, path, title } : tab)),
        );
      }
      setHelp(false);
    } catch (err) {
      setError(String(err));
    }
    element.value = "";
  };

  const openSearchHit = async (hit: SearchHit) => {
    setSearchOpen(false);
    const existing = tabs().find((tab) => tab.path === hit.path);
    const sid = existing?.id ?? (await addTab());
    if (!sid) return;
    try {
      await invoke("open_session", { sid, path: hit.path });
      if (!existing) {
        setTabs((prev) =>
          prev.map((tab) =>
            tab.id === sid ? { ...tab, path: hit.path, title: hit.title } : tab,
          ),
        );
      }
      await switchTab(sid);
    } catch (err) {
      setError(String(err));
    }
  };

  const switchTab = async (sid: string) => {
    if (sid === active()) return;
    try {
      if (viewport) {
        setScrollByTab((prev) => ({ ...prev, [active()]: viewport.scrollTop }));
      }
      await invoke("activate_tab", { sid });
      setRestoring(true);
      setActive(sid);
      setApprovalQueue([]);
      setError(null);
      resetMetrics();
      requestAnimationFrame(() => {
        if (viewport) {
          viewport.scrollTop = scrollByTab()[sid] ?? viewport.scrollHeight;
        }
        setRestoring(false);
      });
    } catch (err) {
      setRestoring(false);
      setError(String(err));
    }
  };

  const closeTab = async (sid: string) => {
    if (tabs().length <= 1) return;
    try {
      await invoke("close_tab", { sid });
      const remaining = tabs().filter((tab) => tab.id !== sid);
      setTabs(remaining);
      if (active() === sid && remaining.length > 0) {
        setActive(remaining[0].id);
        setApprovalQueue([]);
        resetMetrics();
      }
    } catch (err) {
      setError(String(err));
    }
  };

  const newSession = async () => {
    if (busy()) return;
    try {
      // The worker emits SessionCleared; clearing locally here would race with
      // that event and can drop a freshly loaded transcript.
      await invoke("new_session", { sid: active() });
    } catch (err) {
      setError(String(err));
    }
  };

  const rewind = async (steps = 1) => {
    if (busy()) return;
    if (!window.confirm(`撤销最近 ${steps} 轮的文件改动？`)) return;
    try {
      await invoke("rewind", { sid: active(), steps });
    } catch (err) {
      setError(String(err));
    }
  };

  const chooseModel = async (model: string) => {
    try {
      await invoke("set_model", { sid: active(), model });
      const info = ready()?.models.find((entry) => entry.id === model);
      setReady((prev) =>
        prev
          ? {
              ...prev,
              model,
              contextMax: info?.contextLength && info.contextLength > 0
                ? info.contextLength
                : prev.contextMax,
            }
          : prev,
      );
      storageSet("argent.model", model);
    } catch (err) {
      setError(String(err));
    }
  };

  const chooseModelCustom = async () => {
    const model = window.prompt("模型 ID", ready()?.model ?? "")?.trim();
    if (model) await chooseModel(model);
  };

  const chooseThinking = async (level: string) => {
    try {
      await invoke("set_thinking", { sid: active(), level });
      setReady((prev) => (prev ? { ...prev, level } : prev));
      storageSet("argent.thinking", level);
    } catch (err) {
      setError(String(err));
    }
  };

  const openSession = async (path: string) => {
    if (busy()) return;
    try {
      // Loaded + Session events from the worker replace the transcript. Doing
      // it here as well creates a race that can empty a just-reloaded tab.
      await invoke("open_session", { sid: active(), path });
    } catch (err) {
      setError(String(err));
    }
  };

  const renameSession = async (row: SessionRow) => {
    const title = window.prompt("会话名称", row.title);
    if (!title || !title.trim()) return;
    try {
      await invoke("rename_session", { sid: active(), path: row.path, title: title.trim() });
      setSessions((prev) =>
        prev.map((item) =>
          item.path === row.path ? { ...item, title: title.trim() } : item,
        ),
      );
    } catch (err) {
      setError(String(err));
    }
  };

  const deleteSession = async (row: SessionRow) => {
    if (!window.confirm(`删除会话「${row.title || "未命名"}」？`)) return;
    try {
      await invoke("delete_session", { sid: active(), path: row.path });
      setSessions((prev) => prev.filter((item) => item.path !== row.path));
      if (sessionPath() === row.path) setSessionPath("");
    } catch (err) {
      setError(String(err));
    }
  };

  const approvalDanger = () => {
    const name = approval()?.name.toLowerCase() ?? "";
    return /bash|write|edit|delete|remove|patch|move|copy|exec|apply|shell/.test(name);
  };

  const answer = async (allow: boolean, always = false) => {
    const pending = approval();
    if (!pending) return;
    const sid = pending.sid || active();
    setApprovalQueue((prev) => prev.slice(1));
    try {
      if (always) {
        await invoke("allow_tool", { sid, name: pending.name });
      }
      await invoke("answer_approval", {
        sid,
        id: pending.id,
        allow,
      });
    } catch (err) {
      setError(String(err));
    }
  };

  const currentRow = (): SessionRow | undefined =>
    sessions().find((row) => row.path === sessionPath());

  const renameCurrent = async (titleArg: string) => {
    const path = sessionPath();
    if (!path) {
      setError("当前还没有会话文件，先发一条消息");
      return;
    }
    const title = titleArg.trim() || window.prompt("会话名称", currentRow()?.title ?? "")?.trim();
    if (!title) return;
    try {
      await invoke("rename_session", { sid: active(), path, title });
      setSessions((prev) =>
        prev.map((item) => (item.path === path ? { ...item, title } : item)),
      );
    } catch (err) {
      setError(String(err));
    }
  };

  const lastAssistantText = (): string => {
    const all = items();
    for (let index = all.length - 1; index >= 0; index -= 1) {
      const item = all[index];
      if (item.kind === "assistant") return item.text;
    }
    return "";
  };

  const exportTranscript = (format: "md" | "json") => {
    const all = items();
    const body =
      format === "json"
        ? JSON.stringify(all, null, 2)
        : all
            .map((item) => {
              if (item.kind === "tool") {
                return `> ${item.tool.name} ${item.tool.args}\n>\n> ${item.tool.output.replace(/\n/g, "\n> ")}`;
              }
              const prefix =
                item.kind === "user"
                  ? "## 你"
                  : item.kind === "assistant"
                    ? "## argent"
                    : item.kind === "thinking"
                      ? "## thinking"
                      : "## notice";
              return `${prefix}\n\n${item.text}`;
            })
            .join("\n\n");
    const blob = new Blob([body], {
      type: format === "json" ? "application/json" : "text/markdown",
    });
    const url = URL.createObjectURL(blob);
    const anchor = document.createElement("a");
    anchor.href = url;
    anchor.download = `argent-session-${active()}.${format === "json" ? "json" : "md"}`;
    anchor.click();
    URL.revokeObjectURL(url);
  };

  const copyText = async (text: string) => {
    try {
      await navigator.clipboard.writeText(text);
      append("notice", "已复制");
    } catch (err) {
      setError(`复制失败：${String(err)}`);
    }
  };

  const copyLast = async () => {
    const text = lastAssistantText();
    if (!text) {
      setError("还没有可复制的助手回复");
      return;
    }
    try {
      await navigator.clipboard.writeText(text);
      append("notice", "已复制最后一条助手回复");
    } catch (err) {
      setError(`复制失败：${String(err)}`);
    }
  };

  const costSummary = (): string => {
    const max = ready()?.contextMax ?? 0;
    const used = promptTokens();
    const percent = max > 0 ? Math.min((used * 100) / max, 100).toFixed(0) : "—";
    const spend = priced() ? formatCost(cost()) : "未配置单价";
    return [
      `上下文 ${formatTokens(used)} / ${formatTokens(max)} (${percent}%)`,
      `请求 ${requests()} · 花费 ${spend}`,
      `prompt ${formatTokens(promptTokens())} · completion ${formatTokens(completionTokens())} · cached ${formatTokens(cachedTokens())} · total ${formatTokens(totalTokens())}`,
    ].join("\n");
  };

  const runCommand = async (text: string) => {
    const match = /^\/([a-zA-Z-]+)\s*(.*)$/.exec(text);
    if (!match) {
      setError("命令格式：/help");
      return;
    }
    const name = match[1].toLowerCase();
    const args = match[2].trim();
    switch (name) {
      case "new":
        if (busy()) {
          setError("运行中不能新建会话，先停止或等待完成");
          return;
        }
        await newSession();
        return;
      case "rename":
        await renameCurrent(args);
        return;
      case "rewind": {
        const steps = args ? Number.parseInt(args, 10) : 1;
        if (!Number.isFinite(steps) || steps <= 0) {
          setError("用法：/rewind [正整数]");
          return;
        }
        await rewind(steps);
        return;
      }
      case "model":
        if (!args) {
          append(
            "notice",
            `当前模型 ${ready()?.model || "—"} · 可用 ${ready()?.models?.map((model) => model.id).join(", ") || "—"}`,
          );
          return;
        }
        await chooseModel(args);
        return;
      case "thinking": {
        const level = args.toLowerCase();
        if (!level) {
          append(
            "notice",
            `当前思考档位 ${ready()?.level || "auto"} · 可选 ${THINKING_LEVELS.join(", ")}`,
          );
          return;
        }
        if (!thinkingOptions().includes(level)) {
          setError(`未知思考档位 ${level}，可选 ${thinkingOptions().join(", ")}`);
          return;
        }
        await chooseThinking(level);
        return;
      }
      case "compact":
        if (busy()) {
          setError("运行中不能压缩上下文，等待本轮结束");
          return;
        }
        try {
          await invoke("compact", { sid: active() });
        } catch (err) {
          setError(String(err));
        }
        return;
      case "cost":
        append("notice", costSummary());
        return;
      case "copy":
        await copyLast();
        return;
      case "export":
        exportTranscript(args.toLowerCase() === "json" ? "json" : "md");
        return;
      case "help":
        append(
          "notice",
          "命令：/new /rename [标题] /rewind [n] /compact /model [id] /thinking [档位] /cost /copy /export [md|json] /help",
        );
        return;
      default:
        setError(`未知命令 /${name}，输入 /help 查看可用命令`);
    }
  };

  const onComposerKey = (event: KeyboardEvent) => {
    // `isComposing` is the standard guard; keyCode 229 is the fallback used by
    // some IMEs that do not set it on Windows WebView2.
    if (event.isComposing || event.keyCode === 229) return;
    if (event.key === "Enter" && !event.shiftKey) {
      event.preventDefault();
      void send();
    }
  };

  const onDrop = async (event: DragEvent) => {
    event.preventDefault();
    setDragging(false);
    const files = Array.from(event.dataTransfer?.files ?? []);
    const parts: string[] = [];
    for (const file of files) {
      try {
        const text = await file.text();
        parts.push(`【${file.name}】\n${text.slice(0, 20_000)}`);
      } catch (err) {
        setError(`读取附件失败：${String(err)}`);
      }
    }
    if (parts.length) {
      setInput((prev) => `${prev}${prev ? "\n" : ""}${parts.join("\n\n")}`);
    }
  };

  const resizeComposer = (event: InputEvent) => {
    const target = event.currentTarget as HTMLTextAreaElement | null;
    if (!target) return;
    setInput(target.value);
    target.style.height = "auto";
    target.style.height = `${Math.min(target.scrollHeight, 180)}px`;
  };

  const setTelemetryPrefs = async (patch: Partial<{ enabled: boolean; consent: boolean }>) => {
    const next = { ...telemetry(), ...patch };
    setTelemetry(next);
    try {
      await invoke("set_telemetry_preferences", next);
    } catch (err) {
      setError(String(err));
    }
  };

  const paletteActions = () => {
    const query = paletteQuery().trim().toLowerCase();
    const actions = [
      { label: "新建会话标签", run: () => void addTab() },
      { label: "当前标签新建会话", run: () => void newSession() },
      {
        label: "跨会话搜索",
        run: () => {
          setSearchOpen(true);
          setPaletteOpen(false);
        },
      },
      {
        label: "打开定时任务",
        run: () => {
          setScreen("automations");
          setPaletteOpen(false);
        },
      },
      {
        label: "打开文档",
        run: () => {
          setScreen("documents");
          setPaletteOpen(false);
        },
      },
      {
        label: "导出 Markdown",
        run: () => {
          exportTranscript("md");
          setPaletteOpen(false);
        },
      },
      {
        label: "压缩上下文",
        run: () => {
          void invoke("compact", { sid: active() });
          setPaletteOpen(false);
        },
      },
      { label: "撤销上一轮", run: () => void rewind() },
      {
        label: "切换主题",
        run: () => {
          setTheme(theme() === "dark" ? "light" : "dark");
          setPaletteOpen(false);
        },
      },
      {
        label: "快捷键帮助",
        run: () => {
          setHelp(true);
          setPaletteOpen(false);
        },
      },
    ];
    return query
      ? actions.filter((action) => action.label.toLowerCase().includes(query)).slice(0, 10)
      : actions;
  };

  return (
    <div class="app">
      <aside class="sidebar">
        <div class="brand">argent</div>
        <div class="side-tabs">
          <button
            classList={{ active: screen() === "chat" }}
            onClick={() => setScreen("chat")}
          >
            {t(locale(), "chat")}
          </button>
          <button
            classList={{ active: screen() === "automations" }}
            onClick={() => setScreen("automations")}
          >
            {t(locale(), "automations")}
          </button>
          <button
            classList={{ active: screen() === "documents" }}
            onClick={() => setScreen("documents")}
          >
            {t(locale(), "documents")}
          </button>
          <button
            classList={{ active: screen() === "memory" }}
            onClick={() => setScreen("memory")}
          >
            {t(locale(), "memory")}
          </button>
          <button
            classList={{ active: screen() === "extensions" }}
            onClick={() => setScreen("extensions")}
          >
            {t(locale(), "extensions")}
          </button>
          <button
            classList={{ active: screen() === "usage" }}
            onClick={() => setScreen("usage")}
          >
            {t(locale(), "usage")}
          </button>
          <button
            classList={{ active: screen() === "git" }}
            onClick={() => setScreen("git")}
          >
            {t(locale(), "git")}
          </button>
          <button
            classList={{ active: screen() === "jobs" }}
            onClick={() => setScreen("jobs")}
          >
            任务
          </button>
          <button
            classList={{ active: screen() === "workflows" }}
            onClick={() => setScreen("workflows")}
          >
            工作流
          </button>
          <button
            classList={{ active: screen() === "terminal" }}
            onClick={() => setScreen("terminal")}
          >
            终端
          </button>
          <button
            classList={{ active: screen() === "workspace" }}
            onClick={() => setScreen("workspace")}
          >
            文件
          </button>
          <button
            classList={{ active: screen() === "plan" }}
            onClick={() => setScreen("plan")}
          >
            计划
          </button>
        </div>
        <div class="side-head">
          <span>{t(locale(), "chat")}</span>
          <button class="icon" title="新任务" onClick={() => void newSession()}>
            ＋
          </button>
        </div>
        <div class="side-search-wrap">
          <input
            class="side-search"
            placeholder={t(locale(), "searchSessions")}
            value={sessionQuery()}
            onInput={(event) => setSessionQuery(event.currentTarget.value)}
          />
        </div>
        <nav class="side-list">
          <Show
            when={sessions().length > 0}
            fallback={
              <button class="side-item active">
                <span class="side-title">{t(locale(), "newTask")}</span>
                <span class="side-meta">{sessionPath() || ready()?.cwd || "新任务"}</span>
              </button>
            }
          >
            <Show
              when={filteredSessions().length > 0}
              fallback={<div class="side-empty">没有匹配的会话</div>}
            >
              <For each={["今天", "昨天", "更早"]}>
                {(group) => {
                  const rows = () =>
                    filteredSessions().filter((row) => sessionGroup(row.updated) === group);
                  return (
                    <Show when={rows().length > 0}>
                      <div class="side-group">{group}</div>
                      <For each={rows()}>
                        {(row) => (
                          <div
                            class="side-item"
                            classList={{ active: row.path === sessionPath() }}
                          >
                            <button
                              class="side-open"
                              title={`${row.title || "未命名会话"}\n${row.path}`}
                              onClick={() => void openSession(row.path)}
                            >
                              <span class="side-title">{row.title || "未命名会话"}</span>
                              <span class="side-meta">
                                {relativeStamp(row.updated) || row.path}
                              </span>
                            </button>
                            <span class="side-actions">
                              <button
                                class="icon"
                                title="重命名"
                                onClick={() => void renameSession(row)}
                              >
                                ✎
                              </button>
                              <button
                                class="icon"
                                title="删除"
                                onClick={() => void deleteSession(row)}
                              >
                                ×
                              </button>
                            </span>
                          </div>
                        )}
                      </For>
                    </Show>
                  );
                }}
              </For>
            </Show>
          </Show>
        </nav>
        <div class="side-foot">
          <span class="dot" classList={{ live: busy() }} />
          {busy() ? t(locale(), "running") : t(locale(), "ready")}
        </div>
      </aside>

      <main class="main">
        <Show when={screen() === "chat"}>
        <div class="chat-tabs">
          <For each={tabs()}>
            {(tab) => (
              <div class="chat-tab" classList={{ active: tab.id === active() }}>
                <button class="tab-label" onClick={() => void switchTab(tab.id)}>
                  <span class="tab-dot" classList={{ live: tab.id === active() && busy() }} />
                  {tab.title}
                </button>
                <Show when={tabs().length > 1}>
                  <button
                    class="tab-close"
                    title="关闭标签"
                    onClick={() => void closeTab(tab.id)}
                  >
                    ×
                  </button>
                </Show>
              </div>
            )}
          </For>
          <button
            class="icon"
            title={t(locale(), "newTab")}
            onClick={() => void addTab()}
          >
            ＋
          </button>
          <span class="spacer" />
          <button
            class="ghost tab-tools"
            onClick={() => setExpandAllTools((value) => !value)}
          >
            {expandAllTools() ? "收起工具" : "展开工具"}
          </button>
        </div>
        <header class="topbar">
          <div class="title-block">
            <div class="title">Argent</div>
            <div class="path">{ready()?.cwd || ""}</div>
          </div>
          <div class="top-meta">
            <span class="label">{t(locale(), "model")}</span>
            <select
              class="pill select"
              value={ready()?.model || ""}
              onChange={(event) => void chooseModel(event.currentTarget.value)}
            >
              <For each={modelOptions()}>
                {(model) => (
                  <option value={model.id}>
                    {model.id}
                    {model.contextLength > 0 ? ` · ${formatTokens(model.contextLength)}` : ""}
                  </option>
                )}
              </For>
            </select>
            <button
              class="icon"
              title="手动输入模型 ID"
              onClick={() => void chooseModelCustom()}
            >
              ＋
            </button>
            <span class="label">{t(locale(), "thinking")}</span>
            <select
              class="pill select"
              value={ready()?.level || "auto"}
              onChange={(event) => void chooseThinking(event.currentTarget.value)}
            >
              <For each={thinkingOptions()}>
                {(level) => <option value={level}>{level}</option>}
              </For>
            </select>
            <button
              class="ghost"
              title="撤销最近一轮 write/edit"
              disabled={busy()}
              onClick={() => void rewind()}
            >
              ↶ {t(locale(), "rewind")}
            </button>
            <button
              class="icon"
              title={theme() === "dark" ? "切换到浅色主题" : "切换到深色主题"}
              onClick={() => setTheme(theme() === "dark" ? "light" : "dark")}
            >
              {theme() === "dark" ? "☀" : "☾"}
            </button>
            <button class="icon" title="快捷键帮助" onClick={() => setHelp(true)}>
              ?
            </button>
          </div>
        </header>

        <section
          class="transcript"
          role="log"
          aria-live="polite"
          ref={viewport}
          onScroll={() => {
            const atEnd =
              viewport.scrollHeight - viewport.scrollTop - viewport.clientHeight < 48;
            setFollow(atEnd);
          }}
        >
          <Show
            when={items().length > 0}
            fallback={
              <div class="welcome">
                <div class="welcome-card">
                  <div class="welcome-mark">argent</div>
                  <p>{t(locale(), "welcome")}</p>
                  <div class="path">{ready()?.cwd || ""}</div>
                  <p class="welcome-hint">
                    首次使用请在终端运行 <code>argent login</code> 配置模型凭据。
                  </p>
                  <div class="shortcuts">
                    <button
                      onClick={() =>
                        setInput(
                          "Read the repository structure and summarize the main modules.",
                        )
                      }
                    >
                      读懂仓库
                    </button>
                    <button
                      onClick={() =>
                        setInput(
                          "Find one likely bug in this repository and explain how to fix it.",
                        )
                      }
                    >
                      找一个 bug
                    </button>
                    <button
                      onClick={() =>
                        setInput("Explain the architecture of this repository.")
                      }
                    >
                      解释架构
                    </button>
                  </div>
                </div>
              </div>
            }
          >
            <For each={items()}>
              {(item, index) => {
                const stepsAfter = () =>
                  items()
                    .slice(index() + 1)
                    .filter((entry) => entry.kind === "user").length;
                if (item.kind === "tool") {
                  return (
                    <ToolCard
                      tool={item.tool}
                      expanded={expandAllTools() || (expandedTools()[item.tool.id] ?? false)}
                      onToggle={() =>
                        setExpandedTools((prev) => ({
                          ...prev,
                          [item.tool.id]: !(
                            expandAllTools() ||
                            (prev[item.tool.id] ?? false)
                          ),
                        }))
                      }
                    />
                  );
                }
                if (item.kind === "user") {
                  return (
                    <div class="user-row">
                      <div class="user-bubble">
                        <div class="role">
                          你
                          <button
                            class="msg-action"
                            title="复制这条消息"
                            onClick={() => void copyText(item.text)}
                          >
                            复制
                          </button>
                          <Show when={stepsAfter() > 0}>
                            <button
                              class="msg-action"
                              title="回滚到这条消息之前"
                              onClick={() => void rewind(stepsAfter())}
                            >
                              回滚到此
                            </button>
                          </Show>
                        </div>
                        <div class="text">{item.text}</div>
                      </div>
                    </div>
                  );
                }
                if (item.kind === "thinking") {
                  return (
                    <div class="thinking">
                      <div class="role">思考</div>
                      <div class="text">{item.text}</div>
                    </div>
                  );
                }
                if (item.kind === "notice") {
                  return <div class="notice">{item.text}</div>;
                }
                return (
                  <div class="assistant">
                    <div class="role">
                      argent
                      <button
                        class="msg-action"
                        title="复制这条回复"
                        onClick={() => void copyText(item.text)}
                      >
                        复制
                      </button>
                      <Show when={stepsAfter() > 0}>
                        <button
                          class="msg-action"
                          title="回滚到这条回复之前"
                          onClick={() => void rewind(stepsAfter())}
                        >
                          回滚到此
                        </button>
                      </Show>
                    </div>
                    <Markdown text={item.text} />
                  </div>
                );
              }}
            </For>
          </Show>
          <Show when={!follow() && items().length > 0}>
            <button
              class="jump-bottom"
              onClick={() => {
                setFollow(true);
                viewport.scrollTop = viewport.scrollHeight;
              }}
            >
              ↓ 回到底部
            </button>
          </Show>
          <div class="anchor" />
        </section>

        <Show when={approval()}>
          {(pending) => (
            <div class="approval">
              <div class="approval-head">
                <span class="approval-mark">!</span>
                <strong>{t(locale(), "approvalTitle")}</strong>
                <code>{pending().name}</code>
                <span class="risk" classList={{ danger: approvalDanger() }}>
                  {approvalDanger() ? "会修改文件或执行命令" : "只读操作"}
                </span>
                <Show when={approvalQueue().length > 1}>
                  <span class="queue-count">还有 {approvalQueue().length - 1} 个待审</span>
                </Show>
              </div>
              <pre>{pending().arguments}</pre>
              <div class="approval-actions">
                <span class="hint">Y 允许 · N/Esc 拒绝</span>
                <button class="ghost" onClick={() => void answer(false)}>
                  {t(locale(), "deny")}
                </button>
                <button
                  class="ghost"
                  title="本次会话不再询问此工具"
                  onClick={() => void answer(true, true)}
                >
                  {t(locale(), "alwaysAllow")}
                </button>
                <button class="primary" onClick={() => void answer(true)}>
                  {t(locale(), "allow")}
                </button>
              </div>
            </div>
          )}
        </Show>

        <Show when={error()}>
          <div class="error-bar">
            <span>{error()}</span>
            <Show when={lastPrompt()}>
              <button
                class="ghost"
                onClick={() => {
                  setInput(lastPrompt());
                  void send();
                }}
              >
                重试
              </button>
            </Show>
            <button class="icon" onClick={() => setError(null)}>
              ×
            </button>
          </div>
        </Show>

        <footer
          class="composer-wrap"
          classList={{ dragging: dragging() }}
          onDragOver={(event) => {
            event.preventDefault();
            setDragging(true);
          }}
          onDragLeave={() => setDragging(false)}
          onDrop={(event) => void onDrop(event)}
        >
          <Show when={mentionQuery() !== null && fileOptions().length > 0}>
            <div class="slash-menu">
              <For each={fileOptions()}>
                {(file) => (
                  <button
                    onClick={() => {
                      setInput((prev) => prev.replace(/@([^\s@]*)$/, `@${file} `));
                      composer.focus();
                    }}
                  >
                    {file}
                  </button>
                )}
              </For>
            </div>
          </Show>
          <Show when={input().startsWith("/") && slashMatches().length > 0}>
            <div class="slash-menu">
              <For each={slashMatches()}>
                {(command) => (
                  <button
                    onClick={() => {
                      setInput(`${command} `);
                      composer.focus();
                    }}
                  >
                    {command}
                  </button>
                )}
              </For>
            </div>
          </Show>
          <div class="composer">
            <textarea
              ref={composer}
              value={input()}
              placeholder={t(locale(), busy() ? "steerPlaceholder" : "composerPlaceholder")}
              rows={1}
              onInput={resizeComposer}
              onKeyDown={onComposerKey}
            />
            <div class="composer-actions">
              <span class="hint">
                {t(locale(), busy() ? "steerHint" : "composerHint")}
              </span>
              <div class="buttons">
                <button class="ghost" disabled={!busy()} onClick={() => void abort()}>
                  {t(locale(), "stop")}
                </button>
                <button
                  class="ghost"
                  disabled={!busy()}
                  onClick={() => void (paused() ? resume() : pause())}
                >
                  {paused() ? "继续" : "暂停"}
                </button>
                <button
                  class="primary"
                  disabled={!input().trim()}
                  onClick={() => void send()}
                >
                  {t(locale(), busy() ? "steer" : "send")}
                </button>
              </div>
            </div>
          </div>
          <div class="statusbar">
            <span class="dot" classList={{ live: busy() }} />
            <Show when={showSegment("status")}>
              <span role="status" aria-live="polite">
                {paused() ? "已暂停" : status()}
              </span>
            </Show>
            <Show when={showSegment("session")}>
              <span class="sep">·</span>
              <span>{currentRow()?.title || (sessionPath() ? "未命名会话" : "新任务")}</span>
            </Show>
            <Show when={showSegment("provider")}>
              <span class="sep">·</span>
              <span>{ready()?.provider || "—"}</span>
            </Show>
            <Show when={showSegment("model")}>
              <span class="sep">·</span>
              <span>{ready()?.model || "—"}</span>
            </Show>
            <span class="spacer" />
            <Show when={showSegment("tokens")}>
              <span>
                ctx {formatTokens(promptTokens())} / {formatTokens(ready()?.contextMax ?? 0)}
              </span>
            </Show>
            <Show when={showSegment("cache") && cachedTokens() > 0 && promptTokens() > 0}>
              <span class="sep">·</span>
              <span>
                cache {Math.min((cachedTokens() * 100) / promptTokens(), 100).toFixed(0)}%
              </span>
            </Show>
            <Show when={showSegment("requests") && requests() > 0}>
              <span class="sep">·</span>
              <span>{requests()} req</span>
            </Show>
            <Show when={showSegment("cost") && cost() > 0}>
              <span class="sep">·</span>
              <span>{formatCost(cost())}</span>
            </Show>
            <Show
              when={
                showSegment("warn") &&
                (ready()?.contextMax ?? 0) > 0 &&
                (promptTokens() * 100) / (ready()?.contextMax ?? 1) >= 80
              }
            >
              <span class="sep">·</span>
              <span class="warn-text">上下文接近上限 · /compact</span>
            </Show>
            <Show when={showSegment("channel") && channel() !== "stable"}>
              <span class="sep">·</span>
              <span class="warn-text">{channel()}</span>
            </Show>
          </div>
        </footer>
        </Show>
        <Show when={screen() === "automations"}>
          <Automations />
        </Show>
        <Show when={screen() === "documents"}>
          <Documents onAddToChat={addDocumentToChat} />
        </Show>
        <Show when={screen() === "memory"}>
          <Memory />
        </Show>
        <Show when={screen() === "extensions"}>
          <Extensions />
        </Show>
        <Show when={screen() === "usage"}>
          <Usage />
        </Show>
        <Show when={screen() === "git"}>
          <Git onAddToChat={addDocumentToChat} />
        </Show>
        <Show when={screen() === "jobs"}>
          <Jobs />
        </Show>
        <Show when={screen() === "workflows"}>
          <Workflows />
        </Show>
        <Show when={screen() === "terminal"}>
          <Terminal />
        </Show>
        <Show when={screen() === "workspace"}>
          <Workspace />
        </Show>
        <Show when={screen() === "plan"}>
          <Plan items={items()} status={status()} errors={errors()} />
        </Show>
      </main>

      <Show when={paletteOpen()}>
        <div class="overlay" onClick={() => setPaletteOpen(false)}>
          <div class="search-panel palette" onClick={(event) => event.stopPropagation()}>
            <div class="search-head">
              <input
                autofocus
                placeholder="输入动作…"
                value={paletteQuery()}
                onInput={(event) => setPaletteQuery(event.currentTarget.value)}
              />
              <button class="icon" onClick={() => setPaletteOpen(false)}>
                ×
              </button>
            </div>
            <div class="search-results">
              <For
                each={paletteActions()}
                fallback={<div class="side-empty">没有匹配的动作。</div>}
              >
                {(action) => (
                  <button class="search-hit" onClick={action.run}>
                    <strong>{action.label}</strong>
                  </button>
                )}
              </For>
            </div>
          </div>
        </div>
      </Show>

      <Show when={searchOpen()}>
        <div class="overlay" onClick={() => setSearchOpen(false)}>
          <div class="search-panel" onClick={(event) => event.stopPropagation()}>
            <div class="search-head">
              <input
                autofocus
                placeholder="搜索所有会话…"
                value={searchQuery()}
                onInput={(event) => setSearchQuery(event.currentTarget.value)}
              />
              <button class="icon" onClick={() => setSearchOpen(false)}>
                ×
              </button>
            </div>
            <div class="search-results">
              <For
                each={searchResults()}
                fallback={<div class="side-empty">输入至少两个字符开始搜索。</div>}
              >
                {(hit) => (
                  <button class="search-hit" onClick={() => void openSearchHit(hit)}>
                    <strong>{hit.title}</strong>
                    <span>{hit.snippet}</span>
                  </button>
                )}
              </For>
            </div>
          </div>
        </div>
      </Show>

      <Show when={help()}>
        <div class="overlay" onClick={() => setHelp(false)}>
          <div class="help-panel" onClick={(event) => event.stopPropagation()}>
            <div class="log-head">
              <strong>快捷键与命令</strong>
              <button
                class="ghost"
                onClick={() => setLocale(locale() === "zh" ? "en" : "zh")}
              >
                中文 / EN
              </button>
              <button class="icon" onClick={() => setHelp(false)}>
                ×
              </button>
            </div>
            <div class="help-body">
              <div><kbd>Enter</kbd><span>发送；运行中改为插话</span></div>
              <div><kbd>Shift+Enter</kbd><span>换行</span></div>
              <div><kbd>Ctrl+K</kbd><span>聚焦输入框</span></div>
              <div><kbd>Ctrl+Shift+F</kbd><span>跨会话搜索</span></div>
              <div><kbd>Ctrl+Shift+P</kbd><span>命令面板</span></div>
              <div><kbd>Esc</kbd><span>中止当前回合 / 关闭面板 / 拒绝审批</span></div>
              <div><kbd>Y / N</kbd><span>允许 / 拒绝工具审批</span></div>
              <div><kbd>/help</kbd><span>查看斜杠命令</span></div>
              <Show when={"Notification" in window && Notification.permission === "default"}>
                <div>
                  <kbd>通知</kbd>
                  <button class="ghost" onClick={() => void Notification.requestPermission()}>
                    开启完成后桌面通知
                  </button>
                </div>
              </Show>
            </div>
            <div class="privacy-settings">
              <label>
                <input
                  type="checkbox"
                  checked={telemetry().enabled}
                  onChange={(event) =>
                    void setTelemetryPrefs({ enabled: event.currentTarget.checked })
                  }
                />
                本地遥测（重启生效，默认关闭）
              </label>
              <label>
                <input
                  type="checkbox"
                  checked={telemetry().consent}
                  onChange={(event) =>
                    void setTelemetryPrefs({ consent: event.currentTarget.checked })
                  }
                />
                匿名崩溃上报同意（重启生效）
              </label>
            </div>
            <div class="status-settings">
              <span>状态栏分段</span>
              <For each={STATUS_SEGMENTS}>
                {(key) => (
                  <label>
                    <input
                      type="checkbox"
                      checked={showSegment(key)}
                      onChange={() =>
                        setStatusSegments((prev) =>
                          prev.includes(key)
                            ? prev.filter((item) => item !== key)
                            : [...prev, key],
                        )
                      }
                    />
                    {key}
                  </label>
                )}
              </For>
            </div>
            <div class="diag">
              <div class="diag-actions">
                <input
                  ref={importInput}
                  type="file"
                  accept=".jsonl,.json,application/json"
                  style={{ display: "none" }}
                  onChange={(event) => void importSession(event)}
                />
                <button class="ghost" onClick={() => importInput.click()}>
                  导入会话 JSONL
                </button>
                <button class="ghost" onClick={() => exportTranscript("md")}>
                  导出 Markdown
                </button>
                <button class="ghost" onClick={() => exportTranscript("json")}>
                  导出 JSON
                </button>
              </div>
              <div>provider · {ready()?.provider || "—"}</div>
              <div>protocol · {ready()?.protocol || "—"}</div>
              <div>model · {ready()?.model || "—"}</div>
              <div>cwd · {ready()?.cwd || "—"}</div>
              <button
                class="ghost"
                onClick={() =>
                  void copyText(
                    JSON.stringify(
                      {
                        provider: ready()?.provider,
                        protocol: ready()?.protocol,
                        model: ready()?.model,
                        cwd: ready()?.cwd,
                        contextMax: ready()?.contextMax,
                        session: sessionPath(),
                      },
                      null,
                      2,
                    ),
                  )
                }
              >
                复制诊断信息
              </button>
            </div>
          </div>
        </div>
      </Show>
    </div>
  );
}

export default App;
