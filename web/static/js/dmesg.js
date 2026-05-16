/**
 * dmesg.js - Dmesg log viewer for the RKCAN Dashboard.
 */

(function () {
    var outputEl = document.getElementById('dmesg-output');
    var refreshBtn = document.getElementById('dmesg-refresh-btn');
    var clearBtn = document.getElementById('dmesg-clear-btn');
    var hasLoaded = false;

    var loadDmesg = function () {
        if (!outputEl) return;
        outputEl.textContent = 'Loading...';

        window.api('/api/system/dmesg')
            .then(function (data) {
                if (data.dmesg != null) {
                    outputEl.textContent = data.dmesg;
                    hasLoaded = true;
                } else {
                    outputEl.textContent = 'No data returned.';
                }
            })
            .catch(function () {
                if (outputEl) outputEl.textContent = 'Failed to load dmesg.';
            });
    };

    var clearOutput = function () {
        if (!outputEl) return;
        outputEl.textContent = 'Click Refresh to load dmesg...';
        hasLoaded = false;
    };

    var setup = function () {
        if (refreshBtn) {
            refreshBtn.addEventListener('click', loadDmesg);
        }
        if (clearBtn) {
            clearBtn.addEventListener('click', clearOutput);
        }
    };

    window.registerTabInit('dmesg', function () {
        if (!hasLoaded) {
            loadDmesg();
        }
    });

    document.addEventListener('DOMContentLoaded', setup);
})();
