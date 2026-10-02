/**
 * wifi.js - WiFi management tab.
 */

(function () {
    'use strict';

    var statusTimer = null;
    var initialized = false;

    function setText(id, text) {
        var el = document.getElementById(id);
        if (el) el.textContent = text;
    }

    function renderSignalBars(strength) {
        var pct = Number(strength) || 0;
        var color, filled;
        if (pct > 75) { color = '#22c55e'; filled = 4; }
        else if (pct > 50) { color = '#eab308'; filled = 3; }
        else if (pct > 25) { color = '#f97316'; filled = 2; }
        else { color = '#ef4444'; filled = 1; }

        var html = '<span title="' + pct + '%">';
        for (var i = 1; i <= 4; i++) {
            var h = 4 + i * 4;
            var barColor = i <= filled ? color : '#475569';
            html += '<span style="display:inline-block;width:3px;height:' + h +
                'px;background:' + barColor + ';margin-right:1px;vertical-align:bottom;border-radius:1px;"></span>';
        }
        html += '</span>';
        return html;
    }

    function loadStatus() {
        window.api('/api/wifi/status').then(function (data) {
            if (!data) return;
            setText('wifi-state', data.connected ? 'Connected' : 'Disconnected');
            setText('wifi-ssid', data.ssid || '--');
            setText('wifi-ip', data.ip || '--');
            setText('wifi-mac', data.mac || '--');
            setText('wifi-signal', data.signal != null ? data.signal + '%' : '--');
            setText('wifi-freq', data.frequency || '--');
            setText('wifi-gateway', data.gateway || '--');

            var btn = document.getElementById('wifi-disconnect-btn');
            if (btn) btn.disabled = !data.connected;

            var be = { nmcli: 'NetworkManager', wpa_cli: 'wpa_supplicant' }[data.backend];
            setText('wifi-backend', be ? ('接口 ' + (data.iface || 'wlan0') + ' · 由 ' + be + ' 管理')
                : '未检测到 NetworkManager 或 wpa_supplicant，无法连接网络（仅可扫描）');
        }).catch(function () {});
    }

    function scanNetworks() {
        var scanBtn = document.getElementById('wifi-scan-btn');
        if (scanBtn) scanBtn.disabled = true;

        window.api('/api/wifi/scan').then(function (data) {
            var networks = Array.isArray(data) ? data : (data && data.networks) || [];
            renderScanResults(networks);
        }).catch(function () {}).finally(function () {
            if (scanBtn) scanBtn.disabled = false;
        });
    }

    function renderScanResults(networks) {
        var tbody = document.getElementById('wifi-scan-tbody');
        if (!tbody) return;

        if (networks.length === 0) {
            tbody.innerHTML = '<tr><td colspan="5" style="text-align:center;color:#64748b;">No networks found</td></tr>';
            return;
        }

        networks.sort(function (a, b) { return (b.signal || 0) - (a.signal || 0); });

        var html = '';
        for (var i = 0; i < networks.length; i++) {
            var n = networks[i];
            var ssid = window.escapeHtml(n.ssid || '(hidden)');
            html += '<tr data-ssid="' + window.escapeHtml(n.ssid || '') + '">' +
                '<td>' + ssid + (n.inUse ? ' <span style="color:#22c55e">*</span>' : '') + '</td>' +
                '<td>' + renderSignalBars(n.signal) + ' ' + (n.signal || 0) + '%</td>' +
                '<td>' + window.escapeHtml(n.frequency || '--') + '</td>' +
                '<td>' + window.escapeHtml(n.security || 'Open') + '</td>' +
                '<td><button class="btn btn-sm btn-primary wifi-connect-row-btn">Connect</button></td>' +
                '</tr>';
        }
        tbody.innerHTML = html;

        tbody.querySelectorAll('.wifi-connect-row-btn').forEach(function (btn) {
            btn.addEventListener('click', function (e) {
                e.stopPropagation();
                var row = btn.closest('tr');
                openConnectModal(row ? row.dataset.ssid : '');
            });
        });
    }

    function openConnectModal(ssid, manual) {
        var modal = document.getElementById('wifi-connect-modal');
        var ssidInput = document.getElementById('wifi-connect-ssid');
        var passInput = document.getElementById('wifi-connect-password');
        if (!modal) return;

        if (ssidInput) {
            ssidInput.value = ssid;
            ssidInput.readOnly = !manual;
        }
        if (passInput) passInput.value = '';
        document.getElementById('wifi-connect-hidden-row').style.display = manual ? '' : 'none';
        document.getElementById('wifi-connect-hidden').checked = !!manual;
        setConnecting(false);
        modal.classList.add('active');
        if (manual && ssidInput) ssidInput.focus();
        else if (passInput) passInput.focus();
    }

    function setConnecting(on) {
        document.getElementById('wifi-connect-progress').style.display = on ? '' : 'none';
        document.getElementById('wifi-connect-submit').disabled = on;
    }

    function loadSaved() {
        fetch('/api/wifi/saved').then(function (r) { return r.ok ? r.json() : []; }).then(function (nets) {
            var tb = document.getElementById('wifi-saved-body');
            if (!tb) return;
            if (!nets || !nets.length) {
                tb.innerHTML = '<tr><td class="text-muted center">没有已保存的网络</td></tr>';
                return;
            }
            tb.innerHTML = nets.map(function (n) {
                var id = window.escapeHtml(n.id);
                return '<tr><td>' + window.escapeHtml(n.ssid) + (n.current ? ' <span style="color:#22c55e">● 当前</span>' : '') +
                    '</td><td style="text-align:right;white-space:nowrap">' +
                    (n.current ? '' : '<button class="btn btn-sm btn-primary" data-saved-connect="' + id + '">连接</button> ') +
                    '<button class="btn btn-sm btn-danger" data-saved-forget="' + id + '">删除</button></td></tr>';
            }).join('');
        }).catch(function () {});
    }

    function onSavedClick(e) {
        var cid = e.target.getAttribute('data-saved-connect');
        var fid = e.target.getAttribute('data-saved-forget');
        if (cid) {
            e.target.disabled = true;
            window.showToast('正在连接…', 'info');
            window.api('/api/wifi/saved/connect', { method: 'POST', body: JSON.stringify({ id: cid }) })
                .then(function () { window.showToast('已连接', 'success'); loadStatus(); loadSaved(); })
                .catch(function () { e.target.disabled = false; });
        } else if (fid) {
            if (!confirm('删除这个已保存的网络？')) return;
            window.api('/api/wifi/saved/forget', { method: 'POST', body: JSON.stringify({ id: fid }) })
                .then(function () { loadSaved(); loadStatus(); }).catch(function () {});
        }
    }

    function closeConnectModal() {
        var modal = document.getElementById('wifi-connect-modal');
        if (modal) modal.classList.remove('active');
    }

    function submitConnect() {
        var ssid = (document.getElementById('wifi-connect-ssid') || {}).value || '';
        var password = (document.getElementById('wifi-connect-password') || {}).value || '';

        if (!ssid) {
            window.showToast('SSID is required', 'error');
            return;
        }

        var hidden = document.getElementById('wifi-connect-hidden').checked;
        setConnecting(true);
        window.api('/api/wifi/connect', {
            method: 'POST',
            body: JSON.stringify({ ssid: ssid, password: password, hidden: hidden }),
        }).then(function () {
            window.showToast('已连接到 ' + ssid, 'success');
            closeConnectModal();
            loadStatus();
            loadSaved();
        }).catch(function () {
            setConnecting(false);
        });
    }

    function disconnect() {
        window.api('/api/wifi/disconnect', { method: 'POST' }).then(function () {
            window.showToast('WiFi disconnected', 'info');
            loadStatus();
        }).catch(function () {});
    }

    function startStatusPolling() {
        stopStatusPolling();
        statusTimer = setInterval(function () {
            var tab = document.getElementById('tab-wifi');
            if (tab && tab.classList.contains('active')) loadStatus();
        }, 10000);
    }

    function stopStatusPolling() {
        if (statusTimer) {
            clearInterval(statusTimer);
            statusTimer = null;
        }
    }

    function setupListeners() {
        var scanBtn = document.getElementById('wifi-scan-btn');
        if (scanBtn) scanBtn.addEventListener('click', scanNetworks);

        var disconnectBtn = document.getElementById('wifi-disconnect-btn');
        if (disconnectBtn) disconnectBtn.addEventListener('click', disconnect);

        document.getElementById('wifi-hidden-btn').addEventListener('click', function () { openConnectModal('', true); });
        document.getElementById('wifi-saved-refresh').addEventListener('click', loadSaved);
        document.getElementById('wifi-saved-body').addEventListener('click', onSavedClick);

        var modalClose = document.getElementById('wifi-modal-close');
        if (modalClose) modalClose.addEventListener('click', closeConnectModal);

        var cancelBtn = document.getElementById('wifi-connect-cancel');
        if (cancelBtn) cancelBtn.addEventListener('click', closeConnectModal);

        var submitBtn = document.getElementById('wifi-connect-submit');
        if (submitBtn) submitBtn.addEventListener('click', submitConnect);

        var modal = document.getElementById('wifi-connect-modal');
        if (modal) {
            var overlay = modal.querySelector('.modal-overlay');
            if (overlay) overlay.addEventListener('click', closeConnectModal);
        }

        var passInput = document.getElementById('wifi-connect-password');
        if (passInput) {
            passInput.addEventListener('keydown', function (e) {
                if (e.key === 'Enter') submitConnect();
            });
        }
    }

    window.initWiFi = function () {
        if (!initialized) {
            setupListeners();
            initialized = true;
        }
        loadStatus();
        loadSaved();
        scanNetworks();
        startStatusPolling();
    };

    window.registerTabInit('wifi', window.initWiFi);

})();
