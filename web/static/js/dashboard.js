/**
 * dashboard.js - Dashboard tab: charts, metrics, time, temp, processes.
 */

(function () {
    'use strict';

    var cpuChart = null;
    var memChart = null;
    var netChart = null;
    var tempChart = null;
    var netTxIndex = -1;
    var initialized = false;
    var firstData = true;
    var processTimer = null;
    var lastTempHistoryLen = 0;

    function setText(id, text) {
        var el = document.getElementById(id);
        if (el) el.textContent = text;
    }

    function formatBytes(bytes) {
        if (bytes < 1024) return bytes.toFixed(0) + ' B';
        if (bytes < 1048576) return (bytes / 1024).toFixed(1) + ' KB';
        if (bytes < 1073741824) return (bytes / 1048576).toFixed(1) + ' MB';
        return (bytes / 1073741824).toFixed(2) + ' GB';
    }

    function formatSpeed(bytesPerSec) {
        if (bytesPerSec < 1024) return bytesPerSec.toFixed(0) + ' B/s';
        if (bytesPerSec < 1048576) return (bytesPerSec / 1024).toFixed(1) + ' KB/s';
        return (bytesPerSec / 1048576).toFixed(1) + ' MB/s';
    }

    function tempColor(temp) {
        if (temp < 50) return '#4ec9b0';
        if (temp <= 70) return '#eab308';
        return '#ef4444';
    }

    function createCharts() {
        if (cpuChart) return;

        var cpuCanvas = document.getElementById('cpu-chart');
        var memCanvas = document.getElementById('mem-chart');
        var netCanvas = document.getElementById('net-chart');

        if (cpuCanvas) {
            cpuChart = new LineChart(cpuCanvas, {
                lineColor: '#3794ff',
                fillColor: 'rgba(59,130,246,0.12)',
                yMax: 100,
                yLabel: '%',
                background: '#1e1e1e',
                gridColor: '#2b2b2b',
            });
        }

        if (memCanvas) {
            memChart = new LineChart(memCanvas, {
                lineColor: '#4ec9b0',
                fillColor: 'rgba(20,184,166,0.12)',
                yMax: 100,
                yLabel: '%',
                background: '#1e1e1e',
                gridColor: '#2b2b2b',
            });
        }

        if (netCanvas) {
            netChart = new LineChart(netCanvas, {
                lineColor: '#eab308',
                fillColor: 'rgba(234,179,8,0.08)',
                autoScale: true,
                yLabel: 'KB/s',
                showValue: false,
                background: '#1e1e1e',
                gridColor: '#2b2b2b',
                padding: { top: 8, right: 8, bottom: 20, left: 60 },
            });
            netTxIndex = netChart.addSeries({
                color: '#d18616',
                fillColor: 'rgba(249,115,22,0.08)',
                label: 'TX',
            });
        }

        var tempCanvas = document.getElementById('temp-chart');
        if (tempCanvas) {
            tempChart = new LineChart(tempCanvas, {
                lineColor: '#ef4444',
                fillColor: 'rgba(239,68,68,0.12)',
                yMax: 100,
                yLabel: '\u00B0C',
                background: '#1e1e1e',
                gridColor: '#2b2b2b',
            });
        }
    }

    function updateMetricCards(data) {
        if (data.cpu) {
            setText('metric-cpu', data.cpu.usagePercent.toFixed(1) + '%');
        }
        if (data.memory) {
            setText('metric-mem', formatBytes(data.memory.used) + ' / ' + formatBytes(data.memory.total));
        }
        if (data.temps && data.temps.length > 0) {
            var maxTemp = Math.max.apply(null, data.temps.map(function (t) { return t.temp; }));
            setText('metric-temp', maxTemp.toFixed(1) + '\u00B0C');
        }
        if (data.network && data.network.interfaces) {
            var totalRx = 0, totalTx = 0;
            var ifaces = data.network.interfaces;
            for (var name in ifaces) {
                totalRx += ifaces[name].rxBytesPerSec || 0;
                totalTx += ifaces[name].txBytesPerSec || 0;
            }
            setText('metric-net', '\u2193' + formatSpeed(totalRx) + ' \u2191' + formatSpeed(totalTx));
        }
        if (data.disk && data.disk.disks && data.disk.disks.length > 0) {
            var rootDisk = null;
            for (var i = 0; i < data.disk.disks.length; i++) {
                if (data.disk.disks[i].mountedOn === '/') {
                    rootDisk = data.disk.disks[i];
                    break;
                }
            }
            if (!rootDisk) {
                rootDisk = data.disk.disks[0];
            }
            setText('metric-disk', formatBytes(rootDisk.used) + ' / ' + formatBytes(rootDisk.total));
        }
    }

    function updateTimeDisplay(data) {
        if (!data.time) return;

        setText('system-time', data.time.systemTime || '--:--:--');

        var syncEl = document.getElementById('chrony-sync-status');
        if (syncEl && data.time.chronyLeapStatus) {
            var status = data.time.chronyLeapStatus;
            syncEl.textContent = status;
            if (status === 'Normal') {
                syncEl.style.color = '#4ec9b0';
            } else if (status === 'Not synchronised') {
                syncEl.style.color = '#ef4444';
            } else {
                syncEl.style.color = '#eab308';
            }
        }

        var stratumEl = document.getElementById('chrony-stratum-dash');
        if (stratumEl && data.time.chronyStratum != null) {
            stratumEl.textContent = String(data.time.chronyStratum);
            stratumEl.style.color = '';
        }
    }

    function updateTempDisplay(data) {
        if (!data.temps || data.temps.length === 0) return;
        var container = document.getElementById('temp-zones');
        if (!container) return;

        var html = '';
        for (var i = 0; i < data.temps.length; i++) {
            var t = data.temps[i];
            var color = tempColor(t.temp);
            var label = t.zone || t.type || ('Zone ' + i);
            html +=
                '<span style="display:inline-block;margin-right:12px;font-size:11px;color:' + color + '">' +
                label + ' ' + t.temp.toFixed(1) + '\u00B0C' +
                '</span>';
        }
        container.innerHTML = html;
    }

    function updateCharts(data) {
        if (cpuChart && data.cpu) {
            if (firstData && data.cpu.history && data.cpu.history.length > 0) {
                cpuChart.setData(data.cpu.history, 0);
            } else {
                cpuChart.addPoint(data.cpu.usagePercent, 0);
            }
        }

        if (memChart && data.memory) {
            if (firstData && data.memory.history && data.memory.history.length > 0) {
                memChart.setData(data.memory.history, 0);
            } else {
                memChart.addPoint(data.memory.usage, 0);
            }
        }

        if (netChart && data.network) {
            var net = data.network;
            if (firstData && net.rxHistory && net.rxHistory.length > 0) {
                var rxHist = net.rxHistory.map(function (v) { return v / 1024; });
                var txHist = (net.txHistory || []).map(function (v) { return v / 1024; });
                netChart.setData(rxHist, 0);
                if (netTxIndex >= 0) netChart.setData(txHist, netTxIndex);
            } else {
                netChart.addPoint((net.totalRx || 0) / 1024, 0);
                if (netTxIndex >= 0) {
                    netChart.addPoint((net.totalTx || 0) / 1024, netTxIndex);
                }
            }
        }

        if (tempChart && data.tempHistory && data.tempHistory.length > 0) {
            if (firstData) {
                tempChart.setData(data.tempHistory, 0);
                lastTempHistoryLen = data.tempHistory.length;
            } else if (data.tempHistory.length > lastTempHistoryLen) {
                var lastTemp = data.tempHistory[data.tempHistory.length - 1];
                tempChart.addPoint(lastTemp, 0);
                lastTempHistoryLen = data.tempHistory.length;
            }
        }
    }

    function fetchProcesses() {
        fetch('/api/system/processes')
            .then(function (res) { return res.json(); })
            .then(function (processes) { renderProcessTable(processes); })
            .catch(function () {});
    }

    function renderProcessTable(processes) {
        var tbody = document.getElementById('process-tbody');
        if (!tbody) return;

        processes.sort(function (a, b) { return (b.cpuPct || 0) - (a.cpuPct || 0); });

        var top = processes.slice(0, 20);

        var html = '';
        for (var i = 0; i < top.length; i++) {
            var p = top[i];
            html +=
                '<tr>' +
                    '<td>' + (p.pid || 0) + '</td>' +
                    '<td>' + (p.name || '-') + '</td>' +
                    '<td>' + (p.cpuPct || 0).toFixed(1) + '%</td>' +
                    '<td>' + formatBytes((p.memKB || 0) * 1024) + '</td>' +
                    '<td>' + (p.threads || 0) + '</td>' +
                '</tr>';
        }
        tbody.innerHTML = html;
        setText('process-count', String(processes.length));
    }

    function startProcessPolling() {
        if (processTimer) return;
        fetchProcesses();
        processTimer = setInterval(fetchProcesses, 5000);
    }

    function stopProcessPolling() {
        if (processTimer) {
            clearInterval(processTimer);
            processTimer = null;
        }
    }

    function handleResize() {
        if (cpuChart) cpuChart.resize();
        if (memChart) memChart.resize();
        if (netChart) netChart.resize();
        if (tempChart) tempChart.resize();
    }

    window.updateDashboard = function (data) {
        if (!data) return;

        if (!initialized) {
            createCharts();
            initialized = true;
            startProcessPolling();
        }

        updateMetricCards(data);
        updateTimeDisplay(data);
        updateTempDisplay(data);
        updateCharts(data);

        if (firstData) firstData = false;

        if (data.uptime) setText('status-uptime', 'Up: ' + data.uptime);
    };

    window.initDashboard = function () {
        createCharts();
        window.addEventListener('resize', handleResize);

        var dashTab = document.getElementById('tab-dashboard');
        if (dashTab && dashTab.classList.contains('active')) {
            startProcessPolling();
        }

        var observer = new MutationObserver(function (mutations) {
            for (var i = 0; i < mutations.length; i++) {
                if (mutations[i].attributeName === 'class') {
                    var el = mutations[i].target;
                    if (el.id === 'tab-dashboard') {
                        if (el.classList.contains('active')) {
                            handleResize();
                            startProcessPolling();
                        } else {
                            stopProcessPolling();
                        }
                    }
                }
            }
        });
        if (dashTab) {
            observer.observe(dashTab, { attributes: true, attributeFilter: ['class'] });
        }
    };

    // Wire up the set-time button as soon as the DOM is ready.
    document.addEventListener('DOMContentLoaded', function () {
        var btn = document.getElementById('settime-btn');
        if (!btn) return;
        btn.addEventListener('click', function () {
            var unixMs = Date.now();
            btn.disabled = true;
            btn.textContent = '同步中...';
            window.api('/api/system/settime', {
                method: 'POST',
                body: JSON.stringify({ unixMs: unixMs })
            }).then(function (res) {
                window.showToast('时间已同步: ' + res.time, 'success');
            }).catch(function () {
                // api() already shows error toast
            }).finally(function () {
                btn.disabled = false;
                btn.textContent = '同步到开发板';
            });
        });
    });

})();
