const $ = (id) => document.getElementById(id);

async function load() {
  const stored = await chrome.storage.local.get(['user_id', 'jwt', 'gateway']);
  $('user_id').value = stored.user_id || '';
  $('jwt').value = stored.jwt || '';
  $('gateway').value = stored.gateway || 'http://localhost:8081';
}

$('save').addEventListener('click', async () => {
  await chrome.storage.local.set({
    user_id: $('user_id').value.trim(),
    jwt: $('jwt').value.trim(),
    gateway: $('gateway').value.trim() || 'http://localhost:8081',
  });
  const s = $('status');
  s.textContent = 'saved';
  s.className = 'ok';
  setTimeout(() => {
    s.textContent = '';
    s.className = '';
  }, 1500);
});

load();
