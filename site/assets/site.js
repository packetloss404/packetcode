/* Small enhancements; installation and download links work without JavaScript. */
(() => {
  'use strict';
  document.documentElement.classList.add('js');

  const toggle = document.querySelector('[data-menu-toggle]');
  const navigation = toggle && document.getElementById(toggle.getAttribute('aria-controls'));
  const setMenu = (open) => {
    if (!toggle || !navigation) return;
    toggle.setAttribute('aria-expanded', String(open));
    navigation.dataset.open = String(open);
    const icon = toggle.querySelector('span');
    if (icon) icon.textContent = open ? '−' : '+';
  };
  toggle?.addEventListener('click', () => setMenu(toggle.getAttribute('aria-expanded') !== 'true'));
  navigation?.addEventListener('click', (event) => {
    if (event.target.closest('a')) setMenu(false);
  });
  document.addEventListener('keydown', (event) => {
    if (event.key === 'Escape' && toggle?.getAttribute('aria-expanded') === 'true') {
      setMenu(false);
      toggle.focus();
    }
  });

  const tabGroups = [];
  document.querySelectorAll('[role="tablist"]').forEach((list) => {
    const tabs = [...list.querySelectorAll('[role="tab"]')];
    if (!tabs.length) return;
    const select = (chosen, focus = false) => {
      tabs.forEach((tab) => {
        const active = tab === chosen;
        tab.setAttribute('aria-selected', String(active));
        tab.tabIndex = active ? 0 : -1;
        const panel = document.getElementById(tab.getAttribute('aria-controls'));
        if (panel) panel.hidden = !active;
      });
      if (focus) chosen.focus();
    };
    list.addEventListener('click', (event) => {
      const tab = event.target.closest('[role="tab"]');
      if (tabs.includes(tab)) select(tab);
    });
    list.addEventListener('keydown', (event) => {
      const index = tabs.indexOf(document.activeElement);
      if (index === -1) return;
      let next;
      if (event.key === 'ArrowRight') next = (index + 1) % tabs.length;
      else if (event.key === 'ArrowLeft') next = (index - 1 + tabs.length) % tabs.length;
      else if (event.key === 'Home') next = 0;
      else if (event.key === 'End') next = tabs.length - 1;
      else return;
      event.preventDefault();
      select(tabs[next], true);
    });
    select(tabs.find((tab) => tab.getAttribute('aria-selected') === 'true') || tabs[0]);
    tabGroups.push({ list, tabs, select });
  });
  if (/Windows/i.test(navigator.userAgent)) {
    const windowsTab = document.querySelector('[data-install-tab="windows"]');
    const installGroup = tabGroups.find((group) => group.tabs.includes(windowsTab));
    if (installGroup) installGroup.select(windowsTab);
  }

  const copyTimers = new WeakMap();
  const fallbackCopy = (text) => {
    const previousFocus = document.activeElement;
    const input = document.createElement('textarea');
    input.value = text;
    input.setAttribute('readonly', '');
    input.style.cssText = 'position:fixed;left:-9999px;top:0;opacity:0;';
    document.body.append(input);
    input.select();
    const copied = document.execCommand('copy');
    input.remove();
    previousFocus?.focus({ preventScroll: true });
    if (!copied) throw new Error('Clipboard unavailable');
  };
  document.querySelectorAll('[data-copy-target]').forEach((button) => {
    const originalLabel = button.innerHTML;
    button.addEventListener('click', async () => {
      const target = document.getElementById(button.dataset.copyTarget);
      const status = document.getElementById('copy-status');
      if (!target) return;
      const text = target.textContent.trim();
      try {
        try {
          if (!navigator.clipboard?.writeText) throw new Error('Use clipboard fallback');
          await navigator.clipboard.writeText(text);
        } catch {
          fallbackCopy(text);
        }
        button.textContent = 'Copied!';
        if (status) status.textContent = 'Command copied to clipboard.';
      } catch {
        button.textContent = 'Select command';
        const range = document.createRange();
        range.selectNodeContents(target);
        const selection = window.getSelection();
        selection?.removeAllRanges();
        selection?.addRange(range);
        if (status) status.textContent = 'Copy is unavailable. The command is selected so you can copy it manually.';
      }
      clearTimeout(copyTimers.get(button));
      copyTimers.set(button, setTimeout(() => { button.innerHTML = originalLabel; }, 2000));
    });
  });
})();
