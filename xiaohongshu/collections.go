package xiaohongshu

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/go-rod/rod"
	"github.com/sirupsen/logrus"
)

type CollectionsAction struct {
	page *rod.Page
}

func NewCollectionsAction(page *rod.Page) *CollectionsAction {
	return &CollectionsAction{page: page.Timeout(90 * time.Second)}
}

// CollectionItem 一条收藏笔记：id + xsec_token（配对用于后续取详情/取消收藏）+ 标题
type CollectionItem struct {
	ID        string `json:"id"`
	XsecToken string `json:"xsecToken"`
	Title     string `json:"title"`
}

// GetMyCollections 导航到当前登录用户主页 → 点「收藏」tab → 从 __INITIAL_STATE__ 的 Feed 数据提取收藏列表。
// 优先取带 xsecToken 的字段（列表页卡片链接常不带 token，只有 store 里的 Feed 数据带）。
func (c *CollectionsAction) GetMyCollections(ctx context.Context) ([]CollectionItem, error) {
	page := c.page.Context(ctx).Timeout(90 * time.Second)

	nav := NewNavigate(page)
	if err := nav.ToProfilePage(ctx); err != nil {
		return nil, fmt.Errorf("导航到个人主页失败: %w", err)
	}
	page.MustWaitStable()

	// 点「收藏」tab：按可见文字精确匹配叶子节点
	clicked := page.MustEval(`() => {
		const els = Array.from(document.querySelectorAll('span,div,a,button'));
		const tab = els.find(e => e.children.length === 0 && e.textContent.trim() === '收藏');
		if (tab) { tab.click(); return true; }
		return false;
	}`).Bool()
	if !clicked {
		return nil, fmt.Errorf("未找到「收藏」标签页（可能未登录，或页面结构变化）")
	}

	// 等收藏列表异步渲染 + 数据写入 __INITIAL_STATE__
	time.Sleep(3 * time.Second)
	page.MustWaitStable()

	// 遍历 __INITIAL_STATE__.user 各字段，找出「带 xsecToken 的 Feed 数组」= 收藏列表。
	// 同时返回 debug(字段名/首个卡片 href),便于探明结构。
	raw := page.MustEval(`() => {
		const u = (window.__INITIAL_STATE__ && window.__INITIAL_STATE__.user) || {};
		const dbg = { userKeys: Object.keys(u) };
		const a = document.querySelector('a[href*="/explore/"]');
		dbg.firstHref = a ? a.getAttribute('href') : null;
		const toItems = (arr) => {
			const flat = Array.isArray(arr[0]) ? arr.flat() : arr;
			const out = [];
			for (const f of flat) {
				if (f && f.id) out.push({
					id: f.id,
					xsecToken: f.xsecToken || (f.noteCard && f.noteCard.xsecToken) || '',
					title: (f.noteCard && (f.noteCard.displayTitle || f.noteCard.title)) || f.title || ''
				});
			}
			return out;
		};
		let items = [];
		for (const k of Object.keys(u)) {
			try {
				const v = u[k];
				const val = v && (v.value !== undefined ? v.value : v._value);
				if (Array.isArray(val) && val.length) {
					const cand = toItems(val);
					if (cand.some(x => x.xsecToken)) { items = cand; dbg.usedField = k; break; }
					if (cand.length && items.length === 0) { items = cand; dbg.usedField = k + '(no-token)'; }
				}
			} catch (e) {}
		}
		// 兜底:store 里没找到就用 DOM 卡片(至少有 id+title)
		if (items.length === 0) {
			const seen = new Set();
			document.querySelectorAll('a[href*="/explore/"]').forEach(el => {
				const href = el.getAttribute('href') || '';
				const m = href.match(/\/explore\/([0-9a-zA-Z]+)/);
				const t = href.match(/xsec_token=([^&]+)/);
				if (m && !seen.has(m[1])) {
					seen.add(m[1]);
					const card = el.closest('section') || el.parentElement;
					let title = '';
					if (card) { const te = card.querySelector('[class*="title"]'); if (te) title = te.textContent.trim(); }
					items.push({ id: m[1], xsecToken: t ? decodeURIComponent(t[1]) : '', title });
				}
			});
			dbg.usedField = 'dom-fallback';
		}
		return JSON.stringify({ items, debug: dbg });
	}`).String()

	var parsed struct {
		Items []CollectionItem `json:"items"`
		Debug json.RawMessage  `json:"debug"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil, fmt.Errorf("解析收藏列表失败: %w (raw=%.300s)", err, raw)
	}
	logrus.Infof("get_my_collections debug: %s", string(parsed.Debug))
	return parsed.Items, nil
}
