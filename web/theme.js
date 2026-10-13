// Apply the saved appearance before CSS paints; keep storage optional in webviews.
(() => {
  let theme = 'light';
  try { if (localStorage.getItem('taskboard-theme') === 'dark') theme = 'dark'; } catch {}
  document.documentElement.dataset.theme = theme;
  document.querySelector('meta[name="theme-color"]').content = theme === 'dark' ? '#1b1c1f' : '#fafaf8';
})();
