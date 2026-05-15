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

    function openConnectModal(ssid) {
        var modal = document.getElementById('wifi-connect-modal');
        var ssidInput = document.getElementById('wifi-connect-ssid');
        var passInput = document.getElementById('wifi-connect-password');
        if (!modal) return;

        if (ssidInput) ssidInput.value = ssid;
        if (passInput) passInput.value = '';
        modal.classList.add('active');
        if (passInput) passInput.focus();
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

        window.api('/api/wifi/connect', {
            method: 'POST',
            body: JSON.stringify({ ssid: ssid, password: password }),
        }).then(function () {
            window.showToast('Connecting to ' + ssid + '...', 'info');
            closeConnectModal();
            setTimeout(loadStatus, 3000);
        }).catch(function () {});
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
        scanNetworks();
        startStatusPolling();
    };

    window.registerTabInit('wifi', window.initWiFi);

})();
