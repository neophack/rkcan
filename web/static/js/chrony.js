/**
 * chrony.js - Chrony NTP status viewer for the RKCAN Dashboard.
 */

(function () {
    var hasLoaded = false;

    // Elements
    var refreshBtn = document.getElementById('chrony-refresh-btn');
    var leapBadge = document.getElementById('chrony-leap-badge');

    var refIdEl = document.getElementById('chrony-ref-id');
    var stratumEl = document.getElementById('chrony-stratum');
    var sysTimeEl = document.getElementById('chrony-system-time');
    var lastOffEl = document.getElementById('chrony-last-offset');
    var rmsOffEl = document.getElementById('chrony-rms-offset');
    var resFreqEl = document.getElementById('chrony-residual-freq');
    var updIntEl = document.getElementById('chrony-update-interval');
    var rootDelayEl = document.getElementById('chrony-root-delay');

    var sourcesTbody = document.getElementById('chrony-sources-tbody');
    var sourcestatsTbody = document.getElementById('chrony-sourcestats-tbody');

    var configPre = document.getElementById('chrony-config-pre');
    var configTextarea = document.getElementById('chrony-config-textarea');
    var configEditBtn = document.getElementById('chrony-config-edit-btn');
    var configSaveBtn = document.getElementById('chrony-config-save-btn');
    var configCancelBtn = document.getElementById('chrony-config-cancel-btn');

    var cachedConfig = '';

    var setLeapBadge = function (status) {
        if (!leapBadge) return;
        leapBadge.textContent = status || 'Unknown';
        leapBadge.className = 'chrony-status-badge';
        if (status === 'Normal') {
            leapBadge.classList.add('ok');
            leapBadge.textContent = '✓ ' + status;
        } else if (status === 'Not synchronised') {
            leapBadge.classList.add('error');
            leapBadge.textContent = '✗ ' + status;
        } else {
            leapBadge.classList.add('warn');
        }
    };

    var renderTracking = function (t) {
        if (!t) return;
        if (refIdEl) refIdEl.textContent = t.referenceID || '--';
        if (stratumEl) stratumEl.textContent = t.stratum != null ? String(t.stratum) : '--';
        if (sysTimeEl) sysTimeEl.textContent = t.systemTime || '--';
        if (lastOffEl) lastOffEl.textContent = t.lastOffset || '--';
        if (rmsOffEl) rmsOffEl.textContent = t.rmsOffset || '--';
        if (resFreqEl) resFreqEl.textContent = t.residualFreq || '--';
        if (updIntEl) updIntEl.textContent = t.updateInterval || '--';
        if (rootDelayEl) rootDelayEl.textContent = t.rootDelay || '--';
        setLeapBadge(t.leapStatus);
    };

    var sourceStatusClass = function (state, selected) {
        if (selected) return 'selected';
        if (state && state.indexOf('?') >= 0) return 'unreachable';
        if (state && state.indexOf('+') >= 0) return 'backup';
        if (state && state.indexOf('-') >= 0) return 'unreachable';
        return '';
    };

    var sourceStatusLabel = function (state, selected) {
        if (selected) return '主源';
        if (state && state.indexOf('+') >= 0) return '备份';
        if (state && state.indexOf('?') >= 0) return '不可用';
        if (state && state.indexOf('-') >= 0) return '不可用';
        if (state && state.indexOf('x') >= 0) return '异常';
        return state || '--';
    };

    var renderSources = function (sources) {
        if (!sourcesTbody) return;
        if (!sources || sources.length === 0) {
            sourcesTbody.innerHTML = '<tr><td colspan="7" style="color:var(--text-muted)">No sources found</td></tr>';
            return;
        }
        sourcesTbody.innerHTML = sources.map(function (s) {
            var cls = sourceStatusClass(s.state, s.selected);
            var label = sourceStatusLabel(s.state, s.selected);
            return '<tr>' +
                '<td><span class="chrony-source-status ' + cls + '"><span class="dot"></span>' + window.escapeHtml(label) + '</span></td>' +
                '<td>' + window.escapeHtml(s.name || '--') + '</td>' +
                '<td>' + (s.stratum != null ? s.stratum : '--') + '</td>' +
                '<td>' + (s.poll != null ? s.poll : '--') + '</td>' +
                '<td>' + (s.reach != null ? s.reach : '--') + '</td>' +
                '<td>' + (s.lastRx != null ? s.lastRx + 's' : '--') + '</td>' +
                '<td>' + window.escapeHtml(s.offset || '--') + '</td>' +
                '</tr>';
        }).join('');
    };

    var renderSourceStats = function (stats) {
        if (!sourcestatsTbody) return;
        if (!stats || stats.length === 0) {
            sourcestatsTbody.innerHTML = '<tr><td colspan="8" style="color:var(--text-muted)">No statistics found</td></tr>';
            return;
        }
        sourcestatsTbody.innerHTML = stats.map(function (s) {
            return '<tr>' +
                '<td>' + window.escapeHtml(s.name || '--') + '</td>' +
                '<td>' + (s.np != null ? s.np : '--') + '</td>' +
                '<td>' + (s.nr != null ? s.nr : '--') + '</td>' +
                '<td>' + window.escapeHtml(s.span || '--') + '</td>' +
                '<td>' + window.escapeHtml(s.frequency || '--') + '</td>' +
                '<td>' + window.escapeHtml(s.freqSkew || '--') + '</td>' +
                '<td>' + window.escapeHtml(s.offset || '--') + '</td>' +
                '<td>' + window.escapeHtml(s.stdDev || '--') + '</td>' +
                '</tr>';
        }).join('');
    };

    var renderConfig = function (text) {
        cachedConfig = text || '';
        if (configPre) configPre.textContent = cachedConfig;
        if (configTextarea) configTextarea.value = cachedConfig;
    };

    var loadChrony = function () {
        if (leapBadge) leapBadge.textContent = 'Loading...';
        window.api('/api/chrony')
            .then(function (data) {
                renderTracking(data.tracking);
                renderSources(data.sources);
                renderSourceStats(data.sourceStats);
                renderConfig(data.config);
                hasLoaded = true;
            })
            .catch(function () {
                setLeapBadge('Error');
                if (sourcesTbody) sourcesTbody.innerHTML = '<tr><td colspan="7" style="color:var(--text-muted)">Failed to load</td></tr>';
                if (sourcestatsTbody) sourcestatsTbody.innerHTML = '<tr><td colspan="8" style="color:var(--text-muted)">Failed to load</td></tr>';
            });
    };

    // Config editing
    var enterEditMode = function () {
        if (!configPre || !configTextarea || !configEditBtn || !configSaveBtn || !configCancelBtn) return;
        configPre.hidden = true;
        configTextarea.hidden = false;
        configTextarea.value = cachedConfig;
        configEditBtn.hidden = true;
        configSaveBtn.hidden = false;
        configCancelBtn.hidden = false;
        configTextarea.focus();
    };

    var exitEditMode = function () {
        if (!configPre || !configTextarea || !configEditBtn || !configSaveBtn || !configCancelBtn) return;
        configPre.hidden = false;
        configTextarea.hidden = true;
        configEditBtn.hidden = false;
        configSaveBtn.hidden = true;
        configCancelBtn.hidden = true;
    };

    var saveConfig = function () {
        if (!configTextarea) return;
        var newConfig = configTextarea.value;
        window.api('/api/chrony/config', {
            method: 'POST',
            body: JSON.stringify({ config: newConfig })
        }).then(function () {
            cachedConfig = newConfig;
            if (configPre) configPre.textContent = newConfig;
            exitEditMode();
            window.showToast('Configuration saved', 'success');
        }).catch(function () {
            // api() already shows toast on error
        });
    };

    var setup = function () {
        if (refreshBtn) refreshBtn.addEventListener('click', loadChrony);
        if (configEditBtn) configEditBtn.addEventListener('click', enterEditMode);
        if (configSaveBtn) configSaveBtn.addEventListener('click', saveConfig);
        if (configCancelBtn) configCancelBtn.addEventListener('click', exitEditMode);
    };

    window.registerTabInit('chrony', function () {
        if (!hasLoaded) {
            loadChrony();
        }
    });

    document.addEventListener('DOMContentLoaded', setup);
})();
