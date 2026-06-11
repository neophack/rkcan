/**
 * can.js - CAN bus management tab for the RKCAN Dashboard.
 * Sections: CAN Statistics, CAN Diagnostics, Bitrate Configuration.
 */

/* ========================================================================== */
/*  CAN Statistics (called by app.js on each SSE message)                      */
/* ========================================================================== */

window.updateCANStats = (data) => {
    if (!data) return;

    // -- CAN0 status cards --
    const can0State = document.getElementById('can0-state');
    const can0Bitrate = document.getElementById('can0-bitrate');
    if (can0State) {
        const state = data.can0State || 'DOWN';
        const rxFps = data.can0RxFps != null ? data.can0RxFps : 0;
        const txFps = data.can0TxFps != null ? data.can0TxFps : 0;
        if (state === 'UP' && (rxFps > 0 || txFps > 0)) {
            can0State.textContent = `UP  RX:${formatNumber(rxFps)}  TX:${formatNumber(txFps)} fps`;
        } else if (state === 'UP') {
            can0State.textContent = 'UP';
        } else {
            can0State.textContent = 'DOWN';
        }
        can0State.classList.toggle('text-ok', state === 'UP');
        can0State.classList.toggle('text-err', state !== 'UP');
    }
    if (can0Bitrate && data.can0Bitrate != null) {
        can0Bitrate.textContent = formatBitrateLabel(data.can0Bitrate);
    }
    const can0SamplePt = document.getElementById('can0-samplept');
    if (can0SamplePt && data.can0SamplePt != null) {
        can0SamplePt.textContent = (data.can0SamplePt * 100).toFixed(1) + ' %';
    }
    const can0DBitrate = document.getElementById('can0-dbitrate');
    if (can0DBitrate && data.can0DBitrate != null) {
        can0DBitrate.textContent = formatBitrateLabel(data.can0DBitrate);
    }
    const can0DSamplePt = document.getElementById('can0-dsamplept');
    if (can0DSamplePt && data.can0DSamplePt != null) {
        can0DSamplePt.textContent = (data.can0DSamplePt * 100).toFixed(1) + ' %';
    }
    const can0BusState = document.getElementById('can0-busstate');
    if (can0BusState && data.can0BusState != null) {
        can0BusState.textContent = data.can0BusState;
        const st = data.can0BusState;
        if (st === 'ERROR-ACTIVE') can0BusState.style.color = 'var(--green)';
        else if (st.includes('WARNING')) can0BusState.style.color = 'var(--yellow)';
        else if (st.includes('PASSIVE')) can0BusState.style.color = 'var(--orange)';
        else if (st.includes('BUS-OFF') || st.includes('STOPPED')) can0BusState.style.color = 'var(--red)';
        else can0BusState.style.color = '';
    }

    // -- CAN1 status cards --
    const can1State = document.getElementById('can1-state');
    const can1Bitrate = document.getElementById('can1-bitrate');
    if (can1State) {
        const state = data.can1State || 'DOWN';
        const rxFps = data.can1RxFps != null ? data.can1RxFps : 0;
        const txFps = data.can1TxFps != null ? data.can1TxFps : 0;
        if (state === 'UP' && (rxFps > 0 || txFps > 0)) {
            can1State.textContent = `UP  RX:${formatNumber(rxFps)}  TX:${formatNumber(txFps)} fps`;
        } else if (state === 'UP') {
            can1State.textContent = 'UP';
        } else {
            can1State.textContent = 'DOWN';
        }
        can1State.classList.toggle('text-ok', state === 'UP');
        can1State.classList.toggle('text-err', state !== 'UP');
    }
    if (can1Bitrate && data.can1Bitrate != null) {
        can1Bitrate.textContent = formatBitrateLabel(data.can1Bitrate);
    }
    const can1SamplePt = document.getElementById('can1-samplept');
    if (can1SamplePt && data.can1SamplePt != null) {
        can1SamplePt.textContent = (data.can1SamplePt * 100).toFixed(1) + ' %';
    }
    const can1DBitrate = document.getElementById('can1-dbitrate');
    if (can1DBitrate && data.can1DBitrate != null) {
        can1DBitrate.textContent = formatBitrateLabel(data.can1DBitrate);
    }
    const can1DSamplePt = document.getElementById('can1-dsamplept');
    if (can1DSamplePt && data.can1DSamplePt != null) {
        can1DSamplePt.textContent = (data.can1DSamplePt * 100).toFixed(1) + ' %';
    }
    const can1BusState = document.getElementById('can1-busstate');
    if (can1BusState && data.can1BusState != null) {
        can1BusState.textContent = data.can1BusState;
        const st = data.can1BusState;
        if (st === 'ERROR-ACTIVE') can1BusState.style.color = 'var(--green)';
        else if (st.includes('WARNING')) can1BusState.style.color = 'var(--yellow)';
        else if (st.includes('PASSIVE')) can1BusState.style.color = 'var(--orange)';
        else if (st.includes('BUS-OFF') || st.includes('STOPPED')) can1BusState.style.color = 'var(--red)';
        else can1BusState.style.color = '';
    }

    // -- CAN0 statistics table --
    setTxt('can0-tx-frames', data.can0TxFrames);
    setTxt('can0-rx-frames', data.can0RxFrames ?? data.can0Total);
    setTxt('can0-tx-errors', data.can0TxErrors);
    setTxt('can0-rx-errors', data.can0RxErrors);
    setTxt('can0-bus-errors', data.can0BusErrors);
    setTxt('can0-restarts', data.can0Restarts);

    // -- CAN1 statistics table --
    setTxt('can1-tx-frames', data.can1TxFrames);
    setTxt('can1-rx-frames', data.can1RxFrames ?? data.can1Total);
    setTxt('can1-tx-errors', data.can1TxErrors);
    setTxt('can1-rx-errors', data.can1RxErrors);
    setTxt('can1-bus-errors', data.can1BusErrors);
    setTxt('can1-restarts', data.can1Restarts);
};

/* ========================================================================== */
/*  Helpers                                                                    */
/* ========================================================================== */

const setTxt = (id, val) => {
    const el = document.getElementById(id);
    if (el && val != null) el.textContent = formatNumber(val);
};

const formatBitrateLabel = (bps) => {
    if (bps >= 1000000) return `${(bps / 1000000).toFixed(bps % 1000000 === 0 ? 0 : 1)} Mbit/s`;
    if (bps >= 1000) return `${(bps / 1000).toFixed(bps % 1000 === 0 ? 0 : 3).replace(/\.0+$/, '')} kbit/s`;
    return `${bps} bit/s`;
};

/* ========================================================================== */
/*  CAN Diagnostics                                                            */
/* ========================================================================== */

const loadDiagnostics = async () => {
    const iface = document.getElementById('can-diag-iface')?.value || 'can0';
    const output = document.getElementById('can-diag-output');
    if (!output) return;

    output.textContent = 'Running diagnostics...';

    try {
        const results = await window.api(`/api/can/diagnostics?iface=${iface}`);
        output.textContent = formatDiagnostics(results, iface);
    } catch {
        output.textContent = 'Failed to load diagnostics.';
    }
};

const formatDiagnostics = (results, iface) => {
    if (!results || !Array.isArray(results)) {
        return typeof results === 'object' ? JSON.stringify(results, null, 2) : String(results);
    }

    const lines = [`=== Diagnostics: ${iface} ===`, ''];

    for (const item of results) {
        const icon = item.status === 'PASS' ? '[OK]' :
                     item.status === 'WARN' ? '[!!]' : '[XX]';
        lines.push(`${icon} ${item.name || item.check || 'check'}`);
        if (item.detail) lines.push(`    ${item.detail}`);
    }

    // Overall status
    const hasError = results.some(r => r.status === 'FAIL' || r.status === 'ERROR');
    const hasWarn = results.some(r => r.status === 'WARN');
    const overall = hasError ? 'ERROR' : hasWarn ? 'WARNING' : 'OK';
    lines.push('', `Overall: ${overall}`);

    return lines.join('\n');
};

/* ========================================================================== */
/*  CAN Interface Details                                                      */
/* ========================================================================== */

const loadCANDetails = async () => {
    const toggle = document.getElementById('can-details-iface-toggle');
    const active = toggle?.querySelector('.iface-toggle-option.active');
    const iface = active?.dataset.iface || 'can0';
    const body = document.getElementById('can-details-body');
    if (!body) return;

    body.innerHTML = '<div class="can-details-placeholder">Loading...</div>';

    try {
        const info = await window.api(`/api/can/details?iface=${iface}`);
        body.innerHTML = renderCANDetails(info);
    } catch {
        body.innerHTML = '<div class="can-details-placeholder">Failed to load details.</div>';
    }
};

const renderCANDetails = (info) => {
    if (!info) return '<div class="can-details-placeholder">No data.</div>';

    const row = (label, value, unit = '', extraAttr = '') => {
        const v = value != null ? value : '--';
        return `<div class="can-details-row"><span class="can-details-label">${label}</span><span class="can-details-value"${extraAttr}>${v}${unit}</span></div>`;
    };

    const section = (title, content) => `
        <div class="can-details-section">
            <div class="can-details-section-title">${title}</div>
            ${content}
        </div>
    `;

    const busStateColor = (state) => {
        if (!state) return '';
        if (state === 'ERROR-ACTIVE') return ' style="color:var(--green)"';
        if (state.includes('WARNING')) return ' style="color:var(--yellow)"';
        if (state.includes('PASSIVE')) return ' style="color:var(--orange)"';
        if (state.includes('BUS-OFF') || state.includes('STOPPED')) return ' style="color:var(--red)"';
        return '';
    };

    const fmtBitrate = (bps) => {
        if (!bps) return '--';
        if (bps >= 1000000) return `${(bps / 1000000).toFixed(bps % 1000000 === 0 ? 0 : 1)} Mbit/s`;
        if (bps >= 1000) return `${(bps / 1000).toFixed(0)} kbit/s`;
        return `${bps} bit/s`;
    };

    const fmtClock = (hz) => {
        if (!hz) return '--';
        if (hz >= 1000000) return `${(hz / 1000000).toFixed(0)} MHz`;
        if (hz >= 1000) return `${(hz / 1000).toFixed(0)} kHz`;
        return `${hz} Hz`;
    };

    const general = `
        ${row('Interface', info.interface)}
        ${row('State', info.state)}
        ${row('MTU', info.mtu)}
        ${row('Bus State', info.busState, '', busStateColor(info.busState))}
        ${row('Controller', info.controller)}
        ${row('Restart-MS', info.restartMs)}
        ${row('Berr-Counter', info.berrTx != null ? `tx ${info.berrTx} / rx ${info.berrRx}` : '--')}
        ${row('Clock', fmtClock(info.clock))}
    `;

    const nominal = `
        ${row('Bitrate', fmtBitrate(info.bitrate))}
        ${row('Sample Point', info.samplePoint != null ? `${(info.samplePoint * 100).toFixed(1)} %` : '--')}
        ${row('TQ', info.tq)}
        ${row('Prop-Seg', info.propSeg)}
        ${row('Phase-Seg1', info.phaseSeg1)}
        ${row('Phase-Seg2', info.phaseSeg2)}
        ${row('SJW', info.sjw)}
        ${row('BRP', info.brp)}
    `;

    const data = `
        ${row('Bitrate', fmtBitrate(info.dbitrate))}
        ${row('Sample Point', info.dsamplePoint != null ? `${(info.dsamplePoint * 100).toFixed(1)} %` : '--')}
        ${row('TQ', info.dtq)}
        ${row('Prop-Seg', info.dpropSeg)}
        ${row('Phase-Seg1', info.dphaseSeg1)}
        ${row('Phase-Seg2', info.dphaseSeg2)}
        ${row('SJW', info.dsjw)}
        ${row('BRP', info.dbrp)}
    `;

    return section('General', general) +
           section('Nominal Timing', nominal) +
           section('Data Timing (CAN-FD)', data);
};

/* ========================================================================== */
/*  CAN Time Sync                                                              */
/* ========================================================================== */

const updateTimeSyncBadge = (enabled, iface) => {
    const badge = document.getElementById('timesync-status-badge');
    if (!badge) return;
    badge.className = 'card-header-badge ' + (enabled ? 'timesync-badge-running' : 'timesync-badge-stopped');
    badge.textContent = enabled ? `运行中 (${iface})` : '已停止';
};

const loadTimeSyncStatus = async () => {
    try {
        const data = await window.api('/api/can/timesync');
        const toggle = document.getElementById('timesync-enable-toggle');
        const ifaceEl = document.getElementById('timesync-iface');
        if (toggle) toggle.checked = data.enabled;
        if (ifaceEl && data.iface) ifaceEl.value = data.iface;
        updateTimeSyncBadge(data.enabled, data.iface);
    } catch {
        // Non-critical, ignore
    }
};

const applyTimeSyncConfig = async () => {
    const enabled = document.getElementById('timesync-enable-toggle')?.checked ?? false;
    const iface = document.getElementById('timesync-iface')?.value || 'can0';

    try {
        const data = await window.api('/api/can/timesync', {
            method: 'POST',
            body: JSON.stringify({ enabled, iface })
        });
        updateTimeSyncBadge(data.enabled, data.iface);
        const msg = data.enabled
            ? `时间同步已在 ${data.iface} 上启动`
            : '时间同步已停止';
        window.showToast(msg, data.enabled ? 'success' : 'info');
    } catch {
        // api() already shows error toast; revert toggle
        const toggle = document.getElementById('timesync-enable-toggle');
        if (toggle) toggle.checked = !enabled;
    }
};

/* ========================================================================== */
/*  Event Wiring                                                               */
/* ========================================================================== */

const applyCANConfig = async () => {
    const iface = document.getElementById('can-cfg-iface')?.value || 'can0';
    const bitrate = parseInt(document.getElementById('can-cfg-bitrate')?.value || '0', 10);
    const sp = parseFloat(document.getElementById('can-cfg-sp')?.value || '0');
    const dbitrate = parseInt(document.getElementById('can-cfg-dbitrate')?.value || '0', 10);
    const dsp = parseFloat(document.getElementById('can-cfg-dsp')?.value || '0');
    const fd = document.getElementById('can-cfg-fd')?.checked ?? true;

    if (!bitrate || bitrate <= 0) {
        window.showToast('Bitrate is required', 'error');
        return;
    }

    const btn = document.getElementById('can-cfg-apply');
    if (btn) { btn.disabled = true; btn.textContent = 'Applying...'; }

    try {
        await window.api('/api/can/configure', {
            method: 'POST',
            body: JSON.stringify({
                interface: iface,
                bitrate: bitrate,
                samplePoint: sp,
                dbitrate: dbitrate,
                dsamplePoint: dsp,
                fd: fd
            })
        });
        window.showToast('CAN configuration applied. Interface restarted.', 'success');
    } catch (err) {
        // api() already shows toast on error
    } finally {
        if (btn) { btn.disabled = false; btn.textContent = 'Apply & Restart'; }
    }
};

const wireEvents = () => {
    // Diagnostics refresh button
    document.getElementById('can-diag-refresh')?.addEventListener('click', loadDiagnostics);

    // CAN Details
    document.getElementById('can-details-refresh')?.addEventListener('click', loadCANDetails);
    const toggle = document.getElementById('can-details-iface-toggle');
    if (toggle) {
        toggle.addEventListener('click', (e) => {
            const opt = e.target.closest('.iface-toggle-option');
            if (!opt) return;
            toggle.querySelectorAll('.iface-toggle-option').forEach((el) => el.classList.remove('active'));
            opt.classList.add('active');
            loadCANDetails();
        });
    }

    // Bitrate Configuration
    document.getElementById('can-cfg-apply')?.addEventListener('click', applyCANConfig);

    // Time Sync toggle
    const timeSyncToggle = document.getElementById('timesync-enable-toggle');
    if (timeSyncToggle) {
        timeSyncToggle.addEventListener('change', applyTimeSyncConfig);
    }
    const timeSyncIface = document.getElementById('timesync-iface');
    if (timeSyncIface) {
        timeSyncIface.addEventListener('change', () => {
            if (document.getElementById('timesync-enable-toggle')?.checked) {
                applyTimeSyncConfig();
            }
        });
    }
};

/* ========================================================================== */
/*  Init                                                                       */
/* ========================================================================== */

window.initCAN = () => {
    loadDiagnostics();
    loadCANDetails();
    loadTimeSyncStatus();
};

// Wire up events immediately when DOM is ready; register lazy init for tab switch.
document.addEventListener('DOMContentLoaded', () => {
    wireEvents();
    window.registerTabInit('can', window.initCAN);
});
