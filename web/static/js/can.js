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
        const fps = data.can0Fps != null ? data.can0Fps : 0;
        can0State.textContent = fps > 0 ? `UP (${formatNumber(fps)} fps)` : 'DOWN';
        can0State.classList.toggle('text-ok', fps > 0);
        can0State.classList.toggle('text-err', fps === 0);
    }
    if (can0Bitrate && data.can0Bitrate != null) {
        can0Bitrate.textContent = formatBitrateLabel(data.can0Bitrate);
    }

    // -- CAN1 status cards --
    const can1State = document.getElementById('can1-state');
    const can1Bitrate = document.getElementById('can1-bitrate');
    if (can1State) {
        const fps = data.can1Fps != null ? data.can1Fps : 0;
        can1State.textContent = fps > 0 ? `UP (${formatNumber(fps)} fps)` : 'DOWN';
        can1State.classList.toggle('text-ok', fps > 0);
        can1State.classList.toggle('text-err', fps === 0);
    }
    if (can1Bitrate && data.can1Bitrate != null) {
        can1Bitrate.textContent = formatBitrateLabel(data.can1Bitrate);
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
    if (bps >= 1000) return `${(bps / 1000).toFixed(bps % 1000 === 0 ? 0 : 3).replace(/\.?0+$/, '')} kbit/s`;
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
    const iface = document.getElementById('can-details-iface')?.value || 'can0';
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
    `;

    const nominal = `
        ${row('Bitrate', fmtBitrate(info.bitrate))}
        ${row('Sample Point', info.samplePoint != null ? `${(info.samplePoint * 100).toFixed(1)} %` : '--')}
        ${row('Clock', fmtClock(info.clock))}
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
/*  Event Wiring                                                               */
/* ========================================================================== */

const wireEvents = () => {
    // Diagnostics refresh button
    document.getElementById('can-diag-refresh')?.addEventListener('click', loadDiagnostics);

    // CAN Details
    document.getElementById('can-details-refresh')?.addEventListener('click', loadCANDetails);
    document.getElementById('can-details-iface')?.addEventListener('change', loadCANDetails);
};

/* ========================================================================== */
/*  Init                                                                       */
/* ========================================================================== */

window.initCAN = () => {
    loadDiagnostics();
    loadCANDetails();
};

// Wire up events immediately when DOM is ready; register lazy init for tab switch.
document.addEventListener('DOMContentLoaded', () => {
    wireEvents();
    window.registerTabInit('can', window.initCAN);
});
