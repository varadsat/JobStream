// LinkedIn job-page DOM parser.
//
// Kept as a pure function over `Document` and `Location` so it could be
// unit-tested with jsdom later. The selectors are best-effort: LinkedIn
// rewrites class names regularly, so each field tries the modern
// "job-details-jobs-unified-top-card" hierarchy first and falls back to
// stable element shapes (h1, anchor under company-name).

(function () {
  const root = typeof globalThis !== 'undefined' ? globalThis : self;

  function textOf(el) {
    return el && el.textContent ? el.textContent.trim().replace(/\s+/g, ' ') : '';
  }

  function parseLinkedInJob(doc, loc) {
    const titleEl =
      doc.querySelector('.job-details-jobs-unified-top-card__job-title h1') ||
      doc.querySelector('.job-details-jobs-unified-top-card__job-title') ||
      doc.querySelector('h1.t-24') ||
      doc.querySelector('h1');

    const companyEl =
      doc.querySelector('.job-details-jobs-unified-top-card__company-name a') ||
      doc.querySelector('.job-details-jobs-unified-top-card__company-name') ||
      doc.querySelector('a[data-tracking-control-name="public_jobs_topcard-org-name"]');

    return {
      jobTitle: textOf(titleEl),
      company: textOf(companyEl),
      url: loc && loc.href ? loc.href : '',
    };
  }

  root.JobStreamParser = { parseLinkedInJob };
})();
