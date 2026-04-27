// Content script: lives on every LinkedIn /jobs/view/* page. Stays silent
// until the popup asks it to scrape. This keeps the gateway fully opt-in —
// nothing is submitted unless the user clicks "Save".

chrome.runtime.onMessage.addListener((msg, _sender, sendResponse) => {
  if (!msg || msg.type !== 'SCRAPE_JOB') {
    return false;
  }
  try {
    const data = JobStreamParser.parseLinkedInJob(document, window.location);
    sendResponse({ ok: true, data });
  } catch (e) {
    sendResponse({ ok: false, error: String(e) });
  }
  return false; // sync response
});
