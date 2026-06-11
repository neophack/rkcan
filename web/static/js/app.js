/**
 * app.js - Main SPA router and SSE client for the RKCAN Dashboard.
 */

/* Utility Functions */

window.formatBytes = function (bytes) {
    if (bytes === 0) return '0 B';
    var units = ['B', 'KB', 'MB', 'GB'];
    var i = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1);
    var value = bytes / Math.pow(1024, i);
    return value.toFixed(i === 0 ? 0 : 1) + ' ' + units[i];
};

window.formatNumber = function (n) {
    if (n == null) return '0';
    return Number(n).toLocaleString('en-US');
};

window.formatPercent = function (n) {
    if (n == null) return '0.0%';
    return Number(n).toFixed(1) + '%';
};

window.escapeHtml = function (str) {
    var div = document.createElement('div');
    div.appendChild(document.createTextNode(str));
    return div.innerHTML;
};

window.api = function (url, options) {
    options = options || {};
    return fetch(url, {
        headers: Object.assign({ 'Content-Type': 'application/json' }, options.headers || {}),
        method: options.method || 'GET',
        body: options.body || undefined,
    }).then(function (resp) {
        if (!resp.ok) {
            return resp.text().then(function (text) {
                throw new Error(text || 'HTTP ' + resp.status);
            });
        }
        return resp.json();
    }).catch(function (err) {
        window.showToast('API error: ' + err.message, 'error');
        throw err;
    });
};

/* Toast Notification System */

var MAX_TOASTS = 5;
var TOAST_DURATION = 3000;

var getToastContainer = function () {
    var container = document.querySelector('.toast-container');
    if (!container) {
        container = document.createElement('div');
        container.className = 'toast-container';
        document.body.appendChild(container);
    }
    return container;
};

window.showToast = function (message, type) {
    type = type || 'info';
    var container = getToastContainer();

    var toasts = container.querySelectorAll('.toast');
    if (toasts.length >= MAX_TOASTS) {
        toasts[0].remove();
    }

    var toast = document.createElement('div');
    toast.className = 'toast toast-' + type;
    toast.textContent = message;
    container.appendChild(toast);

    toast.offsetHeight;
    toast.classList.add('toast-visible');

    setTimeout(function () {
        toast.classList.remove('toast-visible');
        toast.addEventListener('transitionend', function () { toast.remove(); }, { once: true });
        setTimeout(function () { if (toast.parentNode) toast.remove(); }, 500);
    }, TOAST_DURATION);
};

/* Tab Router */

var tabInitFunctions = {};

window.registerTabInit = function (tabName, fn) {
    tabInitFunctions[tabName] = fn;
};

var activateTab = function (tabName) {
    document.querySelectorAll('.sidebar-icon[data-tab]').forEach(function (icon) {
        icon.classList.toggle('active', icon.dataset.tab === tabName);
    });

    document.querySelectorAll('.tab-panel').forEach(function (panel) {
        var match = panel.id === 'tab-' + tabName;
        panel.classList.toggle('active', match);
        panel.style.display = match ? '' : 'none';
    });

    if (typeof tabInitFunctions[tabName] === 'function') {
        tabInitFunctions[tabName]();
    }
};

var setupTabRouter = function () {
    document.querySelectorAll('.sidebar-icon[data-tab]').forEach(function (icon) {
        icon.addEventListener('click', function () {
            activateTab(icon.dataset.tab);
        });
    });
};

/* SSE Client with Auto-Reconnect */

var sseSource = null;
var sseRetryDelay = 1000;
var SSE_MAX_DELAY = 30000;

var setConnectionStatus = function (connected) {
    var el = document.getElementById('status-connection');
    if (!el) return;
    el.classList.toggle('connected', connected);
    el.classList.toggle('disconnected', !connected);
    el.title = connected ? 'Connected' : 'Disconnected';
};

var handleSSEMessage = function (event) {
    try {
        var data = JSON.parse(event.data);

        if (data.system && typeof window.updateDashboard === 'function') {
            window.updateDashboard(data.system);
        }
        if (data.can && typeof window.updateCANStats === 'function') {
            window.updateCANStats(data.can);
        }

        updateStatusBar(data);
    } catch (err) {
        console.error('[SSE] Failed to parse message:', err);
    }
};

var updateStatusBar = function (data) {
    if (data.system) {
        var uptimeEl = document.getElementById('status-uptime');
        if (uptimeEl && data.system.uptime != null) {
            uptimeEl.textContent = 'Up: ' + String(data.system.uptime);
        }
    }
    if (data.can) {
        var can0El = document.getElementById('status-can0');
        var can1El = document.getElementById('status-can1');
        if (can0El && data.can.can0RxFps != null) {
            can0El.textContent = 'CAN0 RX:' + data.can.can0RxFps + ' TX:' + (data.can.can0TxFps || 0) + ' fps';
        }
        if (can1El && data.can.can1RxFps != null) {
            can1El.textContent = 'CAN1 RX:' + data.can.can1RxFps + ' TX:' + (data.can.can1TxFps || 0) + ' fps';
        }
    }
};

var connectSSE = function () {
    if (sseSource) {
        sseSource.close();
    }

    sseSource = new EventSource('/api/sse');

    sseSource.onopen = function () {
        sseRetryDelay = 1000;
        setConnectionStatus(true);
    };

    sseSource.onmessage = handleSSEMessage;

    sseSource.onerror = function () {
        setConnectionStatus(false);
        sseSource.close();
        sseSource = null;

        console.warn('[SSE] Connection lost. Retrying in ' + (sseRetryDelay / 1000) + 's...');
        setTimeout(connectSSE, sseRetryDelay);
        sseRetryDelay = Math.min(sseRetryDelay * 2, SSE_MAX_DELAY);
    };
};

/* Clock Display */

var startClock = function () {
    var el = document.getElementById('status-time');
    if (!el) return;

    var tick = function () {
        var now = new Date();
        el.textContent = now.toLocaleTimeString('en-US', { hour12: false });
    };

    tick();
    setInterval(tick, 1000);
};

/* Poweroff Modal */

var setupPoweroffModal = function () {
    var modal = document.getElementById('poweroff-modal');
    var openBtn = document.getElementById('power-btn');
    var closeBtn = document.getElementById('poweroff-modal-close');
    var cancelBtn = document.getElementById('poweroff-cancel');
    var shutdownBtn = document.getElementById('poweroff-shutdown-btn');
    var rebootBtn = document.getElementById('poweroff-reboot-btn');

    if (!modal || !openBtn) return;

    var show = function () { modal.classList.add('active'); };
    var hide = function () { modal.classList.remove('active'); };

    openBtn.addEventListener('click', show);
    closeBtn.addEventListener('click', hide);
    cancelBtn.addEventListener('click', hide);
    modal.querySelector('.modal-overlay').addEventListener('click', hide);

    var doAction = function (action) {
        window.api('/api/system/poweroff', {
            method: 'POST',
            body: JSON.stringify({ action: action })
        }).then(function () {
            window.showToast('System is ' + action + '...', 'success');
        }).catch(function () {
            // api() already shows toast on error
        });
    };

    shutdownBtn.addEventListener('click', function () { doAction('poweroff'); });
    rebootBtn.addEventListener('click', function () { doAction('reboot'); });
};

/* Init */

document.addEventListener('DOMContentLoaded', function () {
    setupTabRouter();
    connectSSE();
    activateTab('dashboard');
    startClock();
    setupPoweroffModal();
});
