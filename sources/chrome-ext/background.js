// MV3 service worker. Owns the network call to the intake gateway so the
// JWT and gateway URL are read in one place. The popup hands us a payload;
// we attach the stored JWT and POST it.

const DEFAULT_GATEWAY = 'http://localhost:8081';

async function loadSettings() {
  const stored = await chrome.storage.local.get(['user_id', 'jwt', 'gateway']);
  return {
    userID: (stored.user_id || '').trim(),
    jwt: (stored.jwt || '').trim(),
    gateway: (stored.gateway || DEFAULT_GATEWAY).trim(),
  };
}

async function submitJob(scraped) {
  const { userID, jwt, gateway } = await loadSettings();
  if (!jwt) throw new Error('no JWT — open extension Options and paste one');
  if (!userID) throw new Error('no user_id — open extension Options');
  if (!scraped.jobTitle || !scraped.company) {
    throw new Error('could not parse job page (title or company missing)');
  }

  const body = {
    user_id: userID,
    job_title: scraped.jobTitle,
    company: scraped.company,
    url: scraped.url,
    source: 'SOURCE_CHROME_EXT',
  };

  const resp = await fetch(`${gateway}/v1/applications`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      Authorization: `Bearer ${jwt}`,
    },
    body: JSON.stringify(body),
  });

  const text = await resp.text();
  let parsed;
  try {
    parsed = JSON.parse(text);
  } catch {
    parsed = { error: text };
  }
  if (!resp.ok) {
    throw new Error(`gateway ${resp.status}: ${parsed.error || text}`);
  }
  return parsed; // { application_id }
}

chrome.runtime.onMessage.addListener((msg, _sender, sendResponse) => {
  if (!msg || msg.type !== 'SUBMIT_JOB') return false;
  submitJob(msg.payload)
    .then((r) => sendResponse({ ok: true, ...r }))
    .catch((e) => sendResponse({ ok: false, error: e.message }));
  return true; // keep the message channel open for the async response
});
