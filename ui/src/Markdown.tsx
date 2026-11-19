import { For, Show, type JSX } from "solid-js";

type Segment =
  | { kind: "code"; lang: string; body: string }
  | { kind: "text"; body: string };

type Block =
  | { kind: "heading"; level: number; text: string }
  | { kind: "list"; items: string[] }
  | { kind: "paragraph"; text: string };

function splitCode(text: string): Segment[] {
  const parts = text.split(/```/);
  return parts.map((part, index) => {
    if (index % 2 === 1) {
      const newline = part.indexOf("\n");
      const lang = newline === -1 ? "" : part.slice(0, newline).trim();
      const body = newline === -1 ? part : part.slice(newline + 1);
      return { kind: "code", lang, body: body.replace(/\n$/, "") };
    }
    return { kind: "text", body: part };
  });
}

function parseBlocks(text: string): Block[] {
  const blocks: Block[] = [];
  let paragraph: string[] = [];
  let list: string[] = [];
  const flushParagraph = () => {
    if (paragraph.length) {
      blocks.push({ kind: "paragraph", text: paragraph.join("\n") });
      paragraph = [];
    }
  };
  const flushList = () => {
    if (list.length) {
      blocks.push({ kind: "list", items: [...list] });
      list = [];
    }
  };
  for (const line of text.split("\n")) {
    const heading = /^(#{1,6})\s+(.*)$/.exec(line);
    const bullet = /^(?:[-*]|\d+\.)\s+(.*)$/.exec(line);
    if (heading) {
      flushParagraph();
      flushList();
      blocks.push({ kind: "heading", level: heading[1].length, text: heading[2] });
    } else if (bullet) {
      flushParagraph();
      list.push(bullet[1]);
    } else if (line.trim() === "") {
      flushParagraph();
      flushList();
    } else {
      flushList();
      paragraph.push(line);
    }
  }
  flushParagraph();
  flushList();
  return blocks;
}

function inline(text: string): JSX.Element[] {
  const tokens = text.split(/(\*\*[^*]+\*\*|`[^`]+`|\[[^\]]+\]\([^)]+\))/g);
  return tokens.filter(Boolean).map((token) => {
    if (token.startsWith("**") && token.endsWith("**")) {
      return <strong>{token.slice(2, -2)}</strong>;
    }
    if (token.startsWith("`") && token.endsWith("`")) {
      return <code class="md-code">{token.slice(1, -1)}</code>;
    }
    const link = /^\[([^\]]+)\]\(([^)]+)\)$/.exec(token);
    if (link) {
      const url = link[2].trim();
      // Only http(s) links become anchors; everything else stays inert text so
      // a model cannot smuggle in javascript: or data: URLs.
      if (/^https?:\/\//i.test(url)) {
        return (
          <a href={url} target="_blank" rel="noreferrer noopener">
            {link[1]}
          </a>
        );
      }
      return <>{token}</>;
    }
    return <>{token}</>;
  });
}

function Prose(props: { text: string }) {
  return (
    <For each={parseBlocks(props.text)}>
      {(block) => {
        if (block.kind === "heading") {
          return (
            <Show when={block.level <= 3} fallback={<div class="md-h">{inline(block.text)}</div>}>
              <div class={`md-h md-h${block.level}`}>{inline(block.text)}</div>
            </Show>
          );
        }
        if (block.kind === "list") {
          return (
            <ul class="md-list">
              <For each={block.items}>{(item) => <li>{inline(item)}</li>}</For>
            </ul>
          );
        }
        return <p class="md-p">{inline(block.text)}</p>;
      }}
    </For>
  );
}

function CodeBlock(props: { lang: string; body: string }) {
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(props.body);
    } catch {
      // Clipboard can be denied in a sandboxed WebView; the code stays visible.
    }
  };
  return (
    <div class="code-block">
      <div class="code-head">
        <code>{props.lang || "text"}</code>
        <button onClick={() => void copy()}>复制</button>
      </div>
      <pre>
        <code>{props.body}</code>
      </pre>
    </div>
  );
}

export default function Markdown(props: { text: string }) {
  return (
    <div class="md">
      <For each={splitCode(props.text)}>
        {(segment) =>
          segment.kind === "code" ? (
            <CodeBlock lang={segment.lang} body={segment.body} />
          ) : (
            <Prose text={segment.body} />
          )
        }
      </For>
    </div>
  );
}
