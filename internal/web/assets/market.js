(() => {
  const button = document.querySelector('#scan-now');
  const feedback = document.querySelector('#scan-feedback');
  const statusLabel = document.querySelector('#scan-status');
  const scanConsole = document.querySelector('.scan-console');
  const pageURL = scanConsole?.dataset.pageUrl;
  const statusURL = scanConsole?.dataset.statusUrl;
  const scanURL = scanConsole?.dataset.scanUrl;
  const stateLabels = {queued: '等待中', running: '扫描中', completed: '已完成', degraded: '部分完成', failed: '失败'};
  const triggerLabels = {startup: '启动扫描', manual: '手动扫描', scheduled: '定时扫描', snapshot: '行情更新'};

  document.querySelectorAll('.radar-candidate').forEach((candidate) => {
    candidate.addEventListener('click', () => {
      document.querySelectorAll('.radar-candidate').forEach((item) => {
        const active = item === candidate;
        item.classList.toggle('is-active', active);
        item.setAttribute('aria-pressed', String(active));
      });
      document.querySelectorAll('.opportunity-detail').forEach((detail) => {
        const active = detail.id === candidate.dataset.opportunity;
        detail.classList.toggle('is-active', active);
        detail.hidden = !active;
      });
    });
  });

  if (!button) return;

  const updateStatus = (status) => {
	const state = stateLabels[status.state] || '未知状态';
	const trigger = triggerLabels[status.trigger] || '市场扫描';
	if (statusLabel) statusLabel.textContent = state;
    if (feedback) {
      const progress = status.progress_total > 0 ? ` · ${status.progress_current}/${status.progress_total}` : '';
	  feedback.textContent = `${trigger} · ${state}${progress}`;
    }
    return status.state;
  };

  const pollMarketStatus = async () => {
    for (;;) {
      const response = await fetch(statusURL, {headers: {'Accept': 'application/json'}, cache: 'no-store'});
      if (!response.ok) throw new Error('读取扫描状态失败');
      const status = await response.json();
      const state = updateStatus(status);
      if (state !== 'queued' && state !== 'running') {
        if (state === 'completed' || state === 'degraded') window.location.assign(pageURL);
        if (state === 'failed') throw new Error(status.error || '扫描失败');
        return;
      }
      await new Promise((resolve) => window.setTimeout(resolve, 2000));
    }
  };

  const triggerMarketScan = async () => {
    button.disabled = true;
    if (feedback) feedback.textContent = '正在提交完整扫描…';
    try {
      const response = await fetch(scanURL, {method: 'POST', headers: {'Content-Type': 'application/json', 'Accept': 'application/json'}, body: '{}'});
      const status = await response.json().catch(() => ({}));
      if (response.status === 429) {
        updateStatus(status);
        throw new Error('刚完成扫描，请等待 60 秒后再试');
      }
      if (!response.ok) throw new Error('扫描请求失败');
      updateStatus(status);
      await pollMarketStatus();
    } catch (error) {
      if (feedback) feedback.textContent = error instanceof Error ? error.message : '扫描失败';
      button.disabled = false;
    }
  };

  button.addEventListener('click', triggerMarketScan);
  if (scanConsole?.dataset.scanState === 'running') {
    button.disabled = true;
    pollMarketStatus().catch((error) => {
      if (feedback) feedback.textContent = error.message;
      button.disabled = false;
    });
  }
})();
