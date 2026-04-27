// Popup orchestrates one save: ask the active tab's content script to
// scrape, then hand the result to the background worker which talks to
// the gateway. We keep the popup itself UI-only.

const statusEl = document.getElementById('status');

function setStatus(text, cls) {
  statusEl.textContent = text;
  statusEl.className = cls || '';
}

async function activeTab() {
  const [tab] = await chrome.tabs.query({ active: true, currentWindow: true });
  return tab || null;
}

async function scrape(tab) {
  return new Promise((resolve) => {
    chrome.tabs.sendMessage(tab.id, { type: 'SCRAPE_JOB' }, (resp) => {
      if (chrome.runtime.lastError) {
        resolve({ ok: false, error: chrome.runtime.lastError.message });
        return;
      }
      resolve(resp || { ok: false, error: 'no response from content script' });
    });
  });
}

async function submit(scraped) {
  return new Promise((resolve) => {
    chrome.runtime.sendMessage({ type: 'SUBMIT_JOB', payload: scraped }, (resp) => {
      if (chrome.runtime.lastError) {
        resolve({ ok: false, error: chrome.runtime.lastError.message });
        return;
      }
      resolve(resp || { ok: false, error: 'no response from background' });
    });
  });
}

document.getElementById('save').addEventListener('click', async () => {
  setStatus('scraping…');
  const tab = await activeTab();
  if (!tab || !tab.url || !tab.url.includes('linkedin.com/jobs/view/')) {
    setStatus('open a LinkedIn /jobs/view/ page first', 'err');
    return;
  }
  const scraped = await scrape(tab);
  if (!scraped.ok) {
    setStatus(`scrape failed: ${scraped.error}`, 'err');
    return;
  }
  setStatus(`submitting "${scraped.data.jobTitle}" @ ${scraped.data.company}…`);
  const result = await submit(scraped.data);
  if (result.ok) {
    setStatus(`saved\nid: ${result.application_id}`, 'ok');
  } else {
    setStatus(`error: ${result.error}`, 'err');
  }
});
