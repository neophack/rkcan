/**
 * replay.js - TF/SD card storage, CAN log replay (ASC/BLF) and recording.
 */
(function () {
    'use strict';

    var $ = function (id) { return document.getElementById(id); };
    var esc = function (s) { return window.escapeHtml(String(s == null ? '' : s)); };
    var timer = null;
    var wired = false;
    var ifaces = ['can0', 'can1'];
    var fileInfo = null;
    var volumes = [];

    var STATE_TEXT = {
        idle: '空闲', playing: '回放中', paused: '已暂停',
        finished: '已完成', stopped: '已停止', error: '错误'
    };

    function post(url, body) {
        return window.api(url, { method: 'POST', body: JSON.stringify(body || {}) });
    }

    function fmtDur(sec) {
        sec = Math.max(0, sec || 0);
        var h = Math.floor(sec / 3600), m = Math.floor(sec % 3600 / 60), s = sec % 60;
        return (h ? h + ':' + String(m).padStart(2, '0') : m) + ':' + s.toFixed(1).padStart(4, '0');
    }

    /* ---------- Storage ---------- */

    function loadStorage() {
        return fetch('/api/storage').then(function (r) { return r.json(); }).then(function (d) {
            volumes = d.volumes || [];
            var rows = '';
            volumes.forEach(function (v) {
                rows += '<tr><td class="mono">' + esc(v.mountPoint) + (v.label ? ' (' + esc(v.label) + ')' : '') +
                    '</td><td>' + (v.removable ? esc(v.device) : '内部存储') + '</td><td>' + esc(v.fsType || '-') +
                    '</td><td>' + window.formatBytes(v.total) + '</td><td>' + window.formatBytes(v.free) + '</td><td>' +
                    (v.removable ? '<button class="btn btn-sm" data-unmount="' + esc(v.mountPoint) + '">安全移除</button>' : '') +
                    '</td></tr>';
            });
            (d.cards || []).forEach(function (c) {
                rows += '<tr><td class="text-muted">未挂载</td><td>' + esc(c.device) + (c.label ? ' (' + esc(c.label) + ')' : '') +
                    '</td><td>-</td><td>' + window.formatBytes(c.size) + '</td><td>-</td><td>' +
                    '<button class="btn btn-sm btn-primary" data-mount="' + esc(c.device) + '">挂载</button></td></tr>';
            });
            $('storage-body').innerHTML = rows || '<tr><td colspan="6" class="text-muted center">未检测到 TF 卡或 U 盘</td></tr>';

            // Recording destinations: removable volumes first
            var sel = $('record-dir');
            var cur = sel.value;
            var opts = volumes.slice().sort(function (a, b) { return (b.removable ? 1 : 0) - (a.removable ? 1 : 0); });
            sel.innerHTML = opts.map(function (v) {
                var dir = v.mountPoint.replace(/\/$/, '') + '/canlog';
                return '<option value="' + esc(dir) + '">' + esc(dir) + ' (可用 ' + window.formatBytes(v.free) + ')</option>';
            }).join('');
            if (cur) sel.value = cur;
        }).catch(function () {});
    }

    function onStorageClick(e) {
        var dev = e.target.getAttribute('data-mount');
        var mp = e.target.getAttribute('data-unmount');
        if (dev) {
            e.target.disabled = true;
            post('/api/storage/mount', { device: dev }).then(function (d) {
                window.showToast('已挂载到 ' + d.mountPoint, 'success');
                loadStorage().then(loadFiles);
            }).catch(function () { e.target.disabled = false; });
        } else if (mp) {
            e.target.disabled = true;
            post('/api/storage/unmount', { mountPoint: mp }).then(function () {
                window.showToast('可以安全拔出存储卡了', 'success');
                loadStorage().then(loadFiles);
                refreshStatus();
            }).catch(function () { e.target.disabled = false; });
        }
    }

    /* ---------- Files ---------- */

    function loadFiles() {
        return fetch('/api/replay/files').then(function (r) { return r.json(); }).then(function (files) {
            var sel = $('replay-file');
            var cur = sel.value;
            sel.innerHTML = '<option value="">-- 选择文件 (' + (files || []).length + ') --</option>' +
                (files || []).map(function (f) {
                    return '<option value="' + esc(f.path) + '">' + esc(f.path) + '  [' + window.formatBytes(f.size) +
                        ', ' + esc(f.modTime) + ']</option>';
                }).join('');
            if (cur) sel.value = cur;
        }).catch(function () {});
    }

    function loadInfo() {
        var path = $('replay-file').value;
        fileInfo = null;
        $('replay-chanmap').innerHTML = '';
        if (!path) {
            $('replay-info').textContent = '选择文件后显示帧数、时长和通道。';
            return;
        }
        $('replay-info').textContent = '正在分析文件…';
        fetch('/api/replay/info?path=' + encodeURIComponent(path)).then(function (r) {
            return r.json().then(function (d) { if (!r.ok) throw new Error(d.error || r.status); return d; });
        }).then(function (info) {
            if ($('replay-file').value !== path) return;
            fileInfo = info;
            $('replay-info').textContent = '帧数 ' + window.formatNumber(info.frames) + ' · 时长 ' + fmtDur(info.duration) +
                ' · 通道 ' + (info.channels || []).join(', ') + ' · CAN-FD ' + window.formatNumber(info.fdFrames) +
                ' (BRS ' + window.formatNumber(info.brsFrames) + ')';
            renderChanMap(info.channels || []);
        }).catch(function (err) {
            $('replay-info').textContent = '无法读取文件: ' + err.message;
        });
    }

    function renderChanMap(channels) {
        var html = '';
        // Default: channels 1..N map to the interfaces in order; when the
        // file uses other numbers, map its channels by position instead.
        var direct = channels.every(function (ch) { return ch >= 1 && ch <= ifaces.length; });
        channels.forEach(function (ch, i) {
            var def = direct ? ifaces[ch - 1] : (i < ifaces.length ? ifaces[i] : '');
            html += '<div class="form-group"><label>文件通道 ' + ch + ' →</label><select class="form-select" data-ch="' + ch + '">' +
                '<option value="">不发送</option>' +
                ifaces.map(function (n) {
                    return '<option value="' + esc(n) + '"' + (n === def ? ' selected' : '') + '>' + esc(n) + '</option>';
                }).join('') + '</select></div>';
        });
        $('replay-chanmap').innerHTML = html;
    }

    /* ---------- Replay control ---------- */

    function startReplay() {
        var path = $('replay-file').value;
        if (!path) { window.showToast('请先选择日志文件', 'error'); return; }
        var map = {};
        document.querySelectorAll('#replay-chanmap select[data-ch]').forEach(function (s) {
            map[s.getAttribute('data-ch')] = s.value;
        });
        if (!Object.keys(map).length) { window.showToast('文件信息尚未加载完成', 'error'); return; }
        post('/api/replay/start', {
            path: path,
            speed: parseFloat($('replay-speed').value),
            loop: $('replay-loop').checked,
            brs: $('replay-brs').value,
            direction: $('replay-dir').value,
            channelMap: map
        }).then(renderReplay).catch(function () {});
    }

    function renderReplay(st) {
        if (!st) return;
        var active = st.state === 'playing' || st.state === 'paused';
        $('replay-badge').textContent = STATE_TEXT[st.state] || st.state;
        $('replay-badge').className = 'card-header-badge ' + (active ? 'timesync-badge-running' : 'timesync-badge-stopped');
        $('replay-pause').disabled = !active;
        $('replay-pause').textContent = st.state === 'paused' ? '▶ 继续' : '⏸ 暂停';
        $('replay-stop').disabled = !active;
        if (active && document.activeElement !== $('replay-speed')) {
            $('replay-speed').value = String(st.speed);
        }

        var pct = st.duration > 0 ? Math.min(100, st.position / st.duration * 100) : 0;
        if (st.state === 'finished') pct = 100;
        $('replay-progress').style.width = pct.toFixed(1) + '%';

        if (st.state === 'idle') { $('replay-stats').textContent = '--'; return; }
        var parts = [
            fmtDur(st.position) + ' / ' + (st.duration ? fmtDur(st.duration) : '…'),
            '已发送 ' + window.formatNumber(st.sent) + (st.total ? ' / ' + window.formatNumber(st.total) : ''),
            '跳过 ' + window.formatNumber(st.skipped),
            '错误 ' + window.formatNumber(st.errors),
            '速度 ' + (st.speed ? st.speed + '×' : '最快')
        ];
        if (st.loop) parts.push('循环 ' + st.loops);
        if (st.state === 'playing' && st.lagMs > 50) parts.push('滞后 ' + st.lagMs.toFixed(0) + ' ms');
        if (st.error) parts.push('错误: ' + st.error);
        $('replay-stats').textContent = parts.join('  ·  ');
    }

    /* ---------- Recording ---------- */

    function renderRecordIfaces() {
        $('record-ifaces').innerHTML = ifaces.map(function (n) {
            return '<label class="checkbox-label"><input type="checkbox" value="' + esc(n) + '" checked> ' + esc(n) + '</label>';
        }).join(' ');
    }

    function startRecord() {
        var dir = $('record-dir').value;
        if (!dir) { window.showToast('没有可用的存储位置', 'error'); return; }
        var sel = [];
        document.querySelectorAll('#record-ifaces input:checked').forEach(function (c) { sel.push(c.value); });
        if (!sel.length) { window.showToast('请至少选择一个接口', 'error'); return; }
        post('/api/record/start', {
            dir: dir, ifaces: sel, maxSizeMB: parseInt($('record-maxsize').value, 10) || 0
        }).then(renderRecord).catch(function () {});
    }

    function renderRecord(st) {
        if (!st) return;
        $('record-badge').textContent = st.active ? '记录中' : (st.error ? '错误' : '未记录');
        $('record-badge').className = 'card-header-badge ' + (st.active ? 'timesync-badge-running' : 'timesync-badge-stopped');
        $('record-start').disabled = st.active;
        $('record-stop').disabled = !st.active;
        if (!st.file) { $('record-stats').textContent = '--'; return; }
        var parts = [
            st.file,
            window.formatNumber(st.frames) + ' 帧',
            window.formatBytes(st.bytes)
        ];
        if (st.files && st.files.length > 1) parts.push('共 ' + st.files.length + ' 个文件');
        if (st.dropped) parts.push('丢弃 ' + window.formatNumber(st.dropped));
        if (st.error) parts.push('错误: ' + st.error);
        $('record-stats').textContent = parts.join('  ·  ');
    }

    /* ---------- Polling ---------- */

    function refreshStatus() {
        fetch('/api/replay/status').then(function (r) { return r.json(); }).then(renderReplay).catch(function () {});
        fetch('/api/record/status').then(function (r) { return r.json(); }).then(renderRecord).catch(function () {});
    }

    function tabActive() {
        var t = $('tab-replay');
        return t && t.classList.contains('active');
    }

    function wire() {
        if (wired) return;
        wired = true;
        $('storage-refresh').addEventListener('click', function () { loadStorage().then(loadFiles); });
        $('storage-body').addEventListener('click', onStorageClick);
        $('replay-files-refresh').addEventListener('click', loadFiles);
        $('replay-file').addEventListener('change', loadInfo);
        $('replay-start').addEventListener('click', startReplay);
        $('replay-stop').addEventListener('click', function () { post('/api/replay/stop').then(renderReplay).catch(function () {}); });
        $('replay-pause').addEventListener('click', function () {
            var paused = $('replay-pause').textContent.indexOf('继续') >= 0;
            post(paused ? '/api/replay/resume' : '/api/replay/pause').then(renderReplay).catch(function () {});
        });
        $('replay-speed').addEventListener('change', function () {
            if ($('replay-stop').disabled) return; // not playing: applies on start
            post('/api/replay/speed', { speed: parseFloat($('replay-speed').value) }).then(renderReplay).catch(function () {});
        });
        $('record-start').addEventListener('click', startRecord);
        $('record-stop').addEventListener('click', function () { post('/api/record/stop').then(renderRecord).catch(function () {}); });
    }

    window.initReplay = function () {
        wire();
        (window.canIfacesPromise || Promise.resolve(ifaces)).then(function (list) {
            ifaces = list;
            if (!$('record-ifaces').children.length) renderRecordIfaces();
        });
        loadStorage().then(loadFiles);
        refreshStatus();
        if (!timer) {
            timer = setInterval(function () {
                if (!tabActive()) { clearInterval(timer); timer = null; return; }
                refreshStatus();
            }, 500);
        }
    };

    window.registerTabInit('replay', window.initReplay);
})();
