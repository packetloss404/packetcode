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

  const viewer = document.getElementById('screenshot-viewer');
  if (viewer && typeof viewer.showModal === 'function') {
    const viewport = viewer.querySelector('.viewer-viewport');
    const canvas = viewer.querySelector('.viewer-canvas');
    const image = document.getElementById('screenshot-viewer-image');
    const title = document.getElementById('screenshot-viewer-title');
    const original = document.getElementById('screenshot-original');
    const zoomLabel = document.getElementById('screenshot-zoom');
    const zoomIn = viewer.querySelector('[data-zoom-in]');
    const zoomOut = viewer.querySelector('[data-zoom-out]');
    let trigger;
    let crop;
    let scale = 1;
    let priorOverflow = '';

    const paint = () => {
      canvas.style.width = crop.width * scale + 'px';
      canvas.style.height = crop.height * scale + 'px';
      image.style.width = '1280px';
      image.style.height = '720px';
      image.style.transformOrigin = 'top left';
      image.style.transform = `translate(${-crop.x * scale}px, ${-crop.y * scale}px) scale(${scale})`;
      zoomLabel.textContent = Math.round(scale * 100) + '%';
      zoomIn.disabled = scale >= 4;
      zoomOut.disabled = scale <= 0.25;
    };
    const zoom = (next) => {
      const centerX = (viewport.scrollLeft + viewport.clientWidth / 2) / scale;
      const centerY = (viewport.scrollTop + viewport.clientHeight / 2) / scale;
      scale = Math.min(4, Math.max(0.25, next));
      paint();
      viewport.scrollLeft = centerX * scale - viewport.clientWidth / 2;
      viewport.scrollTop = centerY * scale - viewport.clientHeight / 2;
    };
    const fit = () => zoom(Math.min((viewport.clientWidth - 48) / crop.width, (viewport.clientHeight - 48) / crop.height));
    document.querySelectorAll('[data-screenshot-open]').forEach((link) => {
      link.addEventListener('click', (event) => {
        if (event.ctrlKey || event.metaKey || event.shiftKey || event.altKey) return;
        const values = link.dataset.screenshotCrop.split(',').map(Number);
        if (values.length !== 4 || !values.every(Number.isFinite) || values[2] <= 0 || values[3] <= 0) return;
        event.preventDefault();
        trigger = link;
        crop = { x: values[0], y: values[1], width: values[2], height: values[3] };
        image.src = link.href;
        image.alt = link.querySelector('img').alt;
        title.textContent = link.dataset.screenshotTitle;
        original.href = link.href;
        priorOverflow = document.body.style.overflow;
        document.body.style.overflow = 'hidden';
        viewer.showModal();
        scale = Math.min(1.5, Math.max(1.25, (viewport.clientWidth - 48) / crop.width));
        paint();
        viewport.scrollLeft = 0;
        viewport.scrollTop = 0;
      });
    });
    zoomIn.addEventListener('click', () => zoom(scale + 0.25));
    zoomOut.addEventListener('click', () => zoom(scale - 0.25));
    viewer.querySelector('[data-zoom-fit]').addEventListener('click', fit);
    viewer.querySelector('[data-viewer-close]').addEventListener('click', () => viewer.close());
    viewer.addEventListener('keydown', (event) => {
      if (event.ctrlKey || event.metaKey || event.altKey) return;
      if (event.key === '+' || event.key === '=') zoom(scale + 0.25);
      else if (event.key === '-') zoom(scale - 0.25);
      else if (event.key === '0') fit();
      else if (event.key.startsWith('Arrow')) {
        const offsets = { ArrowLeft: [-80, 0], ArrowRight: [80, 0], ArrowUp: [0, -80], ArrowDown: [0, 80] };
        const offset = offsets[event.key];
        if (!offset) return;
        viewport.scrollLeft += offset[0];
        viewport.scrollTop += offset[1];
      } else return;
      event.preventDefault();
    });
    viewer.addEventListener('close', () => {
      document.body.style.overflow = priorOverflow;
      image.removeAttribute('src');
      trigger?.focus({ preventScroll: true });
    });
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
