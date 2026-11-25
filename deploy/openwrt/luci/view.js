'use strict';
'require view';

return view.extend({
	render: function() {
		var panelUrl = 'http://' + window.location.hostname + ':2023';
		var isHttps = window.location.protocol === 'https:';
		return E('div', { 'style': 'display:flex;flex-direction:column;gap:10px;height:calc(100vh - 110px);min-height:680px;' }, [
			E('div', { 'style': 'display:flex;align-items:center;justify-content:space-between;padding:8px 14px;background:#121a2b;color:#f1f5f9;border-radius:10px;' }, [
				E('div', { 'style': 'font-weight:600;font-size:14px;' }, '⚡ DaeStudio (daed-next) · 高性能 eBPF 透明代理可视化控制中心'),
				E('a', {
					'href': panelUrl,
					'target': '_blank',
					'rel': 'noopener noreferrer',
					'class': 'cbi-button cbi-button-action',
					'style': 'text-decoration:none;'
				}, '↗ 全屏独立打开面板 (:2023)')
			]),
			isHttps
				? E('div', { 'style': 'padding:32px;border:1px solid rgba(148,163,184,0.22);border-radius:12px;background:#090d16;color:#cbd5e1;text-align:center;' }, [
					E('p', { 'style': 'margin:0 0 14px;font-size:15px;' }, '当前 LuCI 使用 HTTPS 访问，浏览器安全策略限制内嵌 HTTP (:2023) 子页面。请点击下方按钮在新标签页独立打开 DaeStudio 面板：'),
					E('a', { 'href': panelUrl, 'target': '_blank', 'rel': 'noopener noreferrer', 'class': 'cbi-button cbi-button-apply', 'style': 'text-decoration:none;' }, '立即打开 DaeStudio (' + panelUrl + ')')
				])
				: E('iframe', {
					'src': panelUrl,
					'style': 'width:100%;flex:1;border:1px solid rgba(148,163,184,0.22);border-radius:12px;background:#090d16;',
					'allow': 'clipboard-write'
				})
		]);
	},
	handleSaveApply: null,
	handleSave: null,
	handleReset: null
});
