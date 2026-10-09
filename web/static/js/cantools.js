/**
 * cantools.js - CAN frame transmission (single / periodic) and live monitor.
 */
(function () {
    'use strict';

    var $ = function (id) { return document.getElementById(id); };
    var monTimer = null;
    var taskTimer = null;
    var monPaused = false;
    var lastData = {};
    var wired = false;

    /* ---------- Interface lists ---------- */

    window.canIfacesPromise = window.canIfacesPromise || fetch('/api/version')
        .then(function (r) { return r.ok ? r.json() : {}; })
        .then(function (d) { return (d && d.canIfaces) || ['can0', 'can1']; })
        .catch(function () { return ['can0', 'can1']; });

    function fillIfaceSelects() {
        window.canIfacesPromise.then(function (ifaces) {
            document.querySelectorAll('.can-iface-select').forEach(function (sel) {
                if (sel.options.length) return;
                ifaces.forEach(function (n) {
                    var o = document.createElement('option');
                    o.value = n; o.textContent = n;
                    sel.appendChild(o);
                });
            });
            var mon = $('canmon-iface');
            if (mon && mon.options.length <= 1) {
                ifaces.forEach(function (n) {
                    var o = document.createElement('option');
                    o.value = n; o.textContent = n;
                    mon.appendChild(o);
                });
            }
        });
    }

    /* ---------- Transmit ---------- */

    function dataBytes() {
        var v = ($('cantx-data').value || '').replace(/0x/gi, '').replace(/[\s,:-]/g, '');
        return v.length / 2;
    }

    function updateFlags() {
        var fd = $('cantx-fd').checked;
        $('cantx-brs').disabled = !fd;
        if (!fd) $('cantx-brs').checked = false;
        $('cantx-rtr').disabled = fd;
        if (fd) $('cantx-rtr').checked = false;
        $('cantx-data').disabled = $('cantx-rtr').checked;
        var n = dataBytes();
        $('cantx-len').textContent = '(' + (n % 1 ? '?' : n) + ' 字节)';
    }

    function spec() {
        return {
            iface: $('cantx-iface').value,
            id: $('cantx-id').value.trim(),
            ext: $('cantx-ext').checked,
            fd: $('cantx-fd').checked,
            brs: $('cantx-brs').checked,
            rtr: $('cantx-rtr').checked,
            data: $('cantx-data').value
        };
    }

    function sendOnce() {
        window.api('/api/can/send', { method: 'POST', body: JSON.stringify(spec()) })
            .then(function () { window.showToast('已发送', 'success'); })
            .catch(function () {});
    }

    function startPeriodic() {
        var body = spec();
        body.intervalMs = parseInt($('cantx-interval').value, 10) || 0;
        body.count = parseInt($('cantx-count').value, 10) || 0;
        window.api('/api/can/periodic', { method: 'POST', body: JSON.stringify(body) })
            .then(function () { loadTasks(); })
            .catch(function () {});
    }

    function stopTask(id) {
        window.api('/api/can/periodic/stop', { method: 'POST', body: JSON.stringify({ id: id }) })
            .then(loadTasks).catch(function () {});
    }

    function loadTasks() {
        fetch('/api/can/periodic').then(function (r) { return r.json(); }).then(function (tasks) {
            var tb = $('cantx-tasks-body');
            if (!tasks || !tasks.length) {
                tb.innerHTML = '<tr><td colspan="9" class="text-muted center">无周期任务</td></tr>';
                return;
            }
            tb.innerHTML = tasks.map(function (t) {
                var s = t.spec;
                var type = (s.fd ? 'FD' : 'CAN') + (s.brs ? '+BRS' : '') + (s.ext ? ' EXT' : '') + (s.rtr ? ' RTR' : '');
                var state = t.running ? '' : ' (完成)';
                return '<tr><td>' + t.id + '</td><td>' + window.escapeHtml(s.iface) + '</td><td class="mono">' +
                    window.escapeHtml(s.id.toUpperCase()) + '</td><td>' + type + '</td><td class="mono">' +
                    window.escapeHtml(s.data) + '</td><td>' + t.intervalMs + ' ms</td><td>' + t.sent + state +
                    '</td><td title="' + window.escapeHtml(t.lastError || '') + '">' + t.errors + '</td>' +
                    '<td><button class="btn btn-sm btn-danger" data-stop="' + t.id + '">停止</button></td></tr>';
            }).join('');
        }).catch(function () {});
    }

    /* ---------- Monitor ---------- */

    function loadMonitor() {
        fetch('/api/can/monitor').then(function (r) { return r.json(); }).then(function (snap) {
            var iface = $('canmon-iface').value;
            var filter = ($('canmon-filter').value || '').trim().toUpperCase().replace(/^0X/, '');
            var rows = (snap.entries || []).filter(function (e) {
                return (!iface || e.iface === iface) && (!filter || e.id.indexOf(filter) >= 0);
            });

            var totals = snap.totals || {};
            $('canmon-summary').textContent = Object.keys(totals).sort().map(function (k) {
                return k + ': ' + window.formatNumber(totals[k]);
            }).join('  ') + '  ·  ' + rows.length + ' ID' + (snap.dropped ? '  ·  超出上限 ' + snap.dropped : '');

            var tb = $('canmon-body');
            if (!rows.length) {
                tb.innerHTML = '<tr><td colspan="7" class="text-muted center">等待报文…</td></tr>';
                return;
            }
            var html = '';
            var seen = {};
            for (var i = 0; i < rows.length; i++) {
                var e = rows[i];
                var key = e.iface + ':' + e.id;
                seen[key] = true;
                var changed = lastData[key] !== undefined && lastData[key] !== e.data;
                lastData[key] = e.data;
                var type = e.rtr ? 'RTR' : (e.fd ? (e.brs ? 'FD+BRS' : 'FD') : 'CAN');
                var stale = e.ageMs > 3000 ? ' class="stale"' : '';
                html += '<tr' + stale + '><td>' + window.escapeHtml(e.iface) + '</td><td>' + e.id + (e.ext ? 'x' : '') +
                    '</td><td>' + type + '</td><td>' + e.len + '</td><td class="' + (changed ? 'data-changed' : '') + '">' +
                    e.data + '</td><td>' + (e.periodMs ? e.periodMs.toFixed(1) : '-') + '</td><td>' + e.count + '</td></tr>';
            }
            tb.innerHTML = html;
            for (var k in lastData) { if (!seen[k]) delete lastData[k]; }
        }).catch(function () {});
    }

    function canTabActive() {
        var t = $('tab-can');
        return t && t.classList.contains('active');
    }

    function startPolling() {
        if (monTimer) return;
        loadMonitor();
        loadTasks();
        monTimer = setInterval(function () {
            if (!canTabActive()) { stopPolling(); return; }
            loadMonitor();
        }, 500);
        taskTimer = setInterval(function () { if (canTabActive()) loadTasks(); }, 2000);
    }

    function stopPolling() {
        clearInterval(monTimer); monTimer = null;
        clearInterval(taskTimer); taskTimer = null;
    }

    function wire() {
        if (wired) return;
        wired = true;
        ['cantx-fd', 'cantx-rtr'].forEach(function (id) { $(id).addEventListener('change', updateFlags); });
        $('cantx-data').addEventListener('input', updateFlags);
        $('cantx-send').addEventListener('click', sendOnce);
        $('cantx-periodic').addEventListener('click', startPeriodic);
        $('cantx-stopall').addEventListener('click', function () { stopTask(0); });
        $('cantx-tasks-body').addEventListener('click', function (e) {
            var id = e.target && e.target.getAttribute('data-stop');
            if (id) stopTask(parseInt(id, 10));
        });
        $('canmon-pause').addEventListener('click', function () {
            monPaused = !monPaused;
            $('canmon-pause').textContent = monPaused ? '继续' : '暂停';
            window.api('/api/can/monitor/pause', { method: 'POST', body: JSON.stringify({ paused: monPaused }) }).catch(function () {});
        });
        $('canmon-reset').addEventListener('click', function () {
            lastData = {};
            window.api('/api/can/monitor/reset', { method: 'POST', body: '{}' }).then(loadMonitor).catch(function () {});
        });
        updateFlags();
    }

    document.addEventListener('DOMContentLoaded', function () {
        wire();
        fillIfaceSelects();
        // Chain onto the CAN tab's existing init (registered by can.js)
        var prevInit = window.initCAN;
        window.registerTabInit('can', function () {
            if (typeof prevInit === 'function') prevInit();
            startPolling();
        });
    });
})();
