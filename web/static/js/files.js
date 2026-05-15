/**
 * files.js - File manager tab.
 */

(function () {
    'use strict';

    var initialized = false;
    var currentPath = '/';

    function $(id) { return document.getElementById(id); }

    function fileIcon(entry) {
        if (entry.isDir) {
            return '<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="#eab308" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">' +
                '<path d="M22 19a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h5l2 3h9a2 2 0 0 1 2 2z"/></svg>';
        }

        var ext = (entry.name || '').split('.').pop().toLowerCase();

        if (['png','jpg','jpeg','gif','bmp','svg','webp','ico'].indexOf(ext) >= 0) {
            return '<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="#22c55e" stroke-width="2"><rect x="3" y="3" width="18" height="18" rx="2"/><circle cx="8.5" cy="8.5" r="1.5"/><polyline points="21 15 16 10 5 21"/></svg>';
        }

        if (['zip','tar','gz','bz2','xz','7z','rar','tgz'].indexOf(ext) >= 0) {
            return '<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="#f97316" stroke-width="2"><path d="M21 8v13H3V8"/><path d="M1 3h22v5H1z"/><path d="M10 12h4"/></svg>';
        }

        if (['sh','bin','exe','elf','out'].indexOf(ext) >= 0) {
            return '<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="#a855f7" stroke-width="2"><polyline points="4 17 10 11 4 5"/><line x1="12" y1="19" x2="20" y2="19"/></svg>';
        }

        if (['txt','log','md','json','yaml','yml','xml','csv','ini','conf','cfg','toml','js','ts','py','go','c','h','cpp','rs','html','css'].indexOf(ext) >= 0) {
            return '<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="#3b82f6" stroke-width="2"><path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z"/><polyline points="14 2 14 8 20 8"/><line x1="16" y1="13" x2="8" y2="13"/><line x1="16" y1="17" x2="8" y2="17"/></svg>';
        }

        return '<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z"/><polyline points="14 2 14 8 20 8"/></svg>';
    }

    function formatDate(d) {
        if (!d) return '--';
        var dt = new Date(d);
        if (isNaN(dt.getTime())) return String(d);
        var yyyy = dt.getFullYear();
        var mm = String(dt.getMonth() + 1).padStart(2, '0');
        var dd = String(dt.getDate()).padStart(2, '0');
        var hh = String(dt.getHours()).padStart(2, '0');
        var mi = String(dt.getMinutes()).padStart(2, '0');
        return yyyy + '-' + mm + '-' + dd + ' ' + hh + ':' + mi;
    }

    function joinPath(base, name) {
        if (base === '/') return '/' + name;
        return base.replace(/\/+$/, '') + '/' + name;
    }

    function renderBreadcrumb(path) {
        var container = $('file-breadcrumb-path');
        if (!container) return;

        var parts = path.split('/').filter(function (p) { return p.length > 0; });
        var html = '<span class="breadcrumb-item" data-path="/">/</span>';
        var accumulated = '';

        for (var i = 0; i < parts.length; i++) {
            accumulated += '/' + parts[i];
            html += ' <span style="color:#64748b;">/</span> ';
            html += '<span class="breadcrumb-item" data-path="' + window.escapeHtml(accumulated) + '">' +
                window.escapeHtml(parts[i]) + '</span>';
        }

        container.innerHTML = html;

        container.querySelectorAll('.breadcrumb-item').forEach(function (item) {
            item.addEventListener('click', function () {
                navigateTo(item.dataset.path);
            });
        });
    }

    function loadFiles(path) {
        if (path === undefined || path === null) path = currentPath;
        currentPath = path;
        renderBreadcrumb(path);

        window.api('/api/files/list?path=' + encodeURIComponent(path)).then(function (data) {
            var files = Array.isArray(data) ? data : (data && data.files) || [];
            renderFileTable(files);
        }).catch(function () {});
    }

    function navigateTo(path) { loadFiles(path); }

    function renderFileTable(files) {
        var tbody = $('file-tbody');
        if (!tbody) return;

        if (files.length === 0) {
            tbody.innerHTML = '<tr><td colspan="6" style="text-align:center;color:#64748b;">Empty directory</td></tr>';
            return;
        }

        files.sort(function (a, b) {
            if (a.isDir && !b.isDir) return -1;
            if (!a.isDir && b.isDir) return 1;
            return (a.name || '').localeCompare(b.name || '');
        });

        var html = '';
        for (var i = 0; i < files.length; i++) {
            var f = files[i];
            var name = window.escapeHtml(f.name || '');
            var fullPath = joinPath(currentPath, f.name);

            html += '<tr>';
            html += '<td class="file-col-icon">' + fileIcon(f) + '</td>';

            if (f.isDir) {
                html += '<td class="file-col-name"><a href="#" class="file-nav-link" data-path="' +
                    window.escapeHtml(fullPath) + '">' + name + '</a></td>';
                html += '<td class="file-col-size">--</td>';
            } else {
                html += '<td class="file-col-name">' + name + '</td>';
                html += '<td class="file-col-size">' + window.formatBytes(f.size || 0) + '</td>';
            }

            html += '<td class="file-col-modified">' + formatDate(f.modTime || f.modified) + '</td>';
            html += '<td class="file-col-perm">' + window.escapeHtml(f.mode || f.permissions || '--') + '</td>';

            html += '<td class="file-col-actions">';
            if (!f.isDir) {
                html += '<button class="btn btn-sm file-download-btn" data-path="' +
                    window.escapeHtml(fullPath) + '">DL</button> ';
            }
            html += '<button class="btn btn-sm file-rename-btn" data-path="' +
                window.escapeHtml(fullPath) + '" data-name="' + window.escapeHtml(f.name) + '">Ren</button> ';
            html += '<button class="btn btn-sm file-delete-btn" data-path="' +
                window.escapeHtml(fullPath) + '" data-name="' + window.escapeHtml(f.name) + '">Del</button>';
            html += '</td>';
            html += '</tr>';
        }

        tbody.innerHTML = html;
        attachFileActions(tbody);
    }

    function attachFileActions(tbody) {
        tbody.querySelectorAll('.file-nav-link').forEach(function (link) {
            link.addEventListener('click', function (e) {
                e.preventDefault();
                navigateTo(link.dataset.path);
            });
        });

        tbody.querySelectorAll('.file-download-btn').forEach(function (btn) {
            btn.addEventListener('click', function () { downloadFile(btn.dataset.path); });
        });

        tbody.querySelectorAll('.file-rename-btn').forEach(function (btn) {
            btn.addEventListener('click', function () { renameFile(btn.dataset.path, btn.dataset.name); });
        });

        tbody.querySelectorAll('.file-delete-btn').forEach(function (btn) {
            btn.addEventListener('click', function () { deleteFile(btn.dataset.path, btn.dataset.name); });
        });
    }

    function downloadFile(path) {
        window.open('/api/files/download?path=' + encodeURIComponent(path), '_blank');
    }

    function deleteFile(path, name) {
        if (!confirm('Delete "' + name + '"?\nThis cannot be undone.')) return;

        window.api('/api/files/delete', {
            method: 'POST',
            body: JSON.stringify({ path: path }),
        }).then(function () {
            window.showToast('Deleted: ' + name, 'info');
            loadFiles();
        }).catch(function () {});
    }

    function renameFile(oldPath, oldName) {
        var newName = prompt('Rename "' + oldName + '" to:', oldName);
        if (!newName || newName === oldName) return;

        var parentDir = currentPath;
        var newPath = joinPath(parentDir, newName);

        window.api('/api/files/rename', {
            method: 'POST',
            body: JSON.stringify({ oldPath: oldPath, newPath: newPath }),
        }).then(function () {
            window.showToast('Renamed to: ' + newName, 'info');
            loadFiles();
        }).catch(function () {});
    }

    function openMkdirModal() {
        var modal = $('mkdir-modal');
        var nameInput = $('mkdir-name');
        if (!modal) return;
        if (nameInput) nameInput.value = '';
        modal.classList.add('active');
        if (nameInput) nameInput.focus();
    }

    function closeMkdirModal() {
        var modal = $('mkdir-modal');
        if (modal) modal.classList.remove('active');
    }

    function submitMkdir() {
        var nameInput = $('mkdir-name');
        var name = nameInput ? nameInput.value.trim() : '';
        if (!name) {
            window.showToast('Folder name is required', 'error');
            return;
        }

        var fullPath = joinPath(currentPath, name);

        window.api('/api/files/mkdir', {
            method: 'POST',
            body: JSON.stringify({ path: fullPath }),
        }).then(function () {
            window.showToast('Created folder: ' + name, 'info');
            closeMkdirModal();
            loadFiles();
        }).catch(function () {});
    }

    function uploadFiles(fileList) {
        if (!fileList || fileList.length === 0) return;

        var progressEl = $('upload-progress');
        var progressBar = $('upload-progress-bar');
        var progressText = $('upload-progress-text');

        if (progressEl) progressEl.hidden = false;
        if (progressBar) progressBar.style.width = '0%';
        if (progressText) progressText.textContent = '0/' + fileList.length;

        var uploaded = 0;
        var total = fileList.length;

        function uploadNext(idx) {
            if (idx >= total) {
                window.showToast('Upload complete (' + total + ' file' + (total > 1 ? 's' : '') + ')', 'success');
                loadFiles();
                if (progressEl) progressEl.hidden = true;
                return;
            }

            var formData = new FormData();
            formData.append('path', currentPath);
            formData.append('file', fileList[idx]);

            fetch('/api/files/upload', {
                method: 'POST',
                body: formData,
            }).then(function (resp) {
                if (!resp.ok) {
                    return resp.text().then(function (t) { throw new Error(t || 'Upload failed'); });
                }
                return resp.json();
            }).then(function () {
                uploaded++;
                if (progressText) progressText.textContent = uploaded + '/' + total;
                uploadNext(idx + 1);
            }).catch(function (err) {
                window.showToast('Upload error: ' + err.message, 'error');
                if (progressEl) progressEl.hidden = true;
            });
        }

        uploadNext(0);
    }

    function setupDragAndDrop() {
        var zone = document.querySelector('.file-drop-target');
        if (!zone) return;

        var dragCounter = 0;

        zone.addEventListener('dragenter', function (e) {
            e.preventDefault();
            dragCounter++;
            zone.classList.add('drag-over');
        });

        zone.addEventListener('dragleave', function (e) {
            e.preventDefault();
            dragCounter--;
            if (dragCounter <= 0) {
                dragCounter = 0;
                zone.classList.remove('drag-over');
            }
        });

        zone.addEventListener('dragover', function (e) { e.preventDefault(); });

        zone.addEventListener('drop', function (e) {
            e.preventDefault();
            dragCounter = 0;
            zone.classList.remove('drag-over');
            if (e.dataTransfer && e.dataTransfer.files.length > 0) {
                uploadFiles(e.dataTransfer.files);
            }
        });
    }

    function setupListeners() {
        var uploadBtn = $('file-upload-btn');
        var fileInput = $('file-upload-input');
        if (uploadBtn && fileInput) {
            uploadBtn.addEventListener('click', function () { fileInput.click(); });
        }
        if (fileInput) {
            fileInput.addEventListener('change', function () {
                if (fileInput.files.length > 0) {
                    uploadFiles(fileInput.files);
                    fileInput.value = '';
                }
            });
        }

        var mkdirBtn = $('file-mkdir-btn');
        if (mkdirBtn) mkdirBtn.addEventListener('click', openMkdirModal);

        var refreshBtn = $('file-refresh-btn');
        if (refreshBtn) refreshBtn.addEventListener('click', function () { loadFiles(); });

        var mkdirClose = $('mkdir-modal-close');
        if (mkdirClose) mkdirClose.addEventListener('click', closeMkdirModal);

        var mkdirCancel = $('mkdir-cancel');
        if (mkdirCancel) mkdirCancel.addEventListener('click', closeMkdirModal);

        var mkdirSubmit = $('mkdir-submit');
        if (mkdirSubmit) mkdirSubmit.addEventListener('click', submitMkdir);

        var mkdirModal = $('mkdir-modal');
        if (mkdirModal) {
            var overlay = mkdirModal.querySelector('.modal-overlay');
            if (overlay) overlay.addEventListener('click', closeMkdirModal);
        }

        var mkdirName = $('mkdir-name');
        if (mkdirName) {
            mkdirName.addEventListener('keydown', function (e) {
                if (e.key === 'Enter') submitMkdir();
            });
        }

        setupDragAndDrop();
    }

    window.initFiles = function () {
        if (!initialized) {
            setupListeners();
            initialized = true;
        }
        loadFiles('/');
    };

    window.registerTabInit('files', window.initFiles);

})();
