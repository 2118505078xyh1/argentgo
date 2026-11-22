package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/cdp"
)

// ExtractLevel 控制提取粒度（参考 ghostchrome 的三级）。
type ExtractLevel string

const (
	LevelSkeleton ExtractLevel = "skeleton" // 仅交互元素
	LevelContent  ExtractLevel = "content"  // 交互元素 + 文本
	LevelFull     ExtractLevel = "full"     // 所有命名元素
)

// Element 是一个带 ref 的可交互元素。
type Element struct {
	Ref      string `json:"ref"`
	Role     string `json:"role"`
	Name     string `json:"name"`
	Tag      string `json:"tag"`
	Selector string `json:"selector"`
}

// Snapshot 是一次页面提取的结果（文本优先）。
type Snapshot struct {
	URL      string
	Title    string
	Elements []Element
	Text     string // 渲染后的紧凑文本（带 @N refs）
}

// Extract 从当前页面提取交互元素并分配 @N refs。
func (b *Browser) Extract(ctx context.Context, level ExtractLevel) (*Snapshot, error) {
	s, err := b.session()
	if err != nil {
		return nil, err
	}
	if level == "" {
		level = LevelContent
	}
	// JS 在页面内遍历 DOM，给可交互元素分配稳定的 @N ref，并写入 data 属性
	// 供后续 click/type 精确定位。返回 JSON 数组。
	js := extractJS(string(level))
	res, err := s.Call(ctx, "Runtime.evaluate", map[string]any{
		"expression": js, "returnByValue": true, "awaitPromise": true,
	})
	if err != nil {
		return nil, err
	}
	// Runtime.evaluate 的 result.value 是 JS JSON.stringify 的结果（字符串），
	// 先解出字符串，再做一次 JSON 解析得到 elements/text。
	var outer struct {
		Result struct {
			Value string `json:"value"`
		} `json:"result"`
	}
	if err := json.Unmarshal(res, &outer); err != nil {
		return nil, fmt.Errorf("browser: decode extract envelope: %w", err)
	}
	var inner struct {
		Elements []Element `json:"elements"`
		Text     string    `json:"text"`
	}
	if err := json.Unmarshal([]byte(outer.Result.Value), &inner); err != nil {
		return nil, fmt.Errorf("browser: decode extract payload: %w", err)
	}
	title := pageTitle(ctx, s)
	url := pageURL(ctx, s)
	return &Snapshot{URL: url, Title: title, Elements: inner.Elements, Text: inner.Text}, nil
}

func pageURL(ctx context.Context, s *cdp.Session) string {
	res, err := s.Call(ctx, "Runtime.evaluate", map[string]any{
		"expression": "location.href", "returnByValue": true,
	})
	if err != nil {
		return ""
	}
	var r struct {
		Result struct {
			Value string `json:"value"`
		} `json:"result"`
	}
	if json.Unmarshal(res, &r) == nil {
		return r.Result.Value
	}
	return ""
}

// extractJS 返回在页面内执行的提取脚本。只保留交互元素（skeleton），
// content/full 额外带文本节点。每个元素写入 data-argentgo-ref 属性。
func extractJS(level string) string {
	return fmt.Sprintf(`(function(){
  const LEVEL=%q;
  const interactive=new Set(['a','button','input','select','textarea','option','label','summary','[role=button]','[role=link]','[role=checkbox]','[role=tab]','[role=menuitem]']);
  const els=[];
  const all=document.querySelectorAll('*');
  let n=0;
  const isInteractive=(e)=>{
    const tag=e.tagName.toLowerCase();
    if(['a','button','input','select','textarea','summary'].includes(tag))return true;
    if(e.getAttribute('role')&&/^(button|link|checkbox|tab|menuitem|textbox|combobox|searchbox)$/.test(e.getAttribute('role')))return true;
    if(e.onclick||e.getAttribute('onclick'))return true;
    if(e.tabIndex>=0 && tag!=='body')return true;
    return false;
  };
  for(const e of all){
    const tag=e.tagName.toLowerCase();
    if(['script','style','noscript','svg','path'].includes(tag))continue;
    let name=(e.getAttribute('aria-label')||e.getAttribute('title')||e.value||e.textContent||'').trim().replace(/\s+/g,' ').slice(0,120);
    const text=(e.textContent||'').trim().replace(/\s+/g,' ').slice(0,200);
    const keep=isInteractive(e)|| (LEVEL==='full'&&name);
    if(!keep)continue;
    n++;
    const ref='@'+n;
    e.setAttribute('data-argentgo-ref',ref);
    let sel='['+'data-argentgo-ref="'+ref+'"]';
    els.push({ref:ref,role:e.getAttribute('role')||tag,name:name.slice(0,120),tag:tag,selector:sel});
  }
  // 构造紧凑文本：skeleton 只有 ref+role+name；content/full 带文本行。
  let lines=[];
  for(const e of els){
    if(LEVEL==='skeleton')lines.push(e.ref+' '+e.role+(e.name?' '+e.name:''));
    else lines.push(e.ref+' '+e.role+(e.name?' '+e.name:'')+(e.tag==='a'&&e.name?' -> ':''));
  }
  return JSON.stringify({elements:els,text:lines.join('\n')});
})()`, level)
}

// Click 点击一个 @N ref 元素。
func (b *Browser) Click(ctx context.Context, ref string) error {
	return b.refAction(ctx, ref, "el.click()")
}

// Focus 聚焦一个 @N ref 元素。
func (b *Browser) Focus(ctx context.Context, ref string) error {
	return b.refAction(ctx, ref, "el.focus()")
}

func (b *Browser) refAction(ctx context.Context, ref, action string) error {
	s, err := b.session()
	if err != nil {
		return err
	}
	js := fmt.Sprintf(`(function(){const el=document.querySelector('[data-argentgo-ref=%q]');if(!el)return 'not found';%s;return 'ok';})()`, ref, action)
	res, err := s.Call(ctx, "Runtime.evaluate", map[string]any{
		"expression": js, "returnByValue": true,
	})
	if err != nil {
		return err
	}
	var r struct {
		Result struct {
			Value string `json:"value"`
		} `json:"result"`
	}
	if json.Unmarshal(res, &r) == nil && r.Result.Value != "ok" {
		return fmt.Errorf("browser: ref %s: %s", ref, r.Result.Value)
	}
	return nil
}

// Type 向 @N ref 元素输入文本（先聚焦，再用 CDP Input.insertText 逐字输入）。
func (b *Browser) Type(ctx context.Context, ref, text string) error {
	if err := b.Focus(ctx, ref); err != nil {
		return err
	}
	s, err := b.session()
	if err != nil {
		return err
	}
	_, err = s.Call(ctx, "Input.insertText", map[string]any{"text": text})
	return err
}

// Eval 在页面中执行任意 JS 表达式并返回值（转义为字符串）。
func (b *Browser) Eval(ctx context.Context, expr string) (string, error) {
	s, err := b.session()
	if err != nil {
		return "", err
	}
	res, err := s.Call(ctx, "Runtime.evaluate", map[string]any{
		"expression": expr, "returnByValue": true, "awaitPromise": true,
	})
	if err != nil {
		return "", err
	}
	var r struct {
		Result struct {
			Value any    `json:"value"`
			Type  string `json:"type"`
		} `json:"result"`
		ExceptionDetails *struct {
			Text string `json:"text"`
		} `json:"exceptionDetails"`
	}
	if json.Unmarshal(res, &r) == nil {
		if r.ExceptionDetails != nil {
			return "", fmt.Errorf("browser: eval: %s", r.ExceptionDetails.Text)
		}
		if s, ok := r.Result.Value.(string); ok {
			return s, nil
		}
		b, _ := json.Marshal(r.Result.Value)
		return string(b), nil
	}
	return strings.TrimSpace(string(res)), nil
}

// Screenshot 截取当前页面（返回 base64 PNG）。
func (b *Browser) Screenshot(ctx context.Context, full bool) (string, error) {
	s, err := b.session()
	if err != nil {
		return "", err
	}
	args := map[string]any{"format": "png"}
	if full {
		args["captureBeyondViewport"] = true
	}
	res, err := s.Call(ctx, "Page.captureScreenshot", args)
	if err != nil {
		return "", err
	}
	var r struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal(res, &r); err != nil {
		return "", err
	}
	return r.Data, nil
}
