/**
 * serial.js - Serial log viewer tab for the RKCAN Dashboard.
 * Handles port selection, open/close, SSE log streaming, and send.
 */

(function () {
    'use strict';

    /* ---------- Constants ---------- */
    var MAX_LINES = 5000;

    /* ---------- State ---------- */
    var initialized = false;
    var sseSource = null;
    var paused = false;
    var lineCount = 0;
    var userScrolled = false;

    /* ---------- Helpers ---------- */

    function $(id) {
        return document.getElementById(id);
    }

    function setDisabled(id, disabled) {
        var el = $(id);
        if (el) el.disabled = disabled;
    }

    function formatTimestamp() {
        var d = new Date();
        var hh = String(d.getHours()).padStart(2, '0');
        var mm = String(d.getMinutes()).padStart(2, '0');
        var ss = String(d.getSeconds()).padStart(2, '0');
        var ms = String(d.getMilliseconds()).padStart(3, '0');
        return hh + ':' + mm + ':' + ss + '.' + ms;
    }

    /* ---------- Port Loading ---------- */

    function loadPorts() {
        window.api('/api/serial/ports').then(function (data) {
            var ports = Array.isArray(data) ? data : (data && data.ports) || [];
            var select = $('serial-port');
            if (!select) return;

            // Preserve current selection
            var current = select.value;
            select.innerHTML = '<option value="">-- Select Port --</option>';

            for (var i = 0; i < ports.length; i++) {
                var p = typeof ports[i] === 'string' ? ports[i] : ports[i].path || ports[i].name || '';
                if (!p) continue;
                var opt = document.createElement('option');
                opt.value = p;
                opt.textContent = p;
                select.appendChild(opt);
            }

            // Restore selection if still available
            if (current) select.value = current;
        }).catch(function () { /* toast handled by api() */ });
    }

    /* ---------- Open / Close Port ---------- */

    function openPort() {
        var port = ($('serial-port') || {}).value;
        if (!port) {
            window.showToast('Please select a port', 'error');
            return;
        }

        var baudRate = parseInt(($('serial-baud') || {}).value, 10) || 115200;
        var dataBits = parseInt(($('serial-databits') || {}).value, 10) || 8;
        var stopBits = parseInt(($('serial-stopbits') || {}).value, 10) || 1;
        var parity = ($('serial-parity') || {}).value || 'none';

        window.api('/api/serial/open', {
            method: 'POST',
            body: JSON.stringify({
                port: port,
                baudRate: baudRate,
                dataBits: dataBits,
                stopBits: stopBits,
                parity: parity,
            }),
        }).then(function () {
            window.showToast('Port ' + port + ' opened', 'info');
            setPortOpenState(true);
            connectSSE();
        }).catch(function () { /* toast handled by api() */ });
    }

    function closePort() {
        disconnectSSE();

        window.api('/api/serial/close', { method: 'POST' }).then(function () {
            window.showToast('Port closed', 'info');
            setPortOpenState(false);
        }).catch(function () { /* toast handled by api() */ });
    }

    function setPortOpenState(isOpen) {
        setDisabled('serial-open-btn', isOpen);
        setDisabled('serial-close-btn', !isOpen);
        setDisabled('serial-send-btn', !isOpen);
        setDisabled('serial-port', isOpen);
        setDisabled('serial-baud', isOpen);
        setDisabled('serial-databits', isOpen);
        setDisabled('serial-stopbits', isOpen);
        setDisabled('serial-parity', isOpen);
    }

    /* ---------- SSE Streaming ---------- */

    function connectSSE() {
        disconnectSSE();
        paused = false;

        sseSource = new EventSource('/api/serial/sse');

        sseSource.onmessage = function (event) {
            if (paused) return;
            appendLine(event.data);
        };

        sseSource.onerror = function () {
            // Connection may close when port is closed; this is expected
            disconnectSSE();
        };
    }

    function disconnectSSE() {
        if (sseSource) {
            sseSource.close();
            sseSource = null;
        }
    }

    function togglePause() {
        paused = !paused;
        window.showToast(paused ? 'Log paused' : 'Log resumed', 'info');
    }

    /* ---------- Terminal Display ---------- */

    function appendLine(text) {
        var output = $('serial-output');
        if (!output) return;

        var showHex = $('serial-show-hex');
        var displayText = text;
        if (showHex && showHex.checked) {
            displayText = toHex(text);
        }

        var timestamp = formatTimestamp();
        var line = document.createElement('span');
        line.className = 'log-line';
        line.textContent = '[' + timestamp + '] ' + displayText + '\n';
        output.appendChild(line);
        lineCount++;

        // Enforce max lines
        while (lineCount > MAX_LINES) {
            var first = output.firstChild;
            if (first) {
                output.removeChild(first);
                lineCount--;
            } else {
                break;
            }
        }

        // Auto-scroll if user hasn't scrolled up
        if (!userScrolled) {
            var container = output.parentElement;
            if (container) container.scrollTop = container.scrollHeight;
        }
    }

    function toHex(str) {
        var hex = '';
        for (var i = 0; i < str.length; i++) {
            var code = str.charCodeAt(i).toString(16).toUpperCase();
            hex += (code.length < 2 ? '0' : '') + code + ' ';
        }
        return hex.trim();
    }

    function clearTerminal() {
        var output = $('serial-output');
        if (output) output.innerHTML = '';
        lineCount = 0;
    }

    function setupScrollDetection() {
        var container = $('serial-output');
        if (!container) container = $('serial-output');
        var parent = container ? container.parentElement : null;
        if (!parent) return;

        parent.addEventListener('scroll', function () {
            var autoScrollCb = $('serial-autoscroll');
            if (autoScrollCb && !autoScrollCb.checked) {
                userScrolled = true;
                return;
            }
            // Consider "at bottom" if within 50px of the end
            var atBottom = parent.scrollHeight - parent.scrollTop - parent.clientHeight < 50;
            userScrolled = !atBottom;
        });
    }

    /* ---------- Send Data ---------- */

    function sendData() {
        var input = $('serial-send-input');
        if (!input || !input.value) return;

        var data = input.value;
        var sendHex = $('serial-send-hex');
        var sendNewline = $('serial-send-newline');

        if (sendNewline && sendNewline.checked) {
            data += '\n';
        }

        window.api('/api/serial/send', {
            method: 'POST',
            body: JSON.stringify({
                data: data,
                hex: sendHex ? sendHex.checked : false,
            }),
        }).then(function () {
            input.value = '';
        }).catch(function () { /* toast handled by api() */ });
    }

    /* ---------- Event Listeners ---------- */

    function setupListeners() {
        var openBtn = $('serial-open-btn');
        if (openBtn) openBtn.addEventListener('click', openPort);

        var closeBtn = $('serial-close-btn');
        if (closeBtn) closeBtn.addEventListener('click', closePort);

        var refreshBtn = $('serial-refresh-ports-btn');
        if (refreshBtn) refreshBtn.addEventListener('click', loadPorts);

        var clearBtn = $('serial-clear-btn');
        if (clearBtn) clearBtn.addEventListener('click', clearTerminal);

        var sendBtn = $('serial-send-btn');
        if (sendBtn) sendBtn.addEventListener('click', sendData);

        // Enter key in send input triggers send
        var sendInput = $('serial-send-input');
        if (sendInput) {
            sendInput.addEventListener('keydown', function (e) {
                if (e.key === 'Enter') sendData();
            });
        }

        // Auto-scroll checkbox
        var autoScrollCb = $('serial-autoscroll');
        if (autoScrollCb) {
            autoScrollCb.addEventListener('change', function () {
                userScrolled = !autoScrollCb.checked;
                if (autoScrollCb.checked) {
                    var output = $('serial-output');
                    var parent = output ? output.parentElement : null;
                    if (parent) parent.scrollTop = parent.scrollHeight;
                }
            });
        }

        setupScrollDetection();
    }

    /* ---------- Public API ---------- */

    window.initSerial = function () {
        if (!initialized) {
            setupListeners();
            initialized = true;
        }
        loadPorts();
        restoreState();
    };

    // Re-sync UI with a port that is already open on the device (e.g. after
    // a page reload).
    function restoreState() {
        fetch('/api/serial/status').then(function (resp) {
            return resp.ok ? resp.json() : null;
        }).then(function (st) {
            if (!st) return;
            setPortOpenState(!!st.open);
            if (st.open) {
                var c = st.config || {};
                var sel = $('serial-port');
                if (sel && c.port) {
                    if (!Array.prototype.some.call(sel.options, function (o) { return o.value === c.port; })) {
                        var opt = document.createElement('option');
                        opt.value = c.port;
                        opt.textContent = c.port;
                        sel.appendChild(opt);
                    }
                    sel.value = c.port;
                }
                if (c.baudRate && $('serial-baud')) $('serial-baud').value = String(c.baudRate);
                if (!sseSource) connectSSE();
            }
        }).catch(function () { /* non-critical */ });
    }

    window.registerTabInit('serial', window.initSerial);

})();
